# Meta-project hello-world testdata

Smallest viable end-to-end fixture for the Bazel-as-orchestrator
shape described in `docs/whole-project-plan.md`. Drives Phase 1's
acceptance gate (`make e2e-meta-hello`).

## Layout

```
testdata/meta-project/
  hello-world.bst             # the sole .bst input
  sources/hello-world/        # the element's source tree (cmake)
    CMakeLists.txt
    hello.c
    include/hello.h
```

## Pipeline (driven by `scripts/meta-hello.sh`)

1. `cmd/write-a` parses `hello-world.bst`, resolves the `kind: local`
   source, and renders **two** workspaces:
   - **Project A** (the meta workspace): `MODULE.bazel`,
     `tools/convert-element` (staged binary), `rules/zero_files.bzl`,
     and `elements/hello-world/BUILD.bazel` containing one `genrule`
     that invokes `convert-element` plus a `zero_files` target for
     any source paths the converter doesn't read.
   - **Project B** (the consumer workspace): `MODULE.bazel`
     (`bazel_dep` on `rules_cc`), the user's source tree under
     `elements/hello-world/`, and a `BUILD_NOT_YET_STAGED`
     placeholder waiting for project A's converted output.
2. `bazel build` in project A runs the genrule; convert-element
   emits `BUILD.bazel.out` (a `cc_library` declaration) plus
   `cmake-config-bundle.tar` and `read_paths.json`.
3. The driver overwrites project B's per-element `BUILD.bazel`
   with project A's `BUILD.bazel.out`. The placeholder is gone;
   project B's element package is now well-formed.
4. The driver writes a hand-authored smoke target into
   `project-B/smoke/` (`cc_binary` linking against the converted
   `cc_library`).
5. `bazel build` + `bazel run` in project B compiles + executes the
   smoke binary; the gate asserts the output contains
   "Hello, World!".

After the round-trip succeeds, two cache-stability assertions
through both projects:

- **Scenario A** (`hello.c` content edit): convert-element cache-hits
  in project A (zero-stub-backed input merkle is content-stable
  across edits to non-read source files); project B's smoke binary
  still prints "Hello, World!".
- **Scenario B** (CMakeLists.txt comment edit): convert-element
  re-runs in project A (CMakeLists is real) but produces a
  byte-identical `BUILD.bazel.out` (cmake's parser strips comments
  before the codemodel); project B's smoke binary sha is unchanged
  (no rebuild).

## Why this fixture

The element source tree is a verbatim copy of
`converter/testdata/sample-projects/hello-world/` — the smallest
cmake project that exercises the full convert-element pipeline
(cc_library + install + cmake-config export). Reusing it keeps
the gate's correctness anchored to existing fixture coverage.
