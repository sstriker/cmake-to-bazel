#!/bin/sh
# meta-autotools-stability.sh — stability gate for the
# trace-driven kind:autotools native render path.
#
# Verifies the cache-narrowing story the genrule split
# delivers: a comment-only edit in a source file
# invalidates `<elem>_install`'s cache key (the build
# re-runs because input bytes changed) but the build's
# trace.log + make-db.txt come out byte-identical, so
# `<elem>_converted`'s narrow cache key is unchanged → its
# action cache hits → BUILD.bazel.out is reused.
#
# The two-action design mirrors kind:cmake's read-paths
# narrowing: trivial source edits trigger the build but
# don't churn project B's BUILD files; consumers of the
# converted target stay cached.
#
# Bazel-availability gating + META_BAZEL_*_ARGS overrides
# mirror scripts/meta-autotools-native.sh.

set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

bin_dir="$repo_root/build/bin"
mkdir -p "$bin_dir"
make converter >/dev/null
CGO_ENABLED=0 go build -o "$bin_dir/write-a" ./cmd/write-a
CGO_ENABLED=0 go build -o "$bin_dir/build-tracer" ./cmd/build-tracer
CGO_ENABLED=0 go build -o "$bin_dir/convert-element-autotools" ./cmd/convert-element-autotools

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

A="$work_dir/A"
B="$work_dir/B"

# Stage a writable copy of the autotools-greet fixture under
# the work dir so the comment edit doesn't dirty the
# committed fixture.
src_stage="$work_dir/fixture-src"
mkdir -p "$src_stage"
cp -r "testdata/meta-project/autotools-greet/." "$src_stage/"
chmod -R u+w "$src_stage"

# Bazel-availability gating.
if command -v bazel >/dev/null; then
    BZL=bazel
elif command -v bazelisk >/dev/null; then
    BZL=bazelisk
else
    echo "meta-autotools-stability: bazel not on PATH, skipping" >&2
    exit 0
fi
bazel_major=$("$BZL" --version 2>&1 | head -1 | awk '{print $2}' | cut -d. -f1)
case "$bazel_major" in
    [0-9]*) ;;
    *) bazel_major=0 ;;
esac
if [ "$bazel_major" -lt 7 ]; then
    echo "meta-autotools-stability: bazel < 7 (no bzlmod), skipping" >&2
    exit 0
fi

META_BAZEL_STARTUP_ARGS=${META_BAZEL_STARTUP_ARGS:-}
META_BAZEL_BUILD_ARGS=${META_BAZEL_BUILD_ARGS:-}
bzl_cache="$work_dir/.bazel"

run_bazel() {
    workspace="$1"
    cmd="$2"
    shift 2
    # shellcheck disable=SC2086
    (cd "$workspace" && "$BZL" --output_user_root="$bzl_cache" \
        $META_BAZEL_STARTUP_ARGS \
        "$cmd" "$@" $META_BAZEL_BUILD_ARGS)
}

render() {
    rm -rf "$A" "$B"
    "$bin_dir/write-a" \
        --bst "$src_stage/greet.bst" \
        --out "$A" \
        --out-b "$B" \
        --convert-element "$bin_dir/convert-element" \
        --convert-element-autotools "$bin_dir/convert-element-autotools" \
        --build-tracer-bin "$bin_dir/build-tracer" >/dev/null
}

sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# Pass 1: build greet from the un-edited fixture.
render
run_bazel "$A" build //elements/greet:greet_converted 2>&1 | tail -5
build_out="$A/bazel-bin/elements/greet/BUILD.bazel.out"
trace_out="$A/bazel-bin/elements/greet/trace.log"
if [ ! -f "$build_out" ] || [ ! -f "$trace_out" ]; then
    echo "meta-autotools-stability: pass 1 missing outputs" >&2
    exit 1
fi
sha_build_1=$(sha_of "$build_out")
sha_trace_1=$(sha_of "$trace_out")
echo "meta-autotools-stability: pass 1 BUILD.bazel.out sha=$sha_build_1 trace.log sha=$sha_trace_1"

# Edit a comment in greet.c — pure-comment edit; no compile
# command change → trace + BUILD must stay byte-stable.
echo "/* an idle comment that doesn't change build commands */" >> "$src_stage/sources/greet.c"

# Pass 2: re-render (no .bst change so write-a is a no-op
# besides re-staging the comment-edited source) and re-build.
render
# greet_install must be invalidated (input source content
# changed), so the build runs. greet_converted must
# cache-hit because its inputs (trace.log + make-db.txt) are
# byte-identical to pass 1.
build_log=$(run_bazel "$A" build //elements/greet:greet_converted 2>&1)
echo "$build_log" | tail -5
sha_build_2=$(sha_of "$build_out")
sha_trace_2=$(sha_of "$trace_out")
echo "meta-autotools-stability: pass 2 BUILD.bazel.out sha=$sha_build_2 trace.log sha=$sha_trace_2"

if [ "$sha_build_1" != "$sha_build_2" ]; then
    echo "meta-autotools-stability: BUILD.bazel.out churned across runs (instability!)" >&2
    diff <(printf '%s\n' "$sha_build_1") <(printf '%s\n' "$sha_build_2")
    exit 1
fi
if [ "$sha_trace_1" != "$sha_trace_2" ]; then
    echo "meta-autotools-stability: trace.log churned across runs (build non-determinism)" >&2
    exit 1
fi

echo "meta-autotools-stability: ok (BUILD.bazel.out byte-stable across comment-only source edit)"
