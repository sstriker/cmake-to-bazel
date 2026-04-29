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

bzl_cache="$spike_dir/.bazel"
sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# WORKSPACE.bazel keeps the spike compatible with bazel < 6 (no
# bzlmod). Newer bazel versions read it directly without the
# --enable_bzlmod flag (which bazel 4 doesn't recognize).
run_bazel() {
    (cd "$spike_dir" && "$BZL" --output_user_root="$bzl_cache" "$@")
}

run_bazel build //elements/hello-world:hello-world_converted 2>&1 | tail -20

build_out="$spike_dir/bazel-bin/elements/hello-world/BUILD.bazel.out"
if [ ! -f "$build_out" ]; then
    echo "spike-hello: BUILD.bazel.out not produced" >&2
    exit 1
fi
if ! grep -q '^cc_library' "$build_out"; then
    echo "spike-hello: BUILD.bazel.out missing cc_library output" >&2
    head -20 "$build_out" >&2
    exit 1
fi
sha_run1=$(sha_of "$build_out")
echo "spike-hello: render OK; first build sha=$sha_run1"

# === Cache-stability scenarios A and A' ===
# Re-run write-a-spike with the previous build's read_paths.json as
# feedback so the source tree gets narrowed to its real read set
# plus auto-included CMakeLists.txt's. Then exercise:
#   - Scenario A:  edit hello.c (NOT in the read set) — convert-element
#                  must cache-hit, BUILD.bazel.out byte-identical.
#   - Scenario A': add a comment to CMakeLists.txt (IS in the read
#                  set) — convert-element re-runs but produces a
#                  byte-identical BUILD.bazel.out.
feedback="$spike_dir/feedback-read-paths.json"
cp "$spike_dir/bazel-bin/elements/hello-world/read_paths.json" "$feedback"

# Stage a writable copy of the source tree so we can edit it.
edit_src="$spike_dir/edit-src"
cp -r testdata/meta-project/sources/hello-world/. "$edit_src"
# Point the .bst at the editable tree.
edit_bst="$spike_dir/hello.bst"
cat > "$edit_bst" <<EOF
kind: cmake
sources:
- kind: local
  path: $edit_src
EOF

# Re-render project A in narrowed mode (feedback set).
rm -rf "$spike_dir"/elements "$spike_dir"/MODULE.bazel "$spike_dir"/WORKSPACE.bazel \
       "$spike_dir"/BUILD.bazel "$spike_dir"/rules "$spike_dir"/tools
"$bin_dir/write-a-spike" \
    --bst "$edit_bst" \
    --out "$spike_dir" \
    --convert-element "$bin_dir/convert-element" \
    --read-paths-feedback "$feedback"
run_bazel build //elements/hello:hello_converted 2>&1 | tail -3
narrow_out="$spike_dir/bazel-bin/elements/hello/BUILD.bazel.out"
sha_run2=$(sha_of "$narrow_out")
if [ "$sha_run1" != "$sha_run2" ]; then
    echo "spike-hello: BUILD.bazel.out sha shifted after narrowing transition" >&2
    echo "  before narrowing: $sha_run1" >&2
    echo "  after  narrowing: $sha_run2" >&2
    exit 1
fi
echo "spike-hello: narrowed-mode build sha matches first run"

# Scenario A: edit a zero-stubbed file.
echo "// scenario-A test edit" >> "$edit_src/hello.c"
"$bin_dir/write-a-spike" \
    --bst "$edit_bst" --out "$spike_dir" \
    --convert-element "$bin_dir/convert-element" \
    --read-paths-feedback "$feedback" >/dev/null
scen_a_log=$(run_bazel build //elements/hello:hello_converted 2>&1 | tail -3)
sha_scen_a=$(sha_of "$narrow_out")
if [ "$sha_scen_a" != "$sha_run2" ]; then
    echo "spike-hello: Scenario A FAILED — BUILD.bazel.out sha shifted after hello.c edit" >&2
    exit 1
fi
echo "spike-hello: Scenario A — hello.c edit, BUILD.bazel.out byte-identical"
# Bazel's "X processes" line on a cache-only run reports just
# internals; finding "1 process: 1 internal" confirms no
# action ran. Soft-check (string match) — different bazel versions
# format slightly differently.
if echo "$scen_a_log" | grep -q '1 process: 1 internal'; then
    echo "spike-hello: Scenario A — convert-element cache-hit (no action ran)"
fi

# Scenario A': edit CMakeLists.txt (in the read set).
echo "# scenario-A' comment $(date +%s)" >> "$edit_src/CMakeLists.txt"
"$bin_dir/write-a-spike" \
    --bst "$edit_bst" --out "$spike_dir" \
    --convert-element "$bin_dir/convert-element" \
    --read-paths-feedback "$feedback" >/dev/null
run_bazel build //elements/hello:hello_converted 2>&1 | tail -3
sha_scen_aprime=$(sha_of "$narrow_out")
if [ "$sha_scen_aprime" != "$sha_run2" ]; then
    echo "spike-hello: Scenario A' FAILED — BUILD.bazel.out sha shifted after comment edit" >&2
    echo "  expected: $sha_run2" >&2
    echo "  got:      $sha_scen_aprime" >&2
    exit 1
fi
echo "spike-hello: Scenario A' — CMakeLists.txt comment edit, BUILD.bazel.out byte-identical"

echo "spike-hello: ok"
