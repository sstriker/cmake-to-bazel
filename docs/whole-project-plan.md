# Whole-project plan: every BuildStream element in the Bazel build

Replaces the earlier FDSDK-only plan, which deferred the fine-vs-
coarse question pending fidelity evidence. This plan picks a
direction: keep fine-grained conversion for `kind: cmake` (where the
File API gives us enough structure), fall back to **per-element coarse-
grained builds** for everything else, integrated into the same
orchestrator pipeline. FDSDK remains the working target; nothing here
is FDSDK-specific.

## Goal

`bazel build //...` over a converted BuildStream project succeeds and
produces the same artifacts BuildStream would have, regardless of
element kind. Cmake elements ship as fine-grained `cc_library` /
`cc_binary` rules (status quo). Non-cmake elements ship as a single
opaque artifact per element, but still appear in Bazel's graph and
participate in cross-element dependency resolution.

## Element kinds and how they're handled

| Kind                      | Strategy                | Why                                                                           |
|---------------------------|-------------------------|-------------------------------------------------------------------------------|
| `cmake`                   | Fine-grained (existing) | File API gives us targets, sources, link/compile fragments, install layout.   |
| `manual`, `autotools`, `meson` | Coarse-grained per element | No File API equivalent. Build the whole element via BuildStream; capture the install delta as a sealed Bazel artifact. |
| `stack`                   | Meta-element, no build  | Just deps. Emit as an empty `filegroup` with `data = [<deps>]`.               |
| `filter`                  | Coarse-grained subset   | Filter parent's output. Emit a Bazel `filegroup` with the filter's globs.     |
| `junction`                | Out of scope (v1)       | References another BuildStream project. Could be a `local_repository` later.  |

The principle the user articulated: **coarse-grained is a fine
default**. Don't try to extract cc_library granularity from a `manual`
element when BuildStream is already going to build it monolithically.
Adding fine-grained support for additional kinds (e.g. autotools via
`./configure --info=...` introspection) is an additive future step,
not a v1 blocker.

## Architecture

The orchestrator already coordinates the cmake-only graph end-to-end
(see `docs/architecture.md`). Extending it: same `converted.json`
manifest schema, same per-element output directory layout, same
bzlmod extension at `bazel/converted_pkg_repo.bzl`. New parts:

1. **Element-kind dispatcher** in
   `orchestrator/internal/orchestrator/run.go:processElement`. After
   `element.BuildGraph()` we already filter to `kind: cmake`. Replace
   the filter with a dispatcher: cmake → existing
   `convertOne()`/`remoteExecute()`; coarse-grained kinds → new
   `coarseBuildOne()` (below); unsupported → Tier-1 failure with a
   clear message.
2. **Coarse-grained build path**, a new package
   `orchestrator/internal/coarse/` (sibling of
   `orchestrator/internal/orchestrator`). Drives BuildStream for one
   element, captures the install delta, packages it into the same
   per-element output directory shape the cmake path uses (same
   `manifest.json`, same `BUILD.bazel`, same `cmake-config/` bundle
   for cross-kind interop).
3. **Bazel extension** changes are surface-level: an enum field on
   each manifest entry (`kind: "cmake" | "coarse"`) and per-kind
   rule synthesis inside `_converted_pkg_repository`. Cmake entries
   keep their current shape; coarse entries get a
   `<element>` filegroup, a `headers` filegroup, and per-shared-lib
   `cc_import` targets.

## Coarse-grained build pipeline (the new work)

Per element, in order:

1. **Source resolution** — same `sourcecheckout` package the cmake
   path uses, no new code. BuildStream sources resolve to a local
   tree the same way regardless of element kind.

2. **Dep staging** — for each dep (cmake or coarse), stage its
   already-emitted output directory into a per-element prefix tree
   under `<work>/sysroot/`. This is the same idea as
   `orchestrator/internal/synthprefix` but with real artifacts:
   coarse outputs are concrete files that BuildStream's build needs
   (libs, headers, pkg-config files), not zero-byte stubs.

3. **BuildStream build** — invoke `bst build <element>` against a
   per-run BuildStream project that strips out the deps (so
   BuildStream doesn't try to rebuild them) and points sources at
   the resolved local tree. Capture `bst artifact checkout` output
   into `<work>/install/`.

   Shelling out to `bst` is acceptable here for the same reason it
   was acceptable in `sourcecheckout`'s `kind: remote-asset`
   handling: BuildStream is the source of truth for these element
   kinds and we don't want to reimplement its semantics.

4. **Install delta capture** — diff `<work>/install/` against the
   staged dep sysroot. The delta is what *this* element installed.
   Walk the delta, sha256 each file, and emit a `manifest.json`
   matching the cmake path's schema (same fields: `outputs`,
   `digests`, `failure_tier`, `kind: "coarse"`).

5. **Synthesize `cmake-config/` bundle** — for cross-kind interop,
   emit a `<Pkg>Config.cmake` + `<Pkg>Targets.cmake` from the install
   delta's lib/include layout. Heuristic: every `lib*.so` /
   `lib*.a` becomes an `add_library(... IMPORTED)` entry; every
   `include/<pkg>/` becomes the include directory. cmake elements
   that depend on a coarse element via `find_package(<Pkg>)` then
   resolve through the existing imports-manifest plumbing without
   special-casing the producer's kind.

   This is best-effort. Coarse elements can't always be cleanly
   exposed as cmake imported targets — meson elements with
   pkg-config files are easy, manual elements with hand-rolled
   layouts may not be. Tier-1 `coarse-export-incomplete` failure
   when we can't synthesize a usable bundle, with a hint to add a
   manual override file under `non_cmake_stubs/` (same escape hatch
   M1 used for libdrm's Glibc dep).

6. **Synthesize `BUILD.bazel`** — three rules per coarse element:
   - `filegroup(name = "<elem>", srcs = glob(["**"]))` — opaque
     full-tree artifact.
   - `filegroup(name = "headers", srcs = glob(["include/**"]))` —
     for cmake-element consumers via `cc_library(deps = ...)`.
   - `cc_import(name = "<libname>", static_library = "lib/lib<x>.a")`
     or `shared_library = "lib/lib<x>.so"` for each library found.

7. **Output ingestion** — same `internal/cas` path the cmake
   pipeline uses. Outputs are content-addressed; re-runs deterministic.

## Cross-kind dependencies

The two directions:

- **Coarse → cmake** (a `manual` element depends on a cmake one):
  the cmake element's output directory already contains the same
  layout BuildStream would have installed. Stage it into the
  coarse element's sysroot at step 2; BuildStream's build sees it
  as a normal prebuilt dep.

- **Cmake → coarse** (a cmake element depends on a `manual` one):
  the coarse element's synthesized `<Pkg>Config.cmake` (step 5
  above) is what the cmake element finds via `find_package()`.
  Same imports-manifest plumbing the cmake-cmake case uses; the
  cmake side doesn't know or care that the producer was coarse.

Bazel-side: cross-kind deps are just labels. A cmake element's
`cc_library(deps = ["@coarse_pkg//:<libname>"])` works because both
sides have BUILD.bazel files; rules_cc handles the import.

## Caching strategy

BuildStream and our orchestrator both have CAS layers; they need
to stop fighting each other.

- **Per-element output cache**: orchestrator's `internal/cas`,
  unchanged. The output of *both* cmake and coarse builds lands
  here keyed by content. Re-runs short-circuit identically.
- **BuildStream's own cache**: configured to share the same
  Buildbarn instance the orchestrator's `--execute` flag points
  at. So `bst build`'s artifact CAS *is* our REAPI CAS. When
  we re-run `bst build <element>` and the element's inputs
  haven't changed, BuildStream gets a cache hit from the same
  CAS our cmake conversions populated.

  Concretely: BuildStream supports a `cache:` block in
  `project.conf` pointing at a remote CAS. The orchestrator
  writes that block to a per-run project overlay during step 3
  above.

This is the single biggest payoff: one CAS, two build systems,
shared blobs.

## Bootstrap and sequencing

Cmake elements often depend on `manual` toolchains (e.g. a base
glibc, a python interpreter for build scripts). The graph
naturally puts them upstream of the cmake elements that need
them — BuildStream already encodes the topology; we just respect
it.

A complication: the orchestrator's first-pass converter probe runs
cmake to populate the File API, and that probe needs the
toolchain on PATH. If the toolchain is itself a coarse element we
haven't built yet, we have a chicken-and-egg.

Resolution: process the topo order without filtering by kind, but
with the dispatcher branching per-element. Coarse elements early
in the topo order build first via BuildStream; their outputs land
in the per-element CAS; the next cmake element's probe step sees
them already staged.

The existing `--toolchain-cmake-file` path (M1's `derive-toolchain`)
continues to work: derive once against a known-good base sysroot,
reuse for every cmake element.

## Failure handling

Tier-1 failures (per `docs/failure-schema.md`) extended with new
codes:

- `bst-build-failed` — `bst build <element>` exited non-zero.
  Subprocess output captured into `failure.json`.
- `coarse-export-incomplete` — synthesized cmake-config bundle
  doesn't cover all targets a downstream cmake element will look
  up. Hint to operator: add an override.
- `unsupported-element-kind` — `kind: junction` in v1, or any
  newly-introduced BuildStream kind we don't recognize.

Tier-2/3 (orchestrator-aborting) failures continue to be reserved
for orchestrator-internal bugs, not element-specific issues.

## Phasing

Five increments, each landable as its own PR:

**Phase 1 — Element-kind dispatch (no new build path yet).**
Replace `FilterByKind("cmake")` with a dispatcher that routes
cmake to the existing path and emits a `unsupported-element-kind`
Tier-1 failure for everything else. New unit tests against the
fdsdk-subset fixture extended with one `manual` element. This
PR doesn't add new functionality but reshapes the orchestrator
to make subsequent PRs additive.

**Phase 2 — Coarse-grained build path, no cross-kind interop.**
New `orchestrator/internal/coarse/` package. Implements steps 1–4
+ 6–7 above (skip step 5: `cmake-config/` bundle synthesis). Per-
element `BUILD.bazel` exposes the artifact as a flat filegroup; no
`find_package()` resolution from cmake elements yet. Acceptance:
a fixture project with one `manual` element and one cmake element
that *doesn't* depend on it both convert and `bazel build //...`
succeeds.

**Phase 3 — Cross-kind cmake-config synthesis.**
Step 5 of the pipeline. Heuristic-driven; ships with a
`non_cmake_stubs/` override directory for cases the heuristic
can't handle. Acceptance: a fixture where cmake element B depends
on manual element A's library, both convert, both build under
Bazel.

**Phase 4 — Shared CAS with BuildStream.**
Wire the orchestrator's per-run BuildStream project overlay to
configure `cache:` pointing at the same Buildbarn instance the
`--execute` flag uses. Acceptance: `bst build <element>` against
a populated CAS gets a cache hit on the second run; orchestrator
metrics surface BuildStream's cache-hit ratio alongside its own.

**Phase 5 — FDSDK acceptance.**
Run the full pipeline over an FDSDK subset that includes
non-cmake elements (a meson element + a manual toolchain element
+ the existing cmake elements). `bazel build //...` succeeds
end-to-end. Document any FDSDK-specific deltas in
`docs/fidelity-known-deltas.md`.

## Out of scope (explicitly)

- **`kind: junction`** (BuildStream-side cross-project references).
  Plausibly a `local_repository` per junction, but no FDSDK use
  case forces it for v1.
- **Reverse-engineering autotools builds** for fine-grained
  conversion. The right pivot is "use coarse, augment specific
  elements when fidelity demands it" — we don't pre-emptively
  build a parallel converter.
- **Replacing BuildStream entirely.** The point of this plan is
  not to eliminate BuildStream from the FDSDK build; it's to
  make Bazel a viable consumer of BuildStream-built artifacts
  alongside the converter's output.
- **Cross-distro generalization.** The plan stays generic about
  element kinds but doesn't try to be a generic "any Linux distro
  → Bazel" converter. FDSDK is the validator.

## Open questions

1. **Install-delta capture mechanism.** Walk-and-diff is simple but
   slow on large sysroots. Alternative: bind-mount the dep sysroot
   read-only and let BuildStream write into a fresh upper layer
   (overlayfs); the upper layer *is* the delta. Decide during
   Phase 2 implementation when we have a concrete sysroot size.

2. **Header collision between coarse elements.** Two `manual`
   elements both installing `include/foo.h`. BuildStream's own
   build catches this; what does Bazel do when both are deps of a
   downstream cmake element? Best guess: a Tier-1 failure on
   conversion if it's detected, defer to Bazel's normal
   diamond-dep resolution otherwise. Revisit when we hit it.

3. **Reproducibility of `bst build`.** BuildStream guarantees
   determinism in principle but in practice depends on every
   element's build script being well-behaved. We'll see real
   numbers once Phase 5 runs over FDSDK; if a meaningful fraction
   of coarse elements aren't bit-identical across runs, that
   becomes a fidelity-tier conversation, not a bug in this plan.

4. **Worker image scope creep.** The current
   `deploy/buildbarn/runner/Dockerfile` ships cmake/ninja/bwrap.
   `bst build` needs Python, `bst` itself, and whatever toolchains
   the project's elements need. Layering all of FDSDK's
   bootstrap into one image is the wrong shape; per-project
   worker images via Bazel platforms is probably right. Defer
   until Phase 4 forces the question.
