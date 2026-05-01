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
| `components/boot-keys-prod.bst` | import | kind:local source files genuinely absent from FDSDK's checkout (production keys are generated/supplied separately) | fixture-level |
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

### 9. `(?):` conditional directive (81 elements) ✓ done

Full pipeline-handler lowering done across PRs #45 / #49 / #51 / #52 / #53 / #54. target_arch lowers to `select()` over `@platforms//cpu:*`; project.conf-declared options lower to `config_setting` per `(option, value)`; cross-product (target_arch × option) emits per-tuple `config_setting`s combining `constraint_values` + `flag_values`. Static-fold survives for host facts (host_arch / build_arch).

Diagnosed-via-probe and **fixed**: when multiple deeply-nested `(@):` includes each declared their own `variables: (?):` block, the YAML composer's parent-wins merge dropped all but one — FDSDK's bootstrap/base-sdk/perl.bst hit this with its flags.yml-derived `bootstrap_build_arch` branches getting overridden by a higher-layer `(?):`. Composer's `mergeMappings` now detects both-mapping-have-`(?)` (key without trailing colon — that's the YAML separator) sequence values and concatenates with src (included) first, dst (parent) last so per-branch last-match-wins preserves "your local (?): overrides the included one" while letting included branches contribute when the parent's don't apply.

### 9-original. `(?):` conditional directive (81 elements) — historical

`cmd/write-a/conditional.go` parses `(?):` blocks at the
`variables:` level into structured `[]conditionalBranch` form.
`extractConditionalsFromVariables` runs after the (@): composer
and before the struct-decode pass, pulling the directive out of
the YAML tree onto `bstFile.Conditionals` /
`projectConf.Conditionals`. Recognized expression syntax:
`target_arch == "X"`, `target_arch != "X"`,
`target_arch in ("X", "Y", ...)`, and `or`-joined chains.

`resolveVarsForArch` composes the same four-layer scope as
`resolveVars` plus the matching branch from each conditional set
when one applies to arch. The pipeline handler's RenderA detects
when any conditional set is non-empty, resolves once per
supported arch (x86_64, aarch64, i686, ppc64le, riscv64,
loongarch64), groups arches by identical resolution, and emits
`cmd = select({...})` over `@platforms//cpu:*` — one branch per
arch, with dedup-collapse to single-string cmd when all per-arch
resolutions are identical.

Bazel resolves the cmd per target platform at build time rather
than write-a baking host-arch into a single rendered cmd. Cross-
compile lands without further write-a changes.

Gated by `make e2e-meta-conditional` against
`testdata/meta-project/conditional-greet/` (single kind:manual
element with `target_arch == "X"` / `target_arch in (X, Y)`
branches; the rendered cmd has one select() entry per supported
arch with the per-arch resolved path).

### 11. kind:local path resolution (project-root vs bst-dir relative) ✓ done

`loadElement` now resolves kind:local `path:` against the project
root (`includeBase` — the directory containing project.conf) when
present, falling back to the .bst's own directory only when no
project.conf was found. Matches BuildStream's kind:local plugin
contract: "the contents of a directory rooted at the project."

Existing self-contained fixtures (no project.conf, or project.conf
co-located with the .bst) are unaffected — bstDir == projectRoot
in those layouts. The change is observable when a .bst lives in a
deeper subdirectory than the kind:local source it references —
the FDSDK shape that boot-keys-prod hit.

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

Every original write-a-side punch-list item is closed (#1-#9
plus #11), plus the loader gaps surfaced by the multi-element
subgraph probe (list-form `filename:` dep entries; bare
list-merge directives; non-string `ref:` for language-package
source kinds). The synthetic multi-element probe exercises every
closed item end-to-end and passes.

The subgraph probe surfaces the next gaps via iterative
transitive-dep walking:

| Element | Subgraph deps loaded | First failure |
|---|---|---|
| `components/expat.bst` | ~80 | `kind: script` unsupported |
| `components/aom.bst` | ~80 | `kind: script` unsupported |
| `components/tar.bst` | ~80 | `kind: script` unsupported |
| `components/bzip2.bst` (stack) | ~80 | `kind: script` unsupported |
| `bootstrap/bzip2.bst` | ~120 | `bootstrap_build_arch` variable referenced but not defined |
| `components/boot-keys-prod.bst` | 0 | FDSDK-checkout-level (production keys missing) |

Two real gaps remain:

- ~~**`kind: script` plugin (53 elements)**~~ — closed.
  `pipelineHandler` registration; `pipelineCfg` gains a `Commands`
  field that maps onto the install-commands slot when set
  (kind:script's flat `config: commands:` list).
- ~~**`kind: collect_manifest` (18 elements)**~~ — closed (v1
  placeholder). No-op handler that emits an empty install_tree.tar;
  build-depends still flow through the graph correctly. Real
  manifest collection (walking dep trees + emitting JSON) lands
  alongside the typed-filegroup wrapper for pipeline-kind outputs.
- **Project.conf-supplied per-host variables** — `bootstrap_build_arch`,
  `host_triplet`, `gcc_triplet`, ... defined in FDSDK's
  `include/_private/arch.yml` but only inside `(?):` branches
  (see expression-syntax limitation below). Surfaced by the
  bootstrap subgraph; orchestration-side fix would be either
  expanding the v1 expression parser to handle FDSDK's full
  syntax, or supplying default values from a project.conf
  overlay.

Other follow-ups (none on the critical path for `write-a render`):

- **multi-element load probe shape** — the script's iterative
  walker is at ~80-120 deps per element; this can probably be
  optimized but doesn't need a write-a change.
- **real source-fetch integration** — write-a now has a
  `--source-cache` flag: pre-fetched trees indexed by source-key
  (SHA of kind+url+ref) under the cache directory stage as if
  they were kind:local. Callers populate the cache via the
  orchestrator's source-checkout layer or by hand. The actual
  fetcher reshapes into a `module_extension` per the design in
  `docs/sources-design.md`; aliases + environment now parsed so
  the data is ready when the extension lands.
- **`(?):` outside variables:** — `variables:` and `config:`
  are now both handled. `extractConditionalsFromConfig` pulls
  per-arch configure-/build-/install-/strip-commands overrides
  out of the YAML tree onto `bstFile.ConfigConditionals`; the
  pipeline handler's `resolveAt` merges matching branches'
  partial pipelineCfg into the per-tuple resolved cfg, so per-
  arch command overrides flow through the same dispatch path
  variable overrides do. `environment:` and `public:` (?):
  blocks still fall through `stripRemainingConditionals` —
  the loader doesn't error, but the per-branch overrides at
  those depths aren't honored. Lands when a fixture forces it.
- **richer `(?):` expression syntax** — done. `host_arch` /
  `build_arch` / `bootstrap_build_arch` references work (the
  parser is variable-agnostic for `==` / `in` / `or`-chains;
  `!=` is target_arch-only because the closed-set complement is
  well-defined there). Outer parens (single layer) supported.
  `and`-combinators supported in both shapes:
  - same-LHS (`var != "X" and var != "Y"`) interprets as set
    intersection of the per-conjunct complements.
  - mixed-LHS (`target_arch == "x86_64" and bootstrap_build_arch == "aarch64"`)
    populates `conditionalBranch.Constraints` — a slice of
    `(Varname, Values)` pairs; `branchMatchesTuple` requires
    every constraint to match for the branch to fire, and
    `dispatchSpaceForElement` collects every constraint's
    Varname as a dispatch dimension.
