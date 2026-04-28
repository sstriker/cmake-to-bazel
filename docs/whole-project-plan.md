# Whole-project plan: per-kind BuildStream-element translators

Replaces `docs/fdsdk-whole-project-plan.md` and the previous coarse-
fallback-via-bst draft of this file. The earlier drafts both leaned
on BuildStream itself doing the work for non-cmake elements
(`bst build` from a Bazel genrule, or pre-staged BuildStream
artifacts wrapped as Bazel imports). Both kept BuildStream in the
build path. The right shape is **per-bst-kind translators that emit
native Bazel rules**, with no `bst` invocation anywhere downstream.
Coarse-grained where the kind doesn't surface enough structure;
fine-grained where it does.

## Goal

`bazel build //...` over a converted BuildStream project succeeds and
produces the same artifacts BuildStream would have. Every element
kind has a translator that lowers it to BUILD.bazel rules. No
runtime dependency on `bst` for the Bazel build.

## Why per-kind translators

BuildStream itself is a thin orchestrator over per-kind plugins
(`cmake`, `autotools`, `meson`, `manual`, `filter`, `stack`, …).
Each plugin knows how to interpret its element's `config:` block and
turn it into a build invocation. We already wrote one such
translator — for `kind: cmake`. The plan is to write the rest, in
the same pluggable shape.

## Phase 0: survey FDSDK

Before committing to per-kind shapes, count what's actually in the
project. Spot-checks against `gitlab.com/freedesktop-sdk/freedesktop-sdk@master`
already show `kind: stack`, `kind: filter`, `kind: autotools`, and
`kind: cmake` are common; `kind: meson` shows up in the GNOME stack;
`kind: manual` is the long tail. Concrete numbers and per-kind
config-shape diversity drive the prioritization in Phase 2.

Output of Phase 0: a checked-in survey under
`docs/fdsdk-element-survey.md` containing:

1. Total element count and per-kind breakdown.
2. Per-kind sample of 5–10 elements (small/medium/large) with the
   `config:` keys actually used. Used to decide what shape the
   translator's input has.
3. Per-kind list of features used in the wild that the BuildStream
   plugin supports but our translator may want to defer
   (e.g. `command-subdir`, `prefix`, custom phases).
4. Source-kind breakdown (`tar` / `git` / `local` / `patch` /
   `remote`) — drives what `sourcecheckout` needs to handle.

The survey is a tool, not a deliverable in itself; once Phase 2
starts we revisit it as new translators land.

## Per-kind translation strategies

Strategies below are the planning-time best guess. Phase 0's survey
will refine numbers; the strategies themselves should hold.

### `kind: cmake`

**Status**: done (existing converter pipeline). Fine-grained
`cc_library` / `cc_binary` rules from cmake's File API.

### `kind: meson`

**Granularity**: fine-grained.

Meson supports `meson introspect --targets --installed --buildoptions`,
which emits JSON close enough in shape to cmake's File API to drive
the same lowering pattern. The translator runs:

1. `meson setup build/` against the resolved source tree (in a
   bwrap sandbox, same as the cmake path).
2. `meson introspect build/ --all` for targets / sources / link deps.
3. Lower the introspection JSON to the existing
   `converter/internal/ir` types (Package, Target, Source, …).
4. Reuse `converter/internal/emit/bazel/emit.go` to emit BUILD.bazel.

Most of the converter's lowering code (`converter/internal/lower/`)
is File-API-shaped today; the work is "introduce a meson-shaped
input parser, fold to the same IR, reuse the rest." Probably 30–40%
new code, 60–70% reuse.

### `kind: autotools`

**Granularity**: coarse-grained for v1, with a fine-grained path
identified for later.

Autotools doesn't expose introspection. The build invocation is the
classic `configure → make → make install` pipeline; `make`'s
internal target graph isn't easily extracted (Makefile parsing is
much harder than ninja parsing).

For v1, emit a single `genrule` per element wrapping the
plugin's standard pipeline, with element-specific overrides folded
in from the .bst's `config:` block:

```bzl
genrule(
    name = "_install_tree",
    srcs = glob(["src/**"]),
    outs = ["install.tar"],
    cmd = """
        cd $$(dirname $(execpath src/configure))
        ./configure --prefix=/usr <element-specific flags>
        make -j$$(nproc)
        make DESTDIR=$$PWD/_install install
        tar -cf $@ -C _install .
    """,
    tools = ["@cc_toolchain//:all"],
)
```

`install.tar` is the coarse output. `cc_import` / `filegroup` rules
sit on top to expose individual libs/headers in the same shape the
fine-grained cmake translator emits, so consumers don't see the
granularity difference.

Fine-grained path (deferred to a later phase): autotools generates
a Makefile we *could* parse for source/object dependencies, but the
ROI is unclear. Many autotools elements are small enough that the
coarse genrule rebuilds in seconds. Revisit when a specific
autotools element becomes a build-time bottleneck.

### `kind: manual`

**Granularity**: coarse-grained, no realistic fine path.

The element's `config:` block has freeform `commands:` lists for
each phase (`configure`, `build`, `install`, `strip`). Translation
is mechanical: emit a single `genrule` whose `cmd` is the
concatenation of those command lists, joined with `&&` and with
BuildStream's variable substitution (`%{prefix}`, `%{install-root}`)
mapped to Bazel-side equivalents.

Where this gets hard: BuildStream's variable substitution is
recursive and project-defined, and `manual` elements lean on it
heavily. The translator carries a small substitution engine that
mirrors the BuildStream-defined-variables semantics enough to
resolve everything an element-config-time substitution can produce.

### `kind: stack`

**Granularity**: trivial.

Stack elements have no build, just dependencies. Translation:

```bzl
filegroup(
    name = "<element>",
    srcs = [],
    data = ["@<dep1>//:all", "@<dep2>//:all", ...],
)
```

Bzlmod extension generates the per-stack repo containing this one
filegroup. Done.

### `kind: filter`

**Granularity**: structural (not really a build).

Filter elements take a parent element's output and split it by
glob patterns. Translation: emit one `filegroup` per filter slice
(e.g. `runtime`, `devel`, `static`) selecting from the parent's
`@<parent>//:all` filegroup via include/exclude globs. The
`split-rules:` block in the .bst maps directly to glob patterns.

### `kind: junction`

**Granularity**: handled at orchestration time, not rule-emission.

Junctions reference another BuildStream project. The orchestrator
treats a junctioned project as a separate workspace: its elements
get translated under a separate Bazel module, bridged into the
parent module via `bazel_dep` against a `local_path_override`
pointing at the junction's converted output dir. No special rule
shape per junctioned element — they get translated by the same
per-kind translators as anything else, just rooted in a different
module.

### Source kinds

Independent of element kind. Already partially handled by
`orchestrator/internal/sourcecheckout`. The translator framework
calls into `sourcecheckout` for every element it processes. Per
source kind:

- `tar`, `remote`: Already handled.
- `git`: Already handled (with `kind: remote-asset` rewriting via
  `bsttranslate`).
- `local`: Already handled.
- `patch`: Apply patch after parent sources resolve. Existing.
- `workspace`: defer until needed.

## Architecture

### Translator interface

A new package `orchestrator/internal/translate/` defines a
`Translator` interface plus per-kind implementations. Shape:

```go
type Translator interface {
    // Kind returns the bst element kind this translator handles.
    Kind() string

    // Translate emits BUILD.bazel + cmake-config bundle into outDir.
    // The dep graph is provided so cross-element references resolve.
    Translate(ctx context.Context, elem *element.Element, srcRoot string, deps []ResolvedDep, outDir string) (*manifest.Entry, error)
}
```

Per-kind packages — `internal/translate/cmake`, `internal/translate/meson`,
`internal/translate/autotools`, `internal/translate/manual`,
`internal/translate/stack`, `internal/translate/filter` — each
implement this interface. The cmake translator wraps the existing
`converter/cmd/convert-element` pipeline; the others are new code.

### Dispatcher

`orchestrator/internal/orchestrator/run.go:processElement` looks up
the translator by kind from a registry built at startup, calls
`Translate`, ingests the output directory into CAS exactly the
same way the current cmake path does. Element-kind-specific code
lives entirely behind the interface.

### Cross-kind interop

The same imports-manifest plumbing the cmake-only path uses today:

- Every translator emits a `cmake-config/` bundle alongside its
  BUILD.bazel. The bundle is a synthesized `<Pkg>Config.cmake` +
  `<Pkg>Targets.cmake` matching the install layout. cmake-element
  consumers' `find_package()` resolves through this bundle without
  caring what kind produced it.
- Bazel-side: every translator's BUILD.bazel exposes a
  `<element>` filegroup, a `headers` filegroup, and per-library
  `cc_import` targets. cc_library consumers reference these by
  label.

The `cmake-config/` synthesis is heuristic for non-cmake kinds —
walk the install delta, every `lib*.so`/`lib*.a` becomes an
`add_library(... IMPORTED)` entry, every `include/<pkg>/`
becomes the include dir. Where the heuristic fails, a Tier-1
`coarse-export-incomplete` failure with a hint to add a
hand-written override under `non_cmake_stubs/` (the existing
M1 escape hatch).

### Toolchain bootstrap

A `kind: manual` toolchain element (gcc, glibc, …) is a coarse-
grained genrule that produces an install tree. cmake elements
downstream of it consume it via `cc_toolchain` declarations
generated from the install tree, the same way `derive-toolchain`
already works against a real install today. The interesting
addition: `derive-toolchain` runs against the toolchain element's
output, not against the host system, so the produced
`cc_toolchain_config.bzl` describes the converted compiler, not
the operator's `/usr/bin/gcc`.

## Phasing

Eight phases, each its own PR. Phases 2–6 can interleave once Phase
1's interface is stable.

**Phase 0 — FDSDK survey.** New `docs/fdsdk-element-survey.md`.
Counts, per-kind config samples, source-kind breakdown.

**Phase 1 — Translator framework.** New `internal/translate/`
package with the interface + registry + dispatcher integration in
the orchestrator. Refactor existing cmake pipeline behind the
interface (no behavior change). Acceptance: existing cmake e2e
tests still pass, but the orchestrator now goes through the
translator interface.

**Phase 2 — Stack + filter translators.** Trivial. Acceptance: a
fixture with one stack element pulling in two cmake elements
converts; `bazel build //...` resolves the stack as a label
referring to both.

**Phase 3 — Autotools translator (coarse).** The biggest single
chunk of FDSDK by element count. Acceptance: a fixture with one
autotools element (openssl-flavored) converts; `bazel build //...`
produces the install tree; a downstream cmake element
`find_package`'s its synthesized config bundle and links
successfully.

**Phase 4 — Meson translator (fine).** Surface area similar to the
cmake translator. Acceptance: a fixture with one meson element
(libfoo-flavored), fine-grained `cc_library` rules emitted, fmt-
style fidelity gate (symbol equivalence vs `meson setup && ninja`
reference).

**Phase 5 — Manual translator (coarse).** Long-tail support. Most
of the work is the variable-substitution engine. Acceptance: a
fixture with one manual element converts and builds.

**Phase 6 — Toolchain bootstrap.** Wire `derive-toolchain` against
a converted toolchain element instead of the host. Acceptance:
the FDSDK subset converts and builds with the converted toolchain
on the cc_toolchain rule path, not the host's.

**Phase 7 — Junction handling.** Cross-project plumbing. Acceptance:
a fixture with two BuildStream projects junctioned together
converts; both projects' elements are reachable from the parent
Bazel module.

**Phase 8 — FDSDK acceptance.** Run the full pipeline over an
FDSDK subset that exercises every translator (1 stack + 1 filter +
1 autotools + 1 meson + 1 manual + 1 cmake). `bazel build //...`
succeeds. Document any FDSDK-specific deltas in
`docs/fidelity-known-deltas.md`. After this, the gate moves to
the full FDSDK graph.

## Out of scope (explicitly)

- **Replacing every element kind's plugin with a fine-grained
  translator on day one.** Coarse-grained is acceptable as a
  default for kinds where introspection isn't natively available;
  finer comes later if performance demands.
- **Reimplementing BuildStream's plugin model in Go.** The
  translators borrow the *interface idea* but each one is a small
  focused emitter, not a faithful re-implementation of the BST
  plugin.
- **Cross-distro generalization.** Same as the previous draft:
  FDSDK is the validator; Debian / Fedora / Alpine / etc. are not
  in scope until FDSDK ships.
- **Workspace-aware translation** (handing converted BUILD.bazel
  files back into a `bst workspace`-driven dev loop). Possibly a
  future ask, but not a v1 concern.

## Open questions for review

1. **Granularity defaults**: is "coarse autotools, fine meson" the
   right starting split? An argument for going coarse on meson
   too is that fine-grained cmake is the gate that has to ship
   first; meson-fine could be a Phase 9 addition rather than
   Phase 4.

2. **Variable substitution in `kind: manual`**: BuildStream's
   substitution is project-defined and recursive. Phase 5's
   substitution engine could either (a) re-implement BST's
   resolver in Go, or (b) outsource substitution to BST itself
   at translation time (`bst show --format ...`). The user's
   "no bst at runtime" constraint allows option (b) at
   translation time since the orchestrator already shells out;
   "no bst at build time" is the firmer rule.

3. **Toolchain bootstrap ordering**: the converted toolchain has
   to exist before any cmake-element conversion runs (because
   cmake's File API probe needs a working compiler). Phase 6
   handles this, but Phases 2–5 land before it. During those
   phases, do we use the host toolchain (operator's gcc) for
   cmake-element conversion, knowing it'll be wrong for FDSDK
   acceptance? Or do we gate Phase 8 on Phase 6 completion?

4. **Phase 2 ordering risk**: the survey (Phase 0) might surface
   a kind we haven't planned for. If FDSDK uses a kind not in
   the table above (e.g. `kind: x86_64-image` or a project-
   specific custom plugin), Phase 0's output reshapes Phase 2's
   plan. That's fine — we plan after we know.
