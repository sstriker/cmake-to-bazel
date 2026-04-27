#!/usr/bin/env bash
# Wraps `bazel query` for the audit shapes documented in docs/codegen-tags.md.
#
# Usage: tools/audit/list-codegen.sh [--workspace <DIR>] <command> [args...]
#
# Commands:
#   all                        list every recovered codegen rule
#   driver <name>              codegen rules driven by <name> (e.g. python3)
#   consumers                  cc rules tagged has-cmake-codegen
#   cmake-e                    codegen rules translated to native Bazel idiom
#   for-target <label>         the codegen rules a given consumer pulls in
#   summary                    counts grouped by driver
#
# Each command is a thin shell over a known-good query expression so changes
# stay reviewable. New commands should also be appended (not renamed) to
# preserve operator muscle memory; the help text doubles as the deprecation
# log.

set -euo pipefail

WORKSPACE=""

usage() {
    sed -n '2,/^$/{ /^# /{ s/^# \?//; p; }; }' "$0"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --workspace) WORKSPACE="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) break ;;
    esac
done

if [[ $# -eq 0 ]]; then
    usage
    exit 64
fi

command -v bazel >/dev/null || {
    echo "list-codegen.sh: bazel not on PATH" >&2
    exit 70
}

bq() {
    local args=()
    [[ -n "$WORKSPACE" ]] && args+=(--bazelrc=/dev/null --output_user_root="${TMPDIR:-/tmp}/bazel-codegen-audit")
    if [[ -n "$WORKSPACE" ]]; then
        (cd "$WORKSPACE" && bazel query "${args[@]}" "$@")
    else
        bazel query "$@"
    fi
}

cmd="$1"; shift
case "$cmd" in
    all)
        bq 'attr("tags", "cmake-codegen", //...)'
        ;;
    driver)
        if [[ $# -lt 1 ]]; then
            echo "usage: list-codegen.sh driver <name>" >&2
            exit 64
        fi
        bq "attr(\"tags\", \"cmake-codegen-driver=$1\", //...)"
        ;;
    consumers)
        bq 'attr("tags", "has-cmake-codegen", //...)'
        ;;
    cmake-e)
        bq 'attr("tags", "cmake-codegen-cmake-e", //...)'
        ;;
    for-target)
        if [[ $# -lt 1 ]]; then
            echo "usage: list-codegen.sh for-target <label>" >&2
            exit 64
        fi
        # Codegen rules in the deps closure of <label>.
        bq "attr(\"tags\", \"cmake-codegen\", deps($1))"
        ;;
    summary)
        # Group counts by driver. We pull labels and parse the driver from
        # `bazel query --output=label_kind` plus a tags follow-up — keeps
        # the script self-contained without bazel's `output=jsonproto`.
        all=$(bq 'attr("tags", "cmake-codegen", //...)')
        if [[ -z "$all" ]]; then
            echo "no codegen rules"
            exit 0
        fi
        printf '%s\n' "$all" | while read -r label; do
            tags=$(bq "labels(\"tags\", $label)" 2>/dev/null || true)
            driver=$(printf '%s\n' "$tags" | grep -E '^cmake-codegen-driver=' | head -1 | sed 's/^cmake-codegen-driver=//')
            printf '%s\t%s\n' "${driver:-unknown}" "$label"
        done | sort | awk -F'\t' '{ count[$1]++ } END { for (k in count) printf "%-24s %d\n", k, count[k] }' | sort
        ;;
    *)
        echo "list-codegen.sh: unknown command $cmd" >&2
        usage
        exit 64
        ;;
esac
