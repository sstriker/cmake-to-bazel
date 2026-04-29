# Meta-project spike testdata

Smallest viable end-to-end demonstration of the Bazel-as-orchestrator
shape described in `docs/whole-project-plan.md` (post-rewrite).

## Layout

```
testdata/meta-project/
  hello-world.bst             # the sole .bst input
  sources/hello-world/        # the element's source tree (cmake)
    CMakeLists.txt
    hello.c
    include/hello.h
```

The spike pipeline (added incrementally across follow-up commits):

1. A toy "writer-of-A" Go binary parses `hello-world.bst`, resolves
   the `kind: local` source, and renders project A:
   - `MODULE.bazel` declaring a generated repo for the element
   - `external/+_writer+hello-world/BUILD.bazel` containing one
     `genrule` that invokes `convert-element` plus typed
     `filegroup` exports.
2. Project A's `bazel build //...` runs `convert-element` against
   the source tree; outputs land at
   `bazel-bin/external/+_writer+hello-world/...`.
3. A wrapper materializes project A's outputs into project B's
   source tree (BUILD.bazel + cmake-config bundle as files,
   the original sources as symlinks).
4. Project B's `bazel build //...` builds the converted hello-world
   library against the host toolchain.

After the spike works end-to-end, two cache-stability assertions:

- **Scenario A** (`hello.c` content edit): project A cache hits
  on the convert-element action (the file is in the zero-stub
  set for that action — cmake configure walks the directory but
  doesn't open compilation sources). Project B's cc_library
  recompiles only the affected target.
- **Scenario A'** (CMakeLists.txt comment edit): project A
  cache-misses (CMakeLists is in the read set) but produces
  byte-identical output (cmake's parser strips comments before
  the codemodel). Project B sees no source delta and doesn't
  rebuild.

## Why this fixture

The element source tree is a verbatim copy of
`converter/testdata/sample-projects/hello-world/` — the smallest
cmake project that exercises the full convert-element pipeline
(cc_library + install + cmake-config export). Reusing it keeps
the spike's correctness anchored to existing fmt-fidelity-style
gates: convert-element's behavior on this source is already
covered by the converter's unit + e2e tests.

## Status

This directory is the *input* layer of the spike. Subsequent
commits add the writer-of-A binary, the `zero_files.bzl` rule,
and the wrapper that drives both Bazel passes.
