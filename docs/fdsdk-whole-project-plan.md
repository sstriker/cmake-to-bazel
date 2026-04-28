# FreeDesktop SDK whole-project Bazel build

## Goal

`bazel build //...` over a converted FDSDK project succeeds and produces
the same set of artifacts BuildStream would have, modulo the documented
fidelity deltas.

## What's converted

`kind: cmake` elements via `convert-element` (M1–M5b). Each one becomes
a `local_repository` declared by the `converted_pkg_repo` Bzlmod
extension off the orchestrator's `manifest/converted.json`. Cross-element
deps resolve via synthesized `<Pkg>Config.cmake` bundles + the imports
manifest. This is done.

## What's not (yet)

Every other element kind. FDSDK's actual graph mixes `kind: cmake` with
`kind: manual / autotools / meson / …` — the non-cmake kinds need to
appear in the Bazel build graph somehow, but **without** translating
their build systems to Starlark. Three options on the table; the
decision criterion is "pick after fmt fidelity is green and the PR
stack has merged" (avoid bikeshedding ahead of evidence).

### Option A: BuildStream pre-builds; Bazel wraps the artifact

BuildStream builds each non-cmake element to its artifact-cache
directory. Orchestrator emits a `local_repository` per such element
pointing at that directory; the synthesized `BUILD.bazel` exposes the
installed prefix as a `filegroup` + `cc_import` set so cmake-element
consumers' `find_package` (resolved via converted Config bundles) sees
the same files cmake would have.

- Pros: zero new build infrastructure on the Bazel side. Re-uses
  whatever the BuildStream project already declares.
- Cons: artifact-cache directory layout is BuildStream-version-coupled.
  Operators have to run `bst build <element>` ahead of time as a
  separate phase; the dependency graph between cmake and non-cmake
  elements isn't visible to Bazel's scheduler.

### Option B: Bazel `genrule` shells out to `bst build`

Each non-cmake element gets a synthesized `BUILD.bazel` whose primary
target is a `genrule` with `cmd = "bst build $(name) && bst artifact
checkout ..."`. The orchestrator emits these the same way it emits
cmake-element BUILD.bazel files; the difference is just rule kind.

- Pros: single Bazel `bazel build //...` runs the entire graph,
  cmake + non-cmake mixed. Cross-element deps via `srcs`/`outs`
  thread through Bazel's scheduler naturally.
- Cons: every non-cmake build is a Bazel cache miss until BuildStream
  itself caches; stacking BuildStream's internal cache under Bazel's
  is a layering anti-pattern. `bst build` inside a `genrule` sandbox
  needs careful handling.

### Option C: Operator stages outputs at known prefix paths

Operator runs BuildStream out-of-band, stages artifacts at known prefix
paths (`/staging/<element>/{lib,include,bin}`). cmake elements'
synthesized `BUILD.bazel` references those paths via `cc_import` +
`filegroup` directly; the orchestrator produces a `MODULE.bazel`
declaring each as a `local_path_override`.

- Pros: clean separation. Bazel sees exactly what cmake's
  `find_package` saw at conversion time.
- Cons: no Bazel-driven invalidation when a non-cmake element changes.
  Two-build-system divergence is on the operator.

### Decision criterion

Pick after Track 5 (fmt fidelity) is green. By then we'll have:
- Real fidelity numbers on a non-trivial cmake project (fmt).
- A scale fixture exercising the orchestrator's concurrency layer.
- A custom Buildbarn worker image proving the worker-side execution path.

That's enough evidence to know whether the friction lives in the
orchestrator (push for B), the cache layer (push for A), or
operator-facing tooling (push for C).

## Out of scope

- Cross-distro generalization (this plan, like every other plan in
  this repo, is FDSDK-only).
- Generic "any BuildStream project → Bazel" generality. The decisions
  above pivot on FDSDK's specific element kinds and dep shapes.
- Non-Bazel downstream consumers of FDSDK's converted output. M5's
  `find_package` plumbing already covers that case.
