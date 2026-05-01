#!/usr/bin/env bash
# bst-source-push: thin wrapper around `bst source push` for FDSDK
# graphs. Usage:
#
#   tools/bst-source-push.sh <bst-project-dir> [element1.bst element2.bst ...]
#
# Requires BuildStream installed on the host. Detection order:
#
#   1. `bst` on PATH (any source: distro package, pip --user, brew, …).
#   2. A pre-existing venv at ~/.cache/cmake-to-bazel/bst-venv/.
#
# When neither is present, prints an install hint and exits non-zero.
# The companion `make bst-venv` target builds the cached venv from
# scratch (pip install BuildStream into a hermetic location).
#
# project.conf must declare a source-caches entry pointing at the
# buildbarn deployment (or whichever REAPI cache the user runs).
# See https://docs.buildstream.build/master/using_configuring_cache_server.html.
#
# This is the production path. cmd/source-push (the BuildStream-free
# Go uploader) covers the test/dev case where a populated
# --source-cache directory exists and BuildStream isn't installed.
set -euo pipefail

if [[ $# -lt 1 ]]; then
    cat >&2 <<EOF
usage: $0 <bst-project-dir> [element1.bst ...]

Runs `bst source push --deps all` against the given elements (or
all elements when none specified). Requires BuildStream on PATH or
in the project's cached venv.
EOF
    exit 2
fi

project_dir="$1"
shift

if [[ ! -f "$project_dir/project.conf" ]]; then
    echo "error: $project_dir does not look like a BuildStream project (no project.conf)" >&2
    exit 1
fi

# Find bst.
BST=""
if command -v bst >/dev/null 2>&1; then
    BST="$(command -v bst)"
elif [[ -x "$HOME/.cache/cmake-to-bazel/bst-venv/bin/bst" ]]; then
    BST="$HOME/.cache/cmake-to-bazel/bst-venv/bin/bst"
fi

if [[ -z "$BST" ]]; then
    cat >&2 <<EOF
error: BuildStream (\`bst\`) not found. Install via one of:

  # Distro:
  apt install buildstream                 # Debian / Ubuntu
  dnf install buildstream                 # Fedora

  # Or via the in-tree venv (run once, hermetic):
  make bst-venv

After install, re-run this script.
EOF
    exit 1
fi

cd "$project_dir"

if [[ $# -eq 0 ]]; then
    # No elements specified — push everything in the project.
    # Use --deps all so transitive sources land in the cache.
    "$BST" source push --deps all
else
    "$BST" source push --deps all "$@"
fi
