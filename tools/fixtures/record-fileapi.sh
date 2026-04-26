#!/usr/bin/env bash
# Regenerate testdata/fileapi/<name>/ from testdata/sample-projects/<name>/.
#
# For each checked-in sample project, runs cmake with File API queries enabled
# and copies the reply directory into testdata for unit-test consumption.
#
# Idempotent: deletes the existing reply dir before regenerating.
#
# Usage: tools/fixtures/record-fileapi.sh [project-name ...]
#        With no args, regenerates every sample project.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SAMPLES_DIR="$REPO_ROOT/converter/testdata/sample-projects"
FIXTURES_DIR="$REPO_ROOT/converter/testdata/fileapi"

record_one() {
    local name="$1"
    local src="$SAMPLES_DIR/$name"
    local out="$FIXTURES_DIR/$name"

    if [[ ! -d "$src" ]]; then
        echo "no such sample project: $src" >&2
        return 1
    fi

    echo "==> recording fileapi for $name"
    local build
    build="$(mktemp -d)"

    # Request all four API kinds we consume.
    mkdir -p "$build/.cmake/api/v1/query"
    : >"$build/.cmake/api/v1/query/codemodel-v2"
    : >"$build/.cmake/api/v1/query/toolchains-v1"
    : >"$build/.cmake/api/v1/query/cmakeFiles-v1"
    : >"$build/.cmake/api/v1/query/cache-v2"

    cmake -S "$src" -B "$build" -G Ninja \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_EXPORT_COMPILE_COMMANDS=ON \
        >"$build/cmake.stdout" 2>"$build/cmake.stderr"

    rm -rf "$out"
    mkdir -p "$out"
    cp -R "$build/.cmake/api/v1/reply/." "$out/"

    # Also stash build.ninja for genrule recovery tests in M2; harmless in M1.
    cp "$build/build.ninja" "$out/build.ninja"

    echo "    -> $(find "$out" -type f | wc -l) files in $out"
    rm -rf "$build"
}

main() {
    if [[ $# -eq 0 ]]; then
        for d in "$SAMPLES_DIR"/*/; do
            record_one "$(basename "$d")"
        done
    else
        for name in "$@"; do
            record_one "$name"
        done
    fi
}

main "$@"
