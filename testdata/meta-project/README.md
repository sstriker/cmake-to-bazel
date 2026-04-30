# Meta-project test fixtures

End-to-end fixtures for the Bazel-as-orchestrator shape described
in `docs/whole-project-plan.md`. Three fixtures so far:

- **`hello-world.bst`** + **`sources/hello-world/`** — single cmake
  element. Phase 1 acceptance gate (`make e2e-meta-hello`).
- **`two-libs/`** — multi-element graph: two `kind: cmake`
  elements (`lib-a.bst`, `lib-b.bst`) plus one `kind: stack`
  (`runtime.bst`) bundling them. Phase 2 acceptance gate
  (`make e2e-meta-stack`).
- **`manual-greet/`** — single `kind: manual` element with a
  trivial install pipeline. Phase 3 acceptance gate
  (`make e2e-meta-manual`).

## hello-world fixture

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

### Why this fixture

The element source tree is a verbatim copy of
`converter/testdata/sample-projects/hello-world/` — the smallest
cmake project that exercises the full convert-element pipeline
(cc_library + install + cmake-config export). Reusing it keeps
the gate's correctness anchored to existing fixture coverage.

## two-libs fixture (Phase 2)

```
testdata/meta-project/two-libs/
  lib-a.bst                # kind:cmake, kind:local source under sources/lib-a/
  lib-b.bst                # kind:cmake, kind:local source under sources/lib-b/
  runtime.bst              # kind:stack, depends on [lib-a, lib-b]
  sources/
    lib-a/{CMakeLists.txt, lib-a.c, include/lib-a.h}
    lib-b/{CMakeLists.txt, lib-b.c, include/lib-b.h}
```

Each lib defines `lib_<x>_message()` returning `"lib-<x> says hi"`
and declares `add_library(lib-<x>)` so the converter emits
`cc_library(name="lib-<x>")` in project A — matching the Phase 2
convention where `kind: stack` references its deps as
`//elements/<dep>:<dep>`.

`scripts/meta-stack.sh` drives the pipeline:

1. `cmd/write-a` parses all three .bst files, builds the dep DAG
   (lib-a → mid, lib-b → mid, runtime → both), renders project A
   (per-element genrules for the two cmake elements + a no-target
   marker package for `runtime`) and project B (cc_library
   placeholders + the runtime filegroup composing dep labels).
2. `bazel build` in project A runs convert-element on each cmake
   element.
3. The driver stages each cmake element's `BUILD.bazel.out` into
   project B's `elements/<name>/BUILD.bazel`.
4. `bazel build //elements/runtime:runtime` in project B validates
   the stack's filegroup resolves all dep labels (the multi-element
   graph composes correctly).
5. The driver writes a smoke target (`cc_binary` depending on both
   `lib-a` and `lib-b`) and `bazel run`s it; output must contain
   both libs' messages.

## manual-greet fixture (Phase 3)

```
testdata/meta-project/manual-greet/
  greet.bst                   # kind:manual + kind:local source
  sources/greeting.txt        # "Hello from kind:manual!"
```

Smallest viable kind:manual fixture: an element whose only
phase-command list is `install-commands`, copying the staged
`greeting.txt` to `%{install-root}%{prefix}/share/greeting.txt`.
Exercises both variable substitutions Phase 3's manual handler
supports (`%{install-root}` / `%{prefix}`) and the install-tree-
tarball output shape.

`scripts/meta-manual.sh` drives the pipeline:

1. `cmd/write-a` parses `greet.bst`, renders project A (a per-
   element genrule that runs the install-commands and tars
   `$INSTALL_ROOT` as `install_tree.tar`) and project B (a
   placeholder package; the typed-filegroup wrapper for
   downstream consumers lands in a follow-up).
2. `bazel build //elements/greet:greet_install` in project A.
3. The driver extracts `bazel-bin/elements/greet/install_tree.tar`
   and asserts:
   - `usr/share/greeting.txt` exists.
   - Its content is `"Hello from kind:manual!"`.
