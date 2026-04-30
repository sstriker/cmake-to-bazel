# FDSDK reality check

Survey of what real `freedesktop-sdk` content (gitlab.com/freedesktop-sdk
@ master, 2026-04-30) hits in `cmd/write-a` today. Methodology: clone
FDSDK, point write-a at successively larger slices, capture the first
failure each time. The numbers are direct counts off `*.bst` files in
the cloned repo (1 092 elements total).

Run `make fdsdk-reality-check` (with `FDSDK_DIR=` pointing at a clone)
to reproduce. The script keeps probing as features land, so its output
is the canonical "what's still broken" view; this doc snapshots it as
of the survey date for narrative context.

## Summary

`cmd/write-a` parses none of FDSDK as-shipped today. Every entry point
hits at least one of the gaps below before reaching a `kind:` handler.
The gaps fall into three buckets:

### Empirical first-failures (from `make fdsdk-reality-check`)

The probe runs each element in isolation (no FDSDK project.conf
alongside) so the per-element gap surfaces directly. As gaps
close, the probes progress to later failure points:

**In-place probes** (write-a runs against the real FDSDK tree —
project.conf parsing, `(@):` composition, source-kind dispatch,
and path-qualified element resolution all engage):

| Element | Kind | First failure | Punch list |
|---|---|---|---|
| `components/bzip2.bst` | stack | dep `public-stacks/runtime-minimal` not in graph | single-element-load |
| `components/boot-keys-prod.bst` | import | kind:local path resolves bst-dir-relative; FDSDK declares them project-root-relative | #11 |
| `components/expat.bst` | cmake | dep `public-stacks/runtime-minimal` not in graph | single-element-load |
| `components/aom.bst` | cmake | dep `public-stacks/runtime-minimal` not in graph | single-element-load |
| `bootstrap/bzip2.bst` | manual | dep `bootstrap/gnu-config` not in graph | single-element-load |
| `components/tar.bst` | autotools | dep `bootstrap/bash` not in graph | single-element-load |
| (synthetic multi-element probe) | mixed | — | **OK** |

After #8, every kind:cmake / kind:autotools / kind:manual element
in the curated set parses + accepts its source list + resolves its
project-rooted element name. The remaining first-failure for those
five is uniformly "dep X not in graph" — the probe loads only
the named .bst, not its transitive dep tree, so single-element
loads naturally surface this. That's a probe-shape limitation
rather than a write-a gap: a multi-element probe that loads all of
FDSDK at once would expose what's actually next.

The single remaining write-a gap surfaced by `boot-keys-prod` is
#11 (kind:local paths resolve bst-dir-relative, but FDSDK declares
them project-root-relative). Small fix, separate PR.

The synthetic probe exercises every closed punch-list item
end-to-end: path-qualified deps, build-depends + runtime-depends,
multi-source elements with `directory:` flag, `public:` block
tolerance, `(@):` composition, and source-kind dispatch (a
kind:git_repo source's metadata flows onto the resolvedSource
entry; staging skips it gracefully). It passes today.

1. **Loader gaps** — write-a's `.bst` / `project.conf` parsing surface
   doesn't match what real `.bst` files declare. These are mechanical
   to close (additional YAML keys / shapes the loader must accept) but
   they block every downstream test.
2. **Cross-element resolution gaps** — write-a's dep / source-path
   resolution assumes flat element basenames + single-source elements;
   FDSDK uses subdir-qualified paths and frequently bundles multiple
   sources per element.
3. **BuildStream composition / conditional directives** — `(@):`
   includes and `(?):` arch conditionals show up in 18 % and 7 % of
   elements respectively (plus the project.conf itself). The first
   reshapes into a YAML pre-processor at write-a's loader; the second
   is the architectural piece — should lower to project-B Starlark
   `select()` over `@platforms`, not write-a-side resolution.

## Ordered punch list

Ordered by **smallest unblocker first** — closing each gap unblocks
strictly more of FDSDK with strictly less new machinery.

### 1. `build-depends` / `runtime-depends` keys (914 / 129 elements) ✓ done

`bstFile` now reads all three dep keys (`depends`, `build-depends`,
`runtime-depends`); `loadGraph` merges them into `element.Deps`,
dedup-ing by element pointer so a dep listed in two categories
produces a single edge. The build-vs-runtime distinction lands
later, when the typed-filegroup wrapper for pipeline-kind outputs
exposes runtime-only labels separately.

### 2. Path-qualified element references (6 018 dep references) ✓ done

`project.conf` now reads `element-path:` (defaults to `.`).
loadGraph keys each element by its path relative to
`<project-root>/<element-path>`, minus `.bst`, so a `.bst` at
`<project>/elements/components/bzip2.bst` keys as
`components/bzip2` and a `depends: [bootstrap/bzip2.bst]` reference
resolves regardless of which subdirectory the dep declaration
lives in. Same-basename collision (FDSDK has both
`elements/components/bzip2.bst` and `elements/bootstrap/bzip2.bst`)
no longer overwrites the graph entry.

When no project.conf is found, write-a falls back to basename
keying (the pre-project.conf shape that
`testdata/meta-project/two-libs/` and similar fixtures rely on).

### 3. Multi-source elements (129 elements) ✓ done

`loadElement` no longer hard-errors on `len(sources) != 1`; it
iterates every source and resolves each kind:local entry into
`element.Sources []resolvedSource`. A new `stageAllSources` helper
in `main.go` drives the per-handler staging:

- `kind:import` (`handler_import.go`): every source mounts into
  `elemPkg/<directory>/`.
- pipeline kinds (`handler_pipeline.go`): every source mounts into
  `elemPkg/sources/<directory>/`; the genrule's
  `glob(["sources/**"])` picks them all up uniformly.
- `kind:cmake` (`handler_cmake.go`): single-source-no-directory
  elements still get the existing read-set-narrowing path;
  multi-source or any-source-with-directory elements fall through
  to "stage everything as real, no zero stubs". Multi-source
  narrowing arrives when an FDSDK fixture forces it.

### 4. Source `directory:` flag (64 elements) ✓ done

`bstSource` gains `Directory string` (yaml:directory). The new
`stageAllSources` helper resolves each source onto
`<elem-pkg>/<directory>/` (or onto the package root when
`directory` is empty). Last-writer-wins on collisions — matches
BuildStream's source-merge behavior.

### 5. `public:` block tolerance (355 elements) ✓ done

`bstFile` gains `Public yaml.Node` (same pattern as `Config`).
Decoded but inert today — the `public:` block round-trips onto
the bstFile so handlers can read it later. kind:filter's domain
enforcement (which consumes `public.bst.split-rules`) lands
alongside the typed-filegroup wrapper for pipeline-kind outputs.

### 6. `(@):` composition directive (project.conf + 202 elements) ✓ done

`cmd/write-a/yaml_compose.go` is the BuildStream-shape YAML
pre-processor: `loadAndComposeYAML` parses a file into a yaml.Node
tree, walks the tree resolving every `(@):` directive (with
include-cycle detection), then strips other unhandled BuildStream
directives (`(?):` / `(>):` / `(<):` / `(=):`) before the struct-
decode pass.

Composition is parent-wins-on-conflict for both scalars and nested
mappings (matches BuildStream's left-to-right composition where
the local document's keys override the included content). Include
paths resolve project-root-relative — a `runtime.yml` at
`<project>/include/` declaring `(@): - include/flags.yml` resolves
the include to `<project>/include/flags.yml` (sibling), not
`<project>/include/include/...`. That's BuildStream's contract;
file-relative resolution would have broken FDSDK's recursive
includes.

`loadProjectConf` and `loadElement` both run the composer before
the struct-decode pass. Test coverage: simple include, parent-wins
on conflict, deep-merge of nested mappings, project-root-relative
nested-include resolution, `(?):` strip, include cycle detection,
missing-include error, scalar-form `(@): "file.yml"`.

`(?):` blocks are stripped today — write-a's host-arch isn't a
faithful proxy for cross-compile target arch, so baking branches
in at codegen time would be wrong. Lowering to project-B
`select()` (#9) is the architectural follow-up.

### 7. Junction-targeted deps (62 elements) ✓ done

`bstDep` accepts both shapes via a custom `UnmarshalYAML`: a
scalar node treats the whole string as the filename; a mapping
node decodes `filename` / `junction` / `config`. For v1 only
`Filename` drives graph resolution; `Junction` and `Config` are
recorded on the `bstDep` entry but inert. Acting on them lands
once junction support proper (#10's separate junction
infrastructure) arrives.

### 8. kind:git_repo / kind:patch / kind:tar / etc. source kinds ✓ done

`bstSource` accepts arbitrary source kinds. `loadElement`
populates a `resolvedSource` entry per source: kind:local sources
carry the resolved AbsPath; non-kind:local sources (kind:git_repo,
kind:tar, kind:patch, kind:remote, ...) carry their URL/Ref/Track
metadata. `stageAllSources` stages kind:local entries normally and
skips non-kind:local ones — render-time succeeds against any
source kind, but bazel-build of the resulting BUILD would fail at
action-input merkle time on elements with non-kind:local sources
until real source-fetch integration with
`orchestrator/internal/sourcecheckout` lands.

For the reality-check goal — confirming the loader / resolver
pipeline parses real FDSDK content end-to-end — this is enough.
After this PR, every kind:cmake / kind:autotools / kind:manual /
kind:meson element in FDSDK reaches the dep-resolution phase; none
fail on source-kind anymore. The next gap is "single-element load
doesn't see transitive deps", which is a probe-shape limitation
rather than a write-a gap.

### 9. `(?):` conditional directive (81 elements)

Per-arch / per-target variable overrides:

```yaml
variables:
  (?):
  - target_arch == "x86_64":
      aom_target: x86_64
  - target_arch == "aarch64":
      aom_target: arm64
```

Per the design discussion (see PR #34's comment thread), these
should lower to project-B Starlark `select()` over `@platforms`,
**not** be evaluated at write-a time — Bazel does target-platform
resolution per-action, and baking write-a's host arch into the
rendered cmd would break cross-compile (which is most of FDSDK).

Fix shape: parse `(?):` blocks, hoist them out of the variable
resolver, and emit per-arch values into the rendered project-B
BUILD via `select(...)`. The genrule cmd references the selected
value through a make-var bound to the select.

### 11. kind:local path resolution (project-root vs bst-dir relative)

`bstSource.Path` for `kind: local` is currently resolved relative
to the .bst file's directory. Real BuildStream resolves them
project-root-relative — boot-keys-prod.bst at
`elements/components/boot-keys-prod.bst` declares
`path: files/boot-keys/PK.key`, which BuildStream resolves to
`<project-root>/files/boot-keys/PK.key`, not
`<project-root>/elements/components/files/boot-keys/PK.key`.

Fix shape: when a project.conf is found, resolve kind:local paths
against `info.ProjectRoot` rather than `bstDir`. When no
project.conf is found, fall back to bst-dir-relative (the
existing-fixture shape). Surfaced empirically by the in-place
boot-keys-prod probe.

### 10. project.conf `name`, `element-path`, `aliases:` handling

`project.conf` parsing currently consumes `variables:` only.
Real FDSDK additionally declares:

- `name:` — the project's BuildStream name (cosmetic; safe to
  ignore for now).
- `element-path:` — where to find `.bst` files relative to project
  root. Needed for path-qualified-dep resolution (item 2).
- `aliases:` — URL aliases used by `kind: git_repo` sources
  (`sourceware:bzip2.git` → `https://sourceware.org/git/bzip2.git`).
  Needed once source dispatch (item 8) lands.
- `min-version`, `fatal-warnings`, `environment`, `split-rules`,
  `plugins`, `(@):` includes — each is its own follow-up (most
  ignorable, some not).

## Quantitative summary (FDSDK, 1 092 elements)

| Feature | Elements | % | Punch-list # |
|---|---|---|---|
| `build-depends:` | 914 | 84 % | 1 |
| Path-qualified dep refs | 6 018 refs | — | 2 |
| Multi-source elements | 129 | 12 % | 3 |
| Source `directory:` flag | 64 | 6 % | 4 |
| `public:` block | 355 | 33 % | 5 |
| Element `(@):` includes | 202 | 18 % | 6 |
| Junction-targeted deps (map shape) | 62 | 6 % | 7 |
| `kind: git_repo` sources | 519 | 48 % | 8 |
| `kind: patch` sources | 55 | 5 % | 8 |
| `runtime-depends:` | 129 | 12 % | 1 |
| `(?):` conditional blocks | 81 | 7 % | 9 |

## Recommended next steps

The reality-check script (`scripts/fdsdk-reality-check.sh`) probes
representative elements and reports which gap each one hits first.
Re-run after every PR that closes a gap; the prior failures should
move down the list.

Items #1, #2, #3, #4, #5, #6, #7, and #8 are closed. The synthetic
multi-element probe exercises every closed item end-to-end (the
new addition: kind:git_repo source metadata flowing through the
resolver) and passes today. The in-place probes have moved past
every parse-/resolution-/source-kind-side gap; five of six now
fail at "dep X not in graph", which is a probe-shape limitation
(single-element load) rather than a write-a gap.

The remaining write-a gaps are:

- **kind:local path resolution (#11)** — project-root-relative
  paths. Surfaced empirically by `boot-keys-prod.bst`; FDSDK
  declares many kind:local sources with project-root-relative
  paths but write-a resolves them bst-dir-relative. Small fix.
- **multi-element load** — the probe shape is the obstacle, not
  write-a. Loading a real FDSDK subgraph (e.g. all elements in a
  given dep closure) is the next natural reality-check step;
  doesn't need a code change to write-a to exercise.
- **`(?):` conditional → project-B `select()`** (#9) — the
  architectural piece. Lowers per-arch variable overrides into
  Bazel-native multi-arch resolution rather than baking write-a's
  host arch into the rendered cmd. Today the composer strips the
  blocks; a real FDSDK build with arch-specific variables won't
  reflect those overrides until this lands.
- **real source-fetch integration** — write-a now records
  kind:git_repo / kind:tar / etc. metadata on `resolvedSource`,
  but skips them at staging time. A bazel-build of those elements
  needs the existing `orchestrator/internal/sourcecheckout` layer
  (or its successor) to actually fetch sources and feed them in.
