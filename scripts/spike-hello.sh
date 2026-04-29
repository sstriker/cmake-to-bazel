#!/bin/sh
# spike-hello.sh — end-to-end smoke for the meta-project hello-world spike.
#
# Renders project A via cmd/write-a-spike, then drives bazel against
# it to invoke convert-element through the per-element genrule. If
# bazel isn't on PATH, the bazel-build phase self-skips and the
# script exits 0 — the rendering phase alone is still a useful
# regression check.
#
# This is the spike validation, not a permanent test surface. It
# replaces itself with a Go-based e2e test under
# orchestrator/internal/... once Phase 1's production writer-of-A
# lands and the cmd/write-a-spike/ scaffolding gets retired.

set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

bin_dir="$repo_root/build/bin"
mkdir -p "$bin_dir"

# Build prerequisites with the Makefile's pinned flags so cache lookups
# match `make converter` runs.
make converter >/dev/null
CGO_ENABLED=0 go build -o "$bin_dir/write-a-spike" ./cmd/write-a-spike

spike_dir="$(mktemp -d)"
trap "rm -rf '$spike_dir'" EXIT

"$bin_dir/write-a-spike" \
    --bst testdata/meta-project/hello-world.bst \
    --out "$spike_dir" \
    --convert-element "$bin_dir/convert-element"

# Render-phase checks. Always run; don't gate on bazel.
for f in WORKSPACE.bazel BUILD.bazel \
        rules/zero_files.bzl rules/BUILD.bazel \
        tools/convert-element tools/BUILD.bazel \
        elements/hello-world/BUILD.bazel \
        elements/hello-world/sources/CMakeLists.txt; do
    if [ ! -f "$spike_dir/$f" ]; then
        echo "spike-hello: missing rendered file $f" >&2
        exit 1
    fi
done

# Bazel phase. Skip cleanly when bazel isn't installed.
if command -v bazel >/dev/null; then
    BZL=bazel
elif command -v bazelisk >/dev/null; then
    BZL=bazelisk
else
    echo "spike-hello: render OK; bazel not on PATH, skipping build phase"
    exit 0
fi

cd "$spike_dir"
# WORKSPACE.bazel keeps the spike compatible with bazel < 6
# (no bzlmod). Newer bazel versions read it directly without
# the --enable_bzlmod flag (which bazel 4 doesn't recognize).
"$BZL" --output_user_root="$spike_dir/.bazel" build \
    //elements/hello-world:hello-world_converted 2>&1 | tail -20

# Output checks.
if [ ! -f bazel-bin/elements/hello-world/BUILD.bazel.out ]; then
    echo "spike-hello: BUILD.bazel.out not produced" >&2
    exit 1
fi
if ! grep -q '^cc_library' bazel-bin/elements/hello-world/BUILD.bazel.out; then
    echo "spike-hello: BUILD.bazel.out missing cc_library output" >&2
    head -20 bazel-bin/elements/hello-world/BUILD.bazel.out >&2
    exit 1
fi

echo "spike-hello: ok"
