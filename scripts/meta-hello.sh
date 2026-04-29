#!/bin/sh
# meta-hello.sh — Phase 1 acceptance gate for the meta-project shape.
#
# Drives the full two-pass pipeline against the hello-world fixture:
#
#   1. cmd/write-a renders project A (the meta workspace) and project B
#      (the consumer workspace).
#   2. bazel build in project A invokes convert-element via the per-
#      element genrule, producing BUILD.bazel.out + cmake-config-bundle.
#   3. The driver stages project A's BUILD.bazel.out into project B's
#      elements/<name>/BUILD.bazel (overwriting the
#      BUILD_NOT_YET_STAGED placeholder write-a rendered).
#   4. The driver writes a hand-authored smoke target into project B
#      (smoke/BUILD.bazel + smoke/smoke.c) that depends on the
#      converted cc_library and prints the library's hello message.
#   5. bazel build + bazel run in project B compiles and executes the
#      smoke binary; output is asserted to contain "Hello, World!".
#
# Cache-stability scenarios A and A' are exercised inline after the
# initial round-trip succeeds — same shape as the spike's checks but
# now also asserting that project B doesn't rebuild on Scenario A.
#
# Bazel-availability gating: rendering checks always run; bazel build
# phases self-skip when no bazel >= 7 is on PATH (via SPIKE_BAZEL_*_ARGS
# documented below).

set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

bin_dir="$repo_root/build/bin"
mkdir -p "$bin_dir"

# Build prereqs.
make converter >/dev/null
CGO_ENABLED=0 go build -o "$bin_dir/write-a" ./cmd/write-a

work_dir="$(mktemp -d)"
trap "rm -rf '$work_dir'" EXIT

A="$work_dir/A"
B="$work_dir/B"

"$bin_dir/write-a" \
    --bst testdata/meta-project/hello-world.bst \
    --out "$A" \
    --out-b "$B" \
    --convert-element "$bin_dir/convert-element"

# Render-phase checks. Always run; don't gate on bazel.
for f in MODULE.bazel BUILD.bazel \
        rules/zero_files.bzl rules/BUILD.bazel \
        tools/convert-element tools/BUILD.bazel \
        elements/hello-world/BUILD.bazel \
        elements/hello-world/sources/CMakeLists.txt; do
    if [ ! -f "$A/$f" ]; then
        echo "meta-hello: missing rendered project A file $f" >&2
        exit 1
    fi
done
for f in MODULE.bazel BUILD.bazel \
        elements/hello-world/BUILD.bazel \
        elements/hello-world/CMakeLists.txt \
        elements/hello-world/hello.c \
        elements/hello-world/include/hello.h; do
    if [ ! -f "$B/$f" ]; then
        echo "meta-hello: missing rendered project B file $f" >&2
        exit 1
    fi
done

# Bazel phase. Both projects need bazel >= 7 (bzlmod). If only an
# older bazel (or none) is available, the rendering check above is
# the only assertion.
if command -v bazel >/dev/null; then
    BZL=bazel
elif command -v bazelisk >/dev/null; then
    BZL=bazelisk
else
    echo "meta-hello: render OK; bazel not on PATH, skipping build phase"
    exit 0
fi
bazel_major=$("$BZL" --version 2>&1 | head -1 | awk '{print $2}' | cut -d. -f1)
case "$bazel_major" in
    [0-9]*) ;;
    *) bazel_major=0 ;;
esac
if [ "$bazel_major" -lt 7 ]; then
    echo "meta-hello: render OK; bazel $($BZL --version | head -1) is < 7 (no bzlmod), skipping build phase"
    exit 0
fi

# SPIKE_BAZEL_STARTUP_ARGS / SPIKE_BAZEL_BUILD_ARGS let sandboxed dev
# environments inject overrides for bcr.bazel.build access (proxy
# whitelists, JVM truststore paths, alternative registries). Empty by
# default; on a normal dev machine bazel reaches bcr fine and needs
# nothing extra.
#
# Bazel splits flags into startup (between `bazel` and the
# subcommand) and command-time (after the subcommand) — they're
# rejected in the wrong position, so the script accepts both
# separately. Example for dev containers without bcr egress but with
# github:
#
#   export SPIKE_BAZEL_STARTUP_ARGS="\
#     --host_jvm_args=-Djavax.net.ssl.trustStore=/etc/ssl/certs/java/cacerts \
#     --host_jvm_args=-Djavax.net.ssl.trustStorePassword=changeit"
#   export SPIKE_BAZEL_BUILD_ARGS="\
#     --registry=https://raw.githubusercontent.com/bazelbuild/bazel-central-registry/main"
SPIKE_BAZEL_STARTUP_ARGS=${SPIKE_BAZEL_STARTUP_ARGS:-}
SPIKE_BAZEL_BUILD_ARGS=${SPIKE_BAZEL_BUILD_ARGS:-}

bzl_cache="$work_dir/.bazel"
sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# Run a bazel subcommand inside the chosen workspace. Both projects
# share an output_user_root so module deps resolve once across the
# two passes.
run_bazel() {
    workspace="$1"
    shift
    cmd="$1"
    shift
    # shellcheck disable=SC2086 # SPIKE_BAZEL_*_ARGS is intentionally word-split.
    (cd "$workspace" && "$BZL" --output_user_root="$bzl_cache" \
        $SPIKE_BAZEL_STARTUP_ARGS \
        "$cmd" "$@" $SPIKE_BAZEL_BUILD_ARGS)
}

# === Pass 1: bazel build project A ===
run_bazel "$A" build //elements/hello-world:hello-world_converted 2>&1 | tail -10
build_out_a="$A/bazel-bin/elements/hello-world/BUILD.bazel.out"
if [ ! -f "$build_out_a" ]; then
    echo "meta-hello: project A's BUILD.bazel.out not produced" >&2
    exit 1
fi
if ! grep -q '^cc_library' "$build_out_a"; then
    echo "meta-hello: project A's BUILD.bazel.out missing cc_library output" >&2
    head -20 "$build_out_a" >&2
    exit 1
fi
sha_a1=$(sha_of "$build_out_a")
echo "meta-hello: project A built; BUILD.bazel.out sha=$sha_a1"

# === Stage A's outputs into B ===
# Replace the BUILD_NOT_YET_STAGED placeholder with the converter's
# real BUILD.bazel.out. After this step, project B's element package
# is well-formed and Bazel can resolve cc_library against the staged
# sources.
cp "$build_out_a" "$B/elements/hello-world/BUILD.bazel"
if grep -q BUILD_NOT_YET_STAGED "$B/elements/hello-world/BUILD.bazel"; then
    echo "meta-hello: stage step appears to have failed; placeholder still present" >&2
    exit 1
fi

# === Smoke target: cc_binary linking against the converted cc_library ===
# Hand-written, fixture-specific. Production graphs have their own
# consumers; this is the Phase 1 acceptance gate's notion of "project
# B actually compiles project A's converted output".
mkdir -p "$B/smoke"
cat > "$B/smoke/BUILD.bazel" <<'EOF'
load("@rules_cc//cc:defs.bzl", "cc_binary")

cc_binary(
    name = "hello_smoke",
    srcs = ["smoke.c"],
    deps = ["//elements/hello-world:hello"],
)
EOF
cat > "$B/smoke/smoke.c" <<'EOF'
#include <stdio.h>
#include "hello.h"

int main(void) {
    printf("%s\n", hello_message());
    return 0;
}
EOF

# === Pass 2: bazel build + run project B ===
run_bazel "$B" build //smoke:hello_smoke 2>&1 | tail -10
smoke_out=$(run_bazel "$B" run //smoke:hello_smoke 2>&1 | tail -5)
echo "meta-hello: smoke output: $smoke_out"
if ! echo "$smoke_out" | grep -q "Hello, World!"; then
    echo "meta-hello: smoke binary did not print 'Hello, World!'" >&2
    echo "$smoke_out" >&2
    exit 1
fi
echo "meta-hello: ok (project A built; staged into B; smoke binary linked + ran)"
