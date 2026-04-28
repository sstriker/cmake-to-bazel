# M3: orchestrator + shadow-tree integration + determinism

## Context

M1 shipped a single-element converter (`convert-element`). M2 made it
handle real-world CMake (codegen recovery, cross-element deps via
imports manifest, fmt acceptance). M3 lifts the converter from
"per-element CLI" to "project-scale orchestration": one invocation
walks an FDSDK element graph and produces converted BUILD.bazel + a
synthesized cmake-config bundle for every `kind: cmake` element, with
per-element caching keyed by content-stable fingerprints.

Two-phase split keeps Buildbarn integration out of the same milestone
as the orchestrator's own bring-up:

- **M3a (this plan)** — local orchestrator using `os/exec` per
  element. All architectural pieces wired: shadow tree, imports
  manifest registry, synth-prefix tree, per-package allowlist
  registry, action-key cache. Determinism proven via three-tmpdir
  replay.
- **M3b (next milestone, sketched at end)** — same orchestrator,
  swap `os/exec` for REAPI Action submission against Buildbarn. No
  semantic change; pure scale play.

## Key decisions

- **BuildStream YAML scope.** Read the strict subset FDSDK uses:
  `kind`, `depends`, `sources`, optionally `variables`. Don't model
  variants, junctions, or conditional inheritance until a real FDSDK
  element forces us to.
- **Source-tree provisioning is user-driven.** The orchestrator takes
  `--fdsdk-root` and expects sources to be already-checked-out under
  a known layout. `bst source checkout` integration is post-M3.
- **Cross-element prefix staging.** For each element B's conversion,
  every dep A's synthesized `<Pkg>Config.cmake` bundle is staged into
  a temporary CMAKE_PREFIX_PATH tree along with zero-byte stubs at
  every IMPORTED_LOCATION path the bundle references. cmake's
  `if(NOT EXISTS)` check (cmake_analysis.md §13) passes against the
  stubs; no real artifacts needed.
- **Action-key cache lives on disk.** `out/cache/actions/<key>/`
  holds the outputs of the last conversion that hashed to `<key>`.
  M3 ships unbounded; M4 adds eviction.
- **One Go module.** Orchestrator code goes in `orchestrator/` next
  to `converter/`, sharing the same `go.mod`. Common types
  (manifest, failure) are imported from `converter/internal/`.

## Step plan with timing

| # | Step | Days | Days (risk-adj) |
|---|---|---:|---:|
| 1 | BuildStream YAML reader (subset) + element-graph topo sort | 1.5 | 2 |
| 2 | Orchestrator: per-element `os/exec` loop with deterministic ordering | 1.5 | 2 |
| 3 | Imports-manifest construction from the dep-export registry | 1 | 1.5 |
| 4 | Synth-prefix tree builder for cross-element deps | 1 | 1.5 |
| 5 | Per-package allowlist registry: merge `read_paths.json` per element | 1 | 1.5 |
| 6 | Action-key fingerprint + idempotent re-run cache | 1 | 1.5 |
| 7 | Determinism test: three-tmpdir replay, byte-identical global manifest | 0.5 | 0.5 |
| | **Total** | **7.5** | **10.5** |

## Critical files

```
orchestrator/
  cmd/orchestrate/main.go                  # CLI: --fdsdk-root, --out, --jobs
  internal/element/
    yaml.go                                # BuildStream YAML reader (subset)
    graph.go                               # element graph + topo sort
  internal/orchestrator/
    run.go                                 # per-element subprocess loop
    manifest.go                            # global converted.json registry
    cache.go                               # action-key -> result cache
  internal/synthprefix/
    build.go                               # zero-byte IMPORTED_LOCATION stubs
  internal/allowlistreg/
    merge.go                               # read_paths.json -> per-elem allowlist
  testdata/
    fdsdk-subset/                          # checked-in reduced FDSDK fixture
docs/
  m3-plan.md                               # this plan
  orchestrator-output-layout.md            # canonical out/ tree shape
```

## Output layout

```
out/
  MODULE.bazel                             # declares each elem_<name> as repo
  elements/
    elem_<name>/
      BUILD.bazel
      cmake-config/                        # synthesized bundle
      read_paths.json                      # per-run; allowlist-reg consumes
  manifest/
    converted.json                         # registry of converted elements
    failures.json                          # Tier-1 per-element aggregation
    determinism.json                       # output-hash record for parity check
  cache/
    actions/<key>/                         # idempotent re-run cache
  registry/
    allowlists/elem_<name>.json            # merged read paths per element
```

## Pipeline (per element)

```
element/yaml + graph
        |
        v   (topo order; parallel batches per level)
   +----------------+
   | for each elem: |
   |                |
   | 1. compute action-key (shadow-tree manifest, imports, deps,    |
   |                        toolchain, converter binary)            |
   | 2. cache hit? -> copy cache/actions/<key>/ to out/elements/... |
   | 3. miss:                                                       |
   |    a. build shadow tree (allowlist + per-elem registry)        |
   |    b. write per-elem imports.json from dep-export registry     |
   |    c. build synth-prefix tree from dep bundles                 |
   |    d. exec convert-element                                     |
   |    e. parse failure.json / read_paths.json                     |
   |    f. write outputs to out/elements/elem_<name>/               |
   |    g. merge read_paths.json into registry/allowlists/          |
   |    h. populate cache/actions/<key>/                            |
   |    i. append elem.exports to global converted.json registry    |
   +----------------+
        |
        v
   global manifest writers (converted.json, failures.json,
                            determinism.json)
```

## Open questions (resolve during M3a)

1. **FDSDK YAML semantic subset.** Spike against current FDSDK trunk
   in step 1; add fields incrementally as elements force them.
2. **Pinned-toolchain layout for synth-prefix.** Probably flat
   `<prefix>/{include,lib,lib/cmake/<Pkg>}` per dep, with a global
   CMAKE_PREFIX_PATH list. Decide concretely after first
   cross-element conversion.
3. **Determinism boundaries.** `SOURCE_DATE_EPOCH` + shadow tree +
   identical in-sandbox source/build paths (`/src` and `/build` per
   element) should suffice. Verify in step 7.
4. **Failure aggregation schema.** `{ elements: [{ name, code,
   message, context }] }`. Codes are the same Tier-1 set from
   `docs/failure-schema.md`.
5. **Source-checkout responsibility.** Orchestrator expects
   pre-checked-out sources for M3a/M3b. `bst source checkout`
   integration is **M3c** — explicitly tracked in `docs/m1-plan.md`'s
   subsequent-milestones table. Until then operators run `bst source
   checkout --deps build <element>:<dest>` per element and pass
   `orchestrate --sources-base <root>`; the orchestrator's per-element
   `resolveSource` honors `kind: local` paths only and refuses other
   source kinds with a clear error.

## Risks

- **YAML semantics blow up step 1's budget.** Mitigation: scope to
  kind:cmake elements; non-cmake elements are stub-edges in the
  graph (their exports come from a hand-edited manifest, like Glibc
  in M1).
- **Cross-element find_package resolution.** Element B's cmake
  configure must locate A's bundle via CMAKE_PREFIX_PATH and the
  EXISTS-checked stubs must satisfy A's `if(NOT EXISTS)` loop. M2's
  drop-in test validated this for hello-world; step 4 extends to
  cross-element.
- **Determinism leaks via cmake's File API.** Codemodel records
  absolute build/source paths. Mounting both at fixed in-sandbox
  paths (`/src`, `/build`) makes codemodel JSON byte-stable across
  machines. Verify in step 7.

## Acceptance

1. `make e2e-m3-determinism` — three fresh tmpdirs against a
   reduced FDSDK fixture under `orchestrator/testdata/fdsdk-subset/`
   produce byte-identical `out/manifest/determinism.json` and
   per-element BUILD.bazel files.
2. `make e2e-m3-cache` — running the orchestrator twice on the same
   fixture with content-only `.c` edits between runs produces a
   100% cache-hit second pass.
3. Failure-mode coverage: deliberately break one element in the
   fixture; the rest still converge, and `out/manifest/failures.json`
   carries the typed Tier-1 entry.
4. `make test` (no cmake) and `make test-e2e` (with cmake/bwrap)
   both pass.

## M3b sketch (separate milestone)

| # | Step | Days |
|---|---|---:|
| 1 | REAPI Action assembly: action descriptor, command, input root from shadow tree + toolchain | 1 |
| 2 | gRPC client: ContentAddressableStorage upload + Execution service submission | 1 |
| 3 | Result extraction: download outputs, populate the orchestrator's local cache | 0.5 |
| 4 | Buildbarn integration test (LocalRunner mode first, then a single worker) | 1 |
| 5 | Determinism test re-run with REAPI execution | 0.5 |
| | **Total** | **4** |

M3b changes only `internal/orchestrator/run.go`'s execution call —
from `os/exec.Command(convertElement, args...)` to a REAPI Action
submission. Everything upstream (graph, shadow tree, imports manifest,
synth-prefix, allowlist registry) is unchanged.
