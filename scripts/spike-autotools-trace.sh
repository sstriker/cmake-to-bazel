#!/bin/sh
# spike-autotools-trace.sh — round-trip the B→A trace-driven
# autotools-to-Bazel conversion against the autotools-greet
# fixture.
#
# Steps:
#   1. Build cmd/convert-element-autotools.
#   2. Stage a writable copy of the fixture's source tree.
#   3. Run ./configure + make under strace, capturing every
#      execve to a text-format trace file.
#   4. Run convert-element-autotools against the trace; emit
#      BUILD.bazel.out.
#   5. Assert the output declares cc_binary(name="greet",
#      srcs=["greet.c"], copts=["-O2"]) — the native shape the
#      same source would produce if it were a kind:cmake
#      element with `add_executable(greet greet.c)`.
#
# This is the spike-validation gate. It does NOT yet wire the
# converter into write-a's kind:autotools handler — that's
# the next slice. The spike proves the trace shape + parser
# are sufficient to recover a sensible Bazel target before we
# spend effort on the action-cache + write-a integration.

set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

bin="$work_dir/convert-element-autotools"
CGO_ENABLED=0 go build -o "$bin" ./cmd/convert-element-autotools

src="$work_dir/src"
cp -r testdata/meta-project/autotools-greet/sources "$src"

trace="$work_dir/trace.log"
build_out="$work_dir/BUILD.bazel.out"

(cd "$src" && \
    strace -f -e trace=execve -s 4096 -o "$trace" \
        -- sh -c './configure --prefix=/usr >/dev/null 2>&1 && make >/dev/null 2>&1') \
    || {
    echo "spike-autotools-trace: build failed under strace" >&2
    head -100 "$trace" >&2
    exit 1
}

"$bin" --trace "$trace" --out-build "$build_out"

# Asserts on the rendered BUILD.
for marker in \
    'cc_binary(' \
    'name = "greet"' \
    'srcs = ["greet.c"]' \
    'copts = ["-O2"]'; do
    if ! grep -qF "$marker" "$build_out"; then
        echo "spike-autotools-trace: rendered BUILD.bazel.out missing marker: $marker" >&2
        cat "$build_out" >&2
        exit 1
    fi
done
echo "spike-autotools-trace: ok"
echo "--- rendered BUILD.bazel.out ---"
cat "$build_out"
