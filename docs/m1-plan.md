# M1: CMake → Bazel converter, FDSDK PoC

## Context

We're migrating the FreeDesktop SDK BuildStream project from CMake to Bazel
+ Buildbarn remote execution. The strategic decision is **not to translate
CMake source to Starlark**: CMake's runtime model
(function-blocker stack reconstructing control flow, textual macro substitution,
late-binding through string-valued variable references) makes static translation
intractable. Instead, run real CMake in a hermetic bwrap sandbox and translate
its **structured output** — File API JSON (`.cmake/api/v1/reply/`) plus parsed
`build.ninja` — into fully-declared `BUILD.bazel` rules and synthetic
`<Pkg>Config.cmake` bundles. See `cmake_analysis.md` for the detailed analysis
that drives this decision.

This plan covers **M1 only**: a standalone `convert-element` binary that
converts a single CMake element end-to-end. M2–M5b extend this into a
Buildbarn-orchestrated multi-element converter; sketched at the bottom.

> **Note (post-M5):** The original draft named libdrm as the M1
> acceptance gate, on the mistaken assumption that libdrm is a cmake
> project. libdrm is actually meson-based, so the libdrm-specific
> hooks (`make e2e-libdrm`, `testdata/fileapi/libdrm-snapshot/`,
> `non_cmake_stubs/glibc/`) never landed. M2's fmt e2e
> (`make e2e-fmt`) became the real-world cmake acceptance package
> instead — it exercises the same surfaces (find_package consumer,
> shadow tree, codegen recovery for fmt's tests) that libdrm was
> meant to cover.

## Key decisions

- **Language:** Go for both converter and orchestrator. One module so
  `manifest`, `failure`, and File API types are shared.
- **Build:** `go build` — not Bazel — for M1. Avoid the bootstrap recursion of
  needing Bazel + rules_go to build the very tool that emits Bazel rules.
- **Single Go module** rooted at the repo.
- **External tools** (`cmake`, `ninja`, `bwrap`) on PATH at pinned versions,
  asserted at startup with a `--allow-cmake-version-mismatch` escape hatch.
- **`bst` (BuildStream)** is not a converter dependency. The converter takes
  `--source-root` already-extracted; the orchestrator (M3) handles
  `bst source checkout`.
- **BUILD.bazel emission:** spike `github.com/bazelbuild/buildtools/build` AST
  in week 1; if its transitive deps are clean, use it. Otherwise fall back to
  `text/template` + post-pass through `buildifier`.
- **Shadow tree:** full path-only-stat implementation in M1, including the
  default content allowlist and `--trace-expand` read-path recording. M3's
  orchestrator wires it into REAPI input roots; M1 proves it works end-to-end
  against libdrm. The libdrm e2e must succeed against the *shadow* tree, not
  just the real tree.

## Critical files

```
go.mod
Makefile
.github/workflows/ci.yml
docs/
  m1-plan.md
  failure-schema.md
converter/
  cmd/convert-element/main.go
  internal/
    cli/flags.go
    hermetic/sandbox.go
    hermetic/toolchain.go
    cmakerun/run.go
    fileapi/reply.go
    fileapi/codemodel.go
    fileapi/toolchains.go
    fileapi/cmakefiles.go
    ninja/parse.go
    ir/types.go
    lower/lower.go
    emit/bazel/emit.go
    emit/cmakecfg/emit.go
    failure/failure.go
    manifest/manifest.go
    shadow/shadow.go
    shadow/allowlist.go
    shadow/trace.go
  testdata/
    sample-projects/hello-world/
    sample-projects/two-target/
    fileapi/hello-world/
    fileapi/libdrm-snapshot/
    golden/hello-world/BUILD.bazel.golden
    golden/libdrm-snapshot/BUILD.bazel.golden
    golden/libdrm-snapshot/libdrm{Config,Targets,Targets-Release}.cmake.golden
non_cmake_stubs/glibc/{GlibcConfig,GlibcTargets}.cmake
tools/fixtures/record-fileapi.sh
```

## Pipeline

```
CLI flags ──► hermetic.Sandbox ──► cmakerun.Configure ──► fileapi.Reply
                                                          + ninja.Graph
                                                                │
                                                                ▼
                                                          lower.ToIR
                                                                │
                                                                ▼
                                                          ir.Package
                                                          /         \
                                                   emit/bazel    emit/cmakecfg
                                                          \         /
                                                          manifest.Write
```

Each stage is independently unit-testable. Most bugs will live in `lower/`;
structure as small pure functions over IR.

## External libraries

- `github.com/google/go-cmp/cmp` — golden-test diffs (test-only).
- `github.com/bazelbuild/buildtools/build` — Buildifier AST for emitting
  BUILD.bazel (preferred; spike in week 1).
- Standard library only otherwise.

## Test strategy

**Tier A — pre-recorded fixtures (millisecond unit tests):**
- `testdata/fileapi/<case>/` holds checked-in reply JSON, generated once via
  `tools/fixtures/record-fileapi.sh`.
- Tests in `lower/`, `emit/bazel/`, `emit/cmakecfg/` skip `cmakerun`/`hermetic`;
  consume fixtures, diff against `testdata/golden/`.
- `go test ./...` runs without cmake installed.
- Update with `make update-golden`.

**Tier B — real cmake e2e (gated, build tag `e2e`):**
- `hello-world` and `two-target` from `sample-projects/` (checked in).
- `libdrm` fetched in CI from a pinned freedesktop git tag.
- Steps: fetch → run pipeline (incl. bwrap + cmake) → assert BUILD.bazel matches
  golden → run `bazel build` on the output → run a downstream consumer fixture
  doing `find_package(libdrm REQUIRED)` against our synthesized bundle.

## Open questions to resolve during M1 (don't block start)

1. **bwrap invocation strategy.** Direct `os/exec` of the binary (simplest).
2. **buildtools AST vs text/template.** Decide week 1 by spiking both for one
   rule shape.
3. **Genrule recovery scope.** libdrm has minimal/zero codegen. If zero, M1
   stubs custom-command support with a Tier 1 `unsupported-custom-command`
   failure; full support comes in M2.
4. **`<Pkg>Targets-Release.cmake` IMPORTED_LOCATION layout.** Likely
   `<out>/cmake/<Pkg>/` for the bundle, `<out>/lib/`, `<out>/include/` for
   artifacts. Decide concretely after first libdrm dry-run.
5. **Failure tier enumeration.** Pin in `docs/failure-schema.md` before M2.
6. **Generator-expression handling.** M1 scope: fold what the File API has
   already resolved; refuse what isn't with a Tier 1 failure.
7. **Multi-config.** M1 emits Release only.
8. **Shadow-tree empty-file representation.** Default to plain zero-byte files;
   revisit if disk inode pressure becomes real.
9. **Per-package allowlist augmentation lifecycle.** M1 records
   `read_paths.json` per run but does NOT merge it back into a persistent
   per-package allowlist registry — that's M3 orchestrator scope.

## Subsequent milestones (sketch)

Status legend: ✅ done, 🔧 partial / validation pending, ⏳ queued.

| M | Status | Goal | Wks | Acceptance |
|---|---|---|---|---|
| M2 | ✅ | build.ninja parser; recovered codegen tagged for project-wide audit; multi-element graph | 2 | Codegen-using element converts; every recovered genrule carries a `cmake-codegen` tag (with driver and recovery-mode sub-tags), every consuming target carries `has-cmake-codegen`; documented in `docs/codegen-tags.md` with stability promise; Bazel-vs-BuildStream parity check on a real package |
| M3a | ✅ | Local orchestrator: BuildStream YAML reader, per-element subprocess loop, shadow + imports + synth-prefix + allowlist registry + action-key cache | 1.5 | Every FDSDK kind:cmake element converts via `os/exec`; determinism test passes on three fresh tmpdirs |
| M3b | 🔧 | REAPI Execute submission against the same Action proto M5 builds | 0.5 | `--execute=grpc://...` routes per-element conversions through Buildbarn workers; client never forks the converter. M5's CAS+AC layer carries inputs and outputs. Validation against a real Buildbarn (vs the in-process fake) still pending. |
| M3c | ✅ | Orchestrator-driven source provisioning for `kind: local` and `kind: git`. Other kinds error out and document the `--sources-base` workaround. | 0.5 | `make e2e-orchestrate` runs without `--sources-base` against a fixture using kind:git; cache hits on repeated runs. |
| M3d | 🔧 | BuildStream source CAS integration. FDSDK is already a BuildStream project; `bst source push/pull` content-addresses each element's source tree in the project's existing CAS. The orchestrator looks up source by uri+qualifiers via Remote Asset Fetch and materializes the resulting Directory from CAS — no git/tar/curl. Step 1 ships the `kind: remote-asset` resolver + Fetch/Push client. Step 2 ships `orchestrate-bst-translate`, which rewrites a `kind: git`/`kind: tar` element tree to `kind: remote-asset` with a stable URI scheme (`bst:source:<element-name>`) and qualifiers preserving the original spec. Operators bind the URIs in their CAS via `bst source push` (or equivalent) and point the orchestrator at the translated tree. | 1 | `orchestrate-bst-translate --in elements/ --out elements-cas/` rewrites every translatable source. The orchestrator + `--source-cas` then converts the translated tree against a populated CAS+RAA without invoking git/tar/curl. |
| M4 | ✅ | Tiered failures + regression detection + fingerprint registry — see `docs/m4-plan.md` | 1.5 | Deliberate breakage produces structured regression report |
| M5 | ✅ | Bazel envelope + `converted_pkg_repo` + real REAPI Action/ActionCache substrate (shared cache across converter instances) — see `docs/m5-plan.md` | 2 | Two independent orchestrators share cache hits via REAPI ActionCache; downstream `bazel build @libdrm//:libdrm` succeeds against the converted FDSDK subset |

## Verification

1. `make test` passes — unit tests over checked-in File API fixtures, no cmake
   required.
2. `make e2e-hello-world` passes — full bwrap+cmake+convert pipeline on a
   3-file CMakeLists, **using the shadow tree**.
3. `make e2e-libdrm` passes — same against libdrm at pinned tag, with the
   hand-curated Glibc stub. Shadow tree must produce byte-identical output to a
   real-tree run; this is the architectural keystone test.
4. **Synth-bundle drop-in test:** swap our synthesized libdrm Config bundle into
   FDSDK's BuildStream-installed prefix, run a real (non-Bazel) CMake configure
   of an unrelated downstream consumer doing `find_package(libdrm REQUIRED)`,
   verify imported targets resolve.
5. The output BUILD.bazel builds successfully under `bazel build //:libdrm`.
6. `convert-element --help` documents all flags; running with mismatched cmake
   produces a clear error.
7. **Shadow-tree invariance check:** modify a `.c` file's content in the libdrm
   source tree (without touching its size or any allowlisted file), re-run
   `convert-element`, verify the output BUILD.bazel and bundle are
   byte-identical.
8. `read_paths.json` lists every source-tree path the converter saw being read,
   parsed from `--trace-expand --trace-format=json-v1`.

## Implementation steps

1. `go mod init`, write `Makefile`, set up CI workflow with cmake/ninja/bwrap
   install.
2. Write `internal/fileapi/` typed structs against a pre-recorded `hello-world`
   reply directory (no cmake invocation yet).
3. Write `internal/lower/` + `internal/emit/bazel/` for the simplest case
   (single static library, no codegen) with golden tests.
4. Add `internal/hermetic/` + `internal/cmakerun/` and wire the Tier B e2e on
   `hello-world` against the **real** source tree first.
5. Write `internal/shadow/` (allowlist + tree creator + trace parser); re-run
   e2e on `hello-world` against the **shadow** tree; assert byte-identical
   output.
6. Write `internal/emit/cmakecfg/`; run synth-bundle drop-in test against
   `hello-world`, then libdrm.
