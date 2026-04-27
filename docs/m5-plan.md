# M5: Bazel envelope + REAPI Action/ActionCache substrate

## Status (post-merge)

M5 shipped on branch `claude/m5-bazel-envelope-FIWUK` (PR #4). M3b's
REAPI Execute path landed alongside since the shapes were already in
place — see the M3b row in `docs/m1-plan.md`'s milestone table.
Validation against a real Buildbarn (vs the in-process fake) is the
remaining 🔧 item; docker-compose Buildbarn integration is queued.

The `bst source push/pull` Remote Asset API flow originally implied
by "M3c" was reshaped: M3c shipped as orchestrator-driven `kind: git`
checkouts (simpler), and the BuildStream-CAS-via-Asset-API approach
moved to M3d (queued).

## Context

M3a produces `<out>/converted/<elem>/{BUILD.bazel, bundle/, manifest.json}`
plus the `<out>/cache/actions/<key>/` action-key cache that lets a single
orchestrator instance skip re-conversion on identical inputs. M4 ships the
regression diff + fingerprint history layered on top.

M5 completes the loop in two directions:

1. **Bazel envelope.** Wrap each converted element as a `converted_pkg_repo`
   that a downstream `bazel build` can consume directly — `MODULE.bazel`
   declares the repos, `BUILD.bazel` files are the ones M3a already emits, the
   `<Pkg>Config.cmake` bundle is mounted at a per-repo prefix path so any
   in-repo CMake-aware consumer still resolves `find_package`.
2. **REAPI as the cache substrate, using real Actions.** M3a's action-key
   cache is a local filesystem directory; that's enough for one machine, but
   every CI runner redoes work. M5 builds a **real REAPI `Action`** for each
   conversion — `Command` proto with argv/env/output_paths, input root as a
   Merkle tree of `Directory` protos, platform constraints — uploads inputs
   to CAS, executes locally, then publishes the result via
   `UpdateActionResult` keyed on the standard `Action` digest. Cache lookups
   go through `GetActionResult`. Independent converter instances share hits
   via the standard Bazel-compatible action cache.

Crucially, M5 does **not** call `Execute` (no remote execution). The
orchestrator still drives `os/exec` of the converter locally; it just wraps
the local run in real REAPI shapes so the cache layer is REAPI-native.
M3b plugs into the same `Action` that M5 already builds — its delta is
"swap local fork for `Execute(action_digest)`". No re-plumbing, no
custom cache schema to migrate off of.

Why real Actions and not a custom index keyed by M3a's action-key? Because
the Action digest *is* M3a's action-key spelled the standard way (sha256
over a Command proto + a Merkle input root, both content-addressed). Using
the real shapes from day one means: standard tooling works (bb_browser,
`bazel`'s own ActionCache introspection), the protobuf surface stays
stable through M3b, and there's no parallel cache schema to maintain.

## Key decisions

- **Real REAPI `Action` + `ActionCache`, not a custom index.** Each
  conversion is modeled as a real REAPI `Action`: a `Command` proto
  (argv, env, declared output paths, platform), an input root digest
  (Merkle tree over shadow + imports manifest + synth-prefix tree +
  converter binary), and the resulting Action proto's sha256 *is* the
  cache key. Cache lookups use `GetActionResult(action_digest)`; cache
  writes use `UpdateActionResult(action_digest, ActionResult)`. M3a's
  custom action-key is dropped — replaced by the Action digest, which
  is the same content hash spelled the standard way.
- **Local execution, real Action shapes.** The orchestrator still
  executes the converter via `os/exec` locally. On a cache miss, after
  the local run succeeds, it: uploads each output file to CAS, packs
  `bundle/` as an `OutputDirectory` (Tree proto in CAS), assembles an
  `ActionResult` referencing all output blobs + exit code + stdout/stderr
  digests, and calls `UpdateActionResult`. On a cache hit, it walks the
  cached `ActionResult`, fetches output blobs from CAS, materializes
  them at the expected paths, and skips the subprocess. M3b later
  replaces the `os/exec` call with `Execute(action_digest)` — same
  Action, same ActionResult shape, same CAS round-trip.
- **`output_paths` declared up-front in the Command proto.** The
  converter's outputs are a fixed set: `BUILD.bazel`, `manifest.json`,
  `read_paths.json`, `bundle/` (directory). On failure, a `failure.json`
  appears at a known path. The Command declares the union; missing
  files are tolerated per REAPI semantics (they just don't appear in
  the ActionResult's `output_files`).
- **Platform properties encode tool versions.** `Command.platform`
  carries `OSFamily=linux`, `Arch=x86_64`, `cmake-version=3.28.3`,
  `ninja-version=1.11.1`, `bwrap-version=0.8.0`. Two converters with
  different cmake versions produce different Action digests — no
  silent cross-version cache poisoning. M3b workers will match these
  same constraints.
- **Deterministic input-root packing.** The shadow tree, imports
  manifest, synth-prefix tree, and converter binary are packed into
  a Merkle tree of `Directory` protos (children sorted by name,
  FileNode with size + sha256 digest + is_executable bit). Identical
  source state → identical input root digest → identical Action
  digest → cache hit. M3a's three-tmpdir determinism test guarantees
  this property holds across machines as long as inputs match.
- **Local CAS proxy fallback.** A `--cas=local:<path>` mode preserves
  M3a's filesystem cache behavior for offline / tests via a tiny
  in-process CAS+AC implementation. `--cas=grpc://host:port` selects
  the REAPI client. CI uses `--cas=grpc://` against a shared
  Buildbarn instance; unit tests use `local:`. Same code path past
  the store-interface boundary.
- **Local CAS proxy fallback.** A `--cas=local:<path>` mode preserves M3a's
  filesystem cache behavior for offline / tests. `--cas=grpc://host:port`
  selects the REAPI client. CI uses `--cas=grpc://` against a shared
  Buildbarn instance; unit tests use `local:`. Same code path past the
  store-interface boundary.
- **Bazel envelope is the consumer-facing artifact.** `converted_pkg_repo`
  is a Bzlmod module extension that, given a `--manifest=<path>` pointing
  at the orchestrator's `<out>/manifest/converted.json`, declares one
  `local_repository` per converted element pointing at
  `<out>/converted/<elem>/`. Downstream `bazel build @libdrm//:libdrm`
  Just Works.
- **CMake interop preserved.** Each `converted_pkg_repo` exposes its
  bundle directory under `cmake/<Pkg>/` so a CMake-aware downstream can
  set `CMAKE_PREFIX_PATH=external/libdrm/cmake/libdrm` and `find_package`
  still resolves. The Bazel labels are the primary surface; the bundle
  is the secondary surface for unconverted CMake-side consumers.
- **No new auth.** M5 inherits whatever credentials Buildbarn already
  accepts (mTLS, bearer token, anonymous on dev). The CAS client takes
  `--cas-tls-cert`, `--cas-tls-key`, `--cas-token-file` and passes
  through. No keyring, no oauth2 dance.

## Step plan with timing

| # | Step | Days | Days (risk-adj) |
|---|---|---:|---:|
| 1 | docs/m5-plan.md (this) | 0.5 | 0.5 |
| 2 | `internal/cas/digest.go` + `internal/cas/tree.go`: deterministic Merkle packing of input/output dirs (Directory, FileNode, Tree protos) | 1 | 1.5 |
| 3 | `internal/cas/store.go`: Store interface (CAS + AC); `local.go` filesystem impl drop-in for M3a's `<out>/cache/actions/<key>/` | 0.5 | 1 |
| 4 | `internal/cas/grpc.go`: REAPI CAS + AC client (FindMissingBlobs, BatchUpdate/BatchRead, streaming Read/Write, GetActionResult, UpdateActionResult) | 1.5 | 2.5 |
| 5 | `internal/reapi/action.go`: build `Command`/`Action` for one conversion; pack input root from shadow + imports + prefix + binary | 1 | 1.5 |
| 6 | `internal/reapi/result.go`: synthesize ActionResult from local outputs (cache write); materialize output tree from ActionResult (cache read) | 1 | 1.5 |
| 7 | Replace M3a's action-key cache with the Action/ActionCache flow; `--cas=local:...` and `--cas=grpc://...` flags | 1 | 1.5 |
| 8 | Two-orchestrator cache-share e2e: machine A converts, machine B (clean, no local converter cache) hits AC, byte-identical outputs | 1 | 1.5 |
| 9 | `bazel/converted_pkg_repo.bzl` module extension + MODULE.bazel template | 1 | 1.5 |
| 10 | Downstream Bazel-build acceptance gate: `bazel build @libdrm//:libdrm` against converted FDSDK subset | 1.5 | 2 |
| 11 | CMake-side consumer test: downstream `find_package(libdrm)` against the per-repo bundle | 0.5 | 1 |
| | **Total** | **10.5** | **16** |

## Critical files

```
internal/
  cas/
    digest.go                 # Digest type aliased to remoteexecution.Digest; sha256+size helpers
    tree.go                   # deterministic Directory/FileNode/Tree packing; REAPI-canonical
    store.go                  # Store interface: CAS (Get/Put/FindMissing) + AC (GetActionResult/UpdateActionResult)
    local.go                  # filesystem impl for offline / tests
    grpc.go                   # REAPI client: ContentAddressableStorage + ActionCache services
  reapi/
    action.go                 # build Command + Action protos; pack input root from converter inputs
    result.go                 # ActionResult <-> local-fs round trip (synth on success, materialize on hit)
    platform.go               # Platform properties: OSFamily, Arch, cmake/ninja/bwrap versions
orchestrator/
  internal/
    actioncache/runner.go     # replaces actionkey: GetActionResult -> hit path; miss -> local exec -> UpdateActionResult
    orchestrator/run.go       # pass --cas flag through to actioncache
  cmd/orchestrate/main.go     # add --cas, --cas-tls-cert, --cas-tls-key, --cas-token-file
bazel/
  converted_pkg_repo.bzl      # Bzlmod module extension: reads converted.json, declares one local_repository per elem
  MODULE.bazel.template       # bazel_dep on toolchain rules + module_extension hookup
testdata/
  bazel/downstream/           # MODULE.bazel + BUILD.bazel for the downstream consumer used in step 10
docs/
  m5-plan.md                  # this plan
```

## Output shape

### REAPI Action / ActionResult (the cache contract)

For each conversion the orchestrator builds a standard
`build.bazel.remote.execution.v2.Action`:

```
Action {
  command_digest:    <sha256 of Command proto>
  input_root_digest: <sha256 of root Directory proto>
  do_not_cache:      false
  platform:          <see platform.go>
}

Command {
  arguments: [
    "convert-element",
    "--source-root", "/sandbox/src",
    "--build-root",  "/sandbox/build",
    "--out",         "/sandbox/out",
    "--imports",     "/sandbox/imports.json",
    "--cmake-prefix-path", "/sandbox/prefix",
    ...
  ]
  environment_variables: [{name: "PATH", value: "/usr/bin:/bin"}, ...]
  output_paths: [
    "out/BUILD.bazel",
    "out/manifest.json",
    "out/read_paths.json",
    "out/failure.json",
    "out/bundle"
  ]
  working_directory: ""
  platform: { properties: [
    {name: "OSFamily",       value: "linux"},
    {name: "Arch",           value: "x86_64"},
    {name: "cmake-version",  value: "3.28.3"},
    {name: "ninja-version",  value: "1.11.1"},
    {name: "bwrap-version",  value: "0.8.0"}
  ]}
}
```

The Action digest is the cache key. On lookup:

```
result, err := ac.GetActionResult(ctx, &GetActionResultRequest{
    ActionDigest: actionDigest,
})
```

On hit, `result.OutputFiles` and `result.OutputDirectories` give us the
CAS digests for everything the converter would have produced.
Materialization is a Tree walk + per-file `BatchReadBlobs`.

On miss, run the converter locally, then:

```
ac.UpdateActionResult(ctx, &UpdateActionResultRequest{
    ActionDigest: actionDigest,
    ActionResult: &ActionResult{
        OutputFiles:       [...],
        OutputDirectories: [{Path: "out/bundle", TreeDigest: ...}],
        ExitCode:          0,
        StdoutDigest:      ...,
        StderrDigest:      ...,
    },
})
```

This is the same contract Bazel's own remote cache uses. `bb_browser`
will render every cached conversion. `bazel`'s `--remote_cache=` will
read these entries (not that we need it to — it's a sanity check that
the schema is correct).

### Bazel-envelope: `converted_pkg_repo` extension

`MODULE.bazel`:

```starlark
bazel_dep(name = "rules_cc", version = "0.0.10")

converted = use_extension("//bazel:converted_pkg_repo.bzl", "converted_pkg_repo")
converted.from_manifest(path = "//:converted.json")
use_repo(converted, "libdrm", "fmt", "uses_hello", "hello")
```

`BUILD.bazel` consumers reference labels as `@libdrm//:libdrm`,
`@fmt//:fmt`, etc. — the very labels the converter already emits in the
imports manifest as cross-element dep targets.

## Verification

1. **Unit:** `internal/cas/tree.go` packs a known directory tree to the
   exact byte digest Bazel's own `bazel-remote-apis` packer would
   produce. Compare against a checked-in golden Tree proto generated
   by a one-shot Go program that imports the upstream proto package.
2. **Action-digest stability:** running `internal/reapi/action.go`
   over identical inputs (same shadow root, same imports, same prefix,
   same converter binary, same platform) produces byte-identical
   Action protos and identical Action digests across three tmpdirs —
   the M3a determinism guarantee, restated in REAPI shape.
3. **Local-CAS regression:** M3a's existing determinism test re-runs
   against `--cas=local:<tmpdir>` and the cache behavior matches
   verbatim — same hit / miss pattern, same outputs.
4. **gRPC integration:** spin up Buildbarn-style CAS+AC in CI (Docker
   compose with `bb_storage`); orchestrator runs with
   `--cas=grpc://localhost:8980`, completes a small FDSDK subset, then
   a second orchestrator on a clean tmpdir hits 100% AC on identical
   inputs and produces byte-identical outputs.
5. **Cache-share keystone test:** machine A converts FDSDK subset and
   publishes to shared CAS+AC; machine B with no `<out>/cache/` and
   no local converter binary (just the orchestrator and the gRPC
   endpoint) hits AC for every element, materializes outputs, and the
   resulting `manifest/converted.json` is byte-identical to A's. This
   is the architectural claim — independent instances share work
   through the standard Bazel-compatible action cache.
6. **bb_browser sanity check:** point `bb_browser` at the M5 CAS+AC
   instance after a run; cached actions render with command, input
   root, and output tree visible. This is free debug surface — confirm
   it works.
7. **Bazel-build downstream gate:** `bazel build @libdrm//:libdrm`
   from `testdata/bazel/downstream/` succeeds against the converted
   FDSDK subset, with the `converted_pkg_repo` extension wiring all
   `local_repository` declarations from `converted.json`.
8. **CMake-consumer drop-in:** an unrelated downstream CMake project
   sets `CMAKE_PREFIX_PATH=$BAZEL_OUT/external/libdrm/cmake/libdrm`,
   does `find_package(libdrm REQUIRED)`, configures successfully.
9. **Cache-corruption resilience:** a malformed CAS blob (manually
   edited mid-test) produces a Tier-3 infrastructure failure with a
   clear "CAS digest mismatch" message, not silent breakage. The
   orchestrator falls through to local re-conversion and re-publishes
   a fresh ActionResult.

## Open questions

1. **Compression on the wire.** REAPI supports zstd-compressed blobs via
   `Compressor`; converter outputs are mostly small text. Default to
   uncompressed for M5 (simpler debug); add zstd opt-in once the e2e
   numbers show it would matter.
2. **Cache eviction.** M5 doesn't manage CAS retention; that's
   Buildbarn's policy. The orchestrator needs a graceful "blob
   missing" path — `GetActionResult` may return an entry whose
   referenced blobs have been evicted. Treat that case as a miss
   (re-run, re-publish). Documented as the resilience case in
   verification step 9.
3. **Converter binary in the input root.** The converter binary is
   uploaded to CAS as part of the input root on first miss, and
   referenced by digest from the Command's input root. Same blob
   stays in CAS on subsequent runs — no re-upload. M3b reuses this
   exact blob as the action's input.
4. **Multi-region CAS.** Out of scope. M5 assumes one CAS+AC
   endpoint; multi-region replication is Buildbarn deployment
   concern.
5. **bzlmod vs WORKSPACE.** Default to bzlmod (`MODULE.bazel`
   extension). Ship a thin WORKSPACE shim only if a real consumer
   demands it.
6. **`do_not_cache` for failures?** Tier-1 failures (per-element
   converter errors) are deterministic outputs of a deterministic
   input — they belong in the cache same as successes. Tier-3
   infrastructure failures (CAS down, sandbox broken) are not
   reproducible from inputs and must NOT cache. The orchestrator
   sets `do_not_cache=true` only on Tier-3; Tier-1 and Tier-2
   results cache normally so re-runs produce the same diagnostic
   without burning local cmake time.

## What changes downstream

- **M3b is now a small delta:** the Action proto, the input root
  upload, the output materialization, and the ActionResult schema
  are all already in place. M3b's only change is replacing the
  `os/exec` call with `Execute(action_digest)` against Buildbarn's
  Execution service. The same Action that M5 used to write an AC
  entry is what M3b submits for remote execution. The same CAS
  blobs M5 uploaded as input root are what M3b's workers read.
- **M4's fingerprint registry is unaffected** — it reads
  `<out>/manifest/`, which is materialized regardless of whether
  conversion came from AC hit or local re-run.
- **Downstream Bazel projects** depending on FDSDK can switch from
  hand-written `cc_library` rules to the `converted_pkg_repo`
  extension, deleting their CMake-side build infra.
- **M3a's action-key cache is removed** — superseded by the AC
  flow. The `<out>/cache/actions/<key>/` directory tree goes away;
  `--cas=local:<path>` provides the same offline guarantee through
  a tiny local CAS+AC implementation.
