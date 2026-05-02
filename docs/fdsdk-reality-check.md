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
alongside) so the per-element gap surfaces directly:

| Element | Kind | First failure | Punch list |
|---|---|---|---|
| `components/bzip2.bst` | stack | path-qualified dep `public-stacks/runtime-minimal` not in graph | #2 |
| `components/boot-keys-prod.bst` | import | multi-source element (10 sources) | #3 |
| `components/expat.bst` | cmake | unsupported source kind `git_repo` | #8 |
| `components/aom.bst` | cmake | yaml unmarshal: `(?):` block (line 15) | #9 |
| `bootstrap/bzip2.bst` | manual | multi-source (2: git_repo + patch) | #3 + #8 |
| `components/tar.bst` | autotools | "0 sources" (sources live in element-level `(@):` include) | #6 + #3 |
| (project.conf in-place) | — | yaml unmarshal: `(@):` in `variables:` | #6 |

Six different elements, six different first failures. The variance
confirms the gaps are independent — closing them in punch-list order
should make distinct probes pass distinct phases incrementally.

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

### 2. Path-qualified element references (6 018 dep references)

`bzip2.bst` declares `depends: [public-stacks/runtime-minimal.bst,
bootstrap/bzip2.bst]`. Our `loadElement` derives the element name
from the basename (`bstFile.Depends` → `runtime-minimal`,
`bzip2`); the resolver then can't find them because the graph
keys by full path.

Fix shape: load every `.bst` under FDSDK's `element-path` (a
project.conf field; defaults to `elements/`). Key the graph by the
relative path under `element-path` rather than basename. Dep
resolution then matches against the same key.

### 3. Multi-source elements (129 elements)

`boot-keys-prod.bst` declares 10 sources. write-a errors:
`requires exactly one source per element`.

Fix shape: drop the hard-error in `loadElement`; iterate every
source. For kind:cmake / kind:autotools / kind:make / kind:manual /
kind:import, extend `stagePipelineSources` (and the cmake handler's
own staging) to copy each source tree into the element's package.

### 4. Source `directory:` flag (64 elements)

Per-source staging subpath: a source declares `directory: extra-kek`
to land its content under `extra-kek/` inside the element's source
tree. We don't currently parse this — every source effectively
stages at the package root.

Fix shape: extend `bstSource` with `Directory string` (yaml:directory),
honor it in source staging.

### 5. `public:` block tolerance (355 elements)

Real elements declare a `public: bst: split-rules: ...` block
defining domain → file-glob mappings (e.g. `runtime`, `devel`,
`debug`). We don't parse `public:` so it's currently a silent
ignore. That works at parse time (yaml.v3 ignores unknown fields)
but means kind:filter has no way to consume domain definitions.

Fix shape: extend `bstFile` with `Public yaml.Node` (same pattern
as `Config`); each handler that needs split-rules decodes it.
kind:filter's domain enforcement lands when both this and the
typed-filegroup wrapper for pipeline-kind outputs are in place.

### 6. `(@):` composition directive (project.conf + 202 elements)

The very first loader call against FDSDK fails at `project.conf`'s
top-level `variables:` block: it carries a `(@):` list directing
BuildStream to load and merge `include/_private/arch.yml` and
`include/repo_branches.yml` into the surrounding map. Our YAML
unmarshal sees the unmerged shape and errors: `cannot unmarshal
!!seq into string`.

202 elements (18 %) use the same directive at the element level
(typically `(@): - elements/include/some-include.yml` to share
per-target variables across many elements).

Fix shape: a YAML pre-processor that runs `(@):` resolution before
unmarshal — read the file path(s), parse them, deep-merge into the
parent map. BuildStream's actual semantics include `(>):`
list-append, `(<):` list-prepend, `(=):` overwrite, and merging
rules; for v1 a basic deep-merge of mappings + list concatenation
covers what we observe in FDSDK.

### 7. Junction-targeted deps (62 elements) ✓ done

`bstDep` accepts both shapes via a custom `UnmarshalYAML`: a
scalar node treats the whole string as the filename; a mapping
node decodes `filename` / `junction` / `config`. For v1 only
`Filename` drives graph resolution; `Junction` and `Config` are
recorded on the `bstDep` entry but inert. Acting on them lands
once junction support proper (#10's separate junction
infrastructure) arrives.

### 8. kind:git_repo / kind:patch source kinds (519 + 55 elements)

write-a only supports `kind: local`. kind:git_repo dominates real
sourcing (mirrors a git repo with URL aliases); kind:patch overlays
patches onto an existing source tree.

Fix shape: source-kind dispatch (mirrors element-kind dispatch).
For the reality-check survey, a stub `kind: git_repo` source that
records the URL but doesn't fetch unblocks rendering of every
element that currently declares git_repo. Real fetching wires into
`orchestrator/internal/sourcecheckout`.

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

Items #1 (build/runtime-depends) and #7 (junction-targeted dep map
shape) are closed. The reality-check probes don't visibly move yet
— every probe still trips earlier on path-qualified deps, multi-
source, or kind:git_repo. The next stack-on PRs target those:

1. **PR `path-qualified element resolution`** — closes #2 + the
   `element-path` slice of #10. Unblocks every dep reference; first
   probe (`bzip2.bst` kind:stack) should reach a different failure.
2. **PR `multi-source elements + public: tolerance + source.directory`** —
   closes #3, #4, #5. Reaches the variable-resolver phase on most
   elements.

After those, the survey re-runs from the variable-resolver side:
the next forcing function will likely be `(@):` composition (item
6), which is the first non-mechanical change on the list. `(?):`
conditional handling (item 9) is the architectural piece; it lands
once the FDSDK fixture forces it.
