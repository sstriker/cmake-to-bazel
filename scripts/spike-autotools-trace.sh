#!/bin/sh
# spike-autotools-trace.sh — round-trip the B→A trace-driven
# autotools-to-Bazel conversion against two fixtures:
#
#   1. autotools-greet — single compile-and-link invocation
#      (`cc -O2 -o greet greet.c`). Validates the basic
#      cc_binary recovery path.
#   2. autotools-libapp — Makefile that compiles foo.c and
#      bar.c into .o files, archives them into libfoo.a, and
#      links myapp against -lfoo. Validates cross-event
#      correlation: archive → cc_library, link's -l<name> →
#      :<name> dep on the archived target.
#
# Each fixture stage:
#   - Stage a writable copy of the fixture's source tree.
#   - Run ./configure + make under `strace -f -e trace=execve`.
#   - Run convert-element-autotools against the trace; emit
#     BUILD.bazel.out.
#   - Assert the rendered output has the expected target shape.
#
# This is the spike-validation gate. It does NOT yet wire the
# converter into write-a's kind:autotools handler — that's the
# next slice. The spike proves the trace shape + parser are
# sufficient to recover sensible Bazel targets before we spend
# effort on the action-cache + write-a integration.

set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

bin="$work_dir/convert-element-autotools"
tracer="$work_dir/build-tracer"
cache_cli="$work_dir/trace-cache"
CGO_ENABLED=0 go build -o "$bin" ./cmd/convert-element-autotools
CGO_ENABLED=0 go build -o "$tracer" ./cmd/build-tracer
CGO_ENABLED=0 go build -o "$cache_cli" ./cmd/trace-cache

# Stand-in REAPI Action Cache root. Production swaps this for
# real RBE storage so distributed builders share traces.
cache_root="$work_dir/trace-cache-root"
mkdir -p "$cache_root"
tracer_version="strace-v1"

# run_fixture stages, traces, converts, asserts.
#
#   $1 — fixture name (testdata/meta-project/<name>/sources/)
#   $2 — assertion grep file (lines = required substrings)
#   $3 — optional imports.json (relative to repo root) passed
#        via --imports-manifest. Empty / absent = no manifest.
run_fixture() {
    name="$1"
    asserts_file="$2"
    imports="${3:-}"

    fixture_root="testdata/meta-project/$name/sources"
    if [ ! -d "$fixture_root" ]; then
        echo "spike-autotools-trace: missing fixture $fixture_root" >&2
        exit 1
    fi

    src="$work_dir/$name-src"
    cp -r "$fixture_root" "$src"
    chmod -R u+w "$src"

    trace="$work_dir/$name-trace.log"
    build_out="$work_dir/$name-BUILD.bazel.out"

    # Round 1: project B builds the element under build-tracer.
    # The trace artifact gets registered in the cache keyed by
    # the element's srckey + tracer_version. (For the spike we
    # use the fixture name as a stand-in srckey; production
    # passes the content-addressed @src_<key>// digest.)
    srckey="$name"

    if "$cache_cli" has --root="$cache_root" \
        --srckey="$srckey" --tracer-version="$tracer_version" >/dev/null; then
        # Round 2 (or later): cache hit. Skip the build; pull the
        # trace from the cache. This is the path where
        # convert-element-autotools renders native targets
        # without needing project B to re-run.
        "$cache_cli" lookup --root="$cache_root" \
            --srckey="$srckey" --tracer-version="$tracer_version" \
            --out="$trace"
        echo "spike-autotools-trace[$name]: cache hit; reused trace from $cache_root"
    else
        # Round 1: cache miss. Run the build under build-tracer
        # (today: strace shim; future: in-action ptrace). On
        # success, register the trace for future rounds.
        (cd "$src" && "$tracer" --out="$trace" -- \
            sh -c './configure --prefix=/usr >/dev/null 2>&1 && make >/dev/null 2>&1') \
            || {
            echo "spike-autotools-trace[$name]: build failed under tracer" >&2
            head -100 "$trace" >&2
            exit 1
        }
        "$cache_cli" register --root="$cache_root" \
            --srckey="$srckey" --tracer-version="$tracer_version" \
            --trace="$trace"
        echo "spike-autotools-trace[$name]: cache miss; ran tracer + registered trace"
    fi

    if [ -n "$imports" ]; then
        "$bin" --trace "$trace" --out-build "$build_out" --imports-manifest "$imports"
    else
        "$bin" --trace "$trace" --out-build "$build_out"
    fi

    while IFS= read -r marker; do
        [ -z "$marker" ] && continue
        if ! grep -qF "$marker" "$build_out"; then
            echo "spike-autotools-trace[$name]: rendered BUILD.bazel.out missing marker: $marker" >&2
            cat "$build_out" >&2
            exit 1
        fi
    done < "$asserts_file"

    echo "spike-autotools-trace[$name]: ok"
    echo "--- $name BUILD.bazel.out ---"
    cat "$build_out"
}

# autotools-greet asserts: single cc_binary, no cc_library
greet_asserts="$work_dir/greet-asserts.txt"
cat > "$greet_asserts" <<'EOF'
cc_binary(
name = "greet"
srcs = ["greet.c"]
copts = ["-O2"]
EOF
run_fixture autotools-greet "$greet_asserts"

# autotools-libapp asserts: cc_library {name=foo} + cc_binary
# {name=myapp, deps=[":foo", "//elements/zlib:zlib"]}. The :foo
# dep comes from in-trace correlation; the zlib dep comes from
# the imports manifest (myapp links -lz, but no archive
# producing libz.a appears in the trace).
libapp_asserts="$work_dir/libapp-asserts.txt"
cat > "$libapp_asserts" <<'EOF'
load("@rules_cc//cc:defs.bzl", "cc_binary", "cc_library")
cc_library(
name = "foo"
srcs = ["bar.c", "foo.c"]
linkstatic = True
cc_binary(
name = "myapp"
srcs = ["myapp.c"]
deps = ["//elements/zlib:zlib", ":foo"]
EOF
run_fixture autotools-libapp "$libapp_asserts" \
    "testdata/meta-project/autotools-libapp/imports.json"

# Round 2: re-run the same fixture. The cache is now populated;
# run_fixture should hit the cache instead of running build-tracer
# again. Asserts the round-2 BUILD.bazel.out is byte-identical to
# round 1 (the convergence guarantee — same trace, same converter
# version, same output).
echo
echo "=== round 2: re-run with populated cache ==="
round1_libapp="$work_dir/autotools-libapp-BUILD.bazel.out"
cp "$round1_libapp" "$work_dir/round1-libapp.txt"
run_fixture autotools-libapp "$libapp_asserts" \
    "testdata/meta-project/autotools-libapp/imports.json"
if ! cmp -s "$work_dir/round1-libapp.txt" "$round1_libapp"; then
    echo "spike-autotools-trace: round-2 BUILD.bazel.out diverged from round 1" >&2
    diff "$work_dir/round1-libapp.txt" "$round1_libapp" >&2
    exit 1
fi
echo "spike-autotools-trace: round 2 byte-identical to round 1 (convergence ok)"
