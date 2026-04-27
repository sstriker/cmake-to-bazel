# M5: Bazel envelope + REAPI CAS substrate

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
2. **REAPI as the cache substrate.** M3a's action-key cache is a local
   filesystem directory; that's enough for one machine, but every CI runner
   redoes work. M5 swaps the local action-key store for a **REAPI ContentAddressableStorage**
   client, so independent converter instances (laptop, CI, distro builders)
   share cache hits via a common Buildbarn CAS. This is **caching/distribution
   only**, not action submission — M3b remains queued and adds remote *execution*
   on top of the same CAS.

The split matters: REAPI CAS is a low-risk, high-payoff plumbing change (one
gRPC client, one hash on the existing action-key, deterministic upload of the
output tree). REAPI Action submission (M3b) is a much bigger lift: it needs
the converter binary itself uploaded as an input root, sandboxed via Buildbarn
workers, with every cmake/ninja/bwrap dependency content-addressed. M5
delivers shared caching today and lets M3b plug in later without re-plumbing
the cache layer.

## Key decisions

- **REAPI CAS only, not Execution, in M5.** The orchestrator continues to
  drive `os/exec` of the converter locally. After each successful conversion,
  the output tree (`BUILD.bazel`, `bundle/`, `manifest.json`, `read_paths.json`,
  `failure.json` if applicable) is uploaded to CAS keyed by the M3a action-key.
  Before each conversion, the orchestrator queries CAS for the action-key
  digest and on hit, materializes the tree locally and skips the subprocess.
  Buildbarn-side this is `FindMissingBlobs` + `BatchReadBlobs`/`Read` against
  the standard `build.bazel.remote.execution.v2.ContentAddressableStorage`
  service. No Execution service touched.
- **Action-key as the CAS index.** M3a's action-key (sha256 over shadow root
  + imports + prefix + converter binary digest) is already a content hash;
  reuse it verbatim as the CAS lookup key. The mapping
  `action-key → output-tree-digest` lives in a small versioned manifest blob
  (also stored in CAS), keyed deterministically — `sha256("convert-element-v1::"
  + action-key)` — so any client can derive it without a sidecar service.
- **`AC` (Action Cache) optional, not required.** REAPI splits storage into
  CAS (raw blobs) and AC (action digest → ActionResult). Using AC would be
  cleaner long-term but couples us to REAPI Action protobuf shapes, which
  M3b will rework. M5 stays in CAS-only mode; M3b will migrate to AC at the
  same time it starts submitting Actions.
- **Deterministic output-tree packing.** The converter's output dir is
  packed into a Merkle tree the same way Bazel does it: directories become
  `Directory` protos (sorted by name), files become `FileNode` (digest +
  is_executable bit). Same Tree digest format as REAPI Tree messages. This
  means a future M3b can lift the same packing code unchanged.
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
| 1 | docs/m5-plan.md + cas-protocol.md (this) | 0.5 | 0.5 |
| 2 | `internal/cas/store.go`: Store interface + local impl matching M3a's behavior | 0.5 | 1 |
| 3 | `internal/cas/grpc.go`: REAPI CAS client (FindMissingBlobs, BatchUpdate/BatchRead, Read for big blobs) | 1.5 | 2.5 |
| 4 | `internal/cas/tree.go`: deterministic Merkle packing of converter output dirs | 1 | 1.5 |
| 5 | Wire orchestrator action-key cache through `cas.Store`; `--cas=local:...` and `--cas=grpc://...` flags | 1 | 1.5 |
| 6 | Two-orchestrator cache-share e2e: machine A converts, machine B (clean) hits CAS, byte-identical outputs | 1 | 1.5 |
| 7 | `bazel/converted_pkg_repo.bzl` module extension + MODULE.bazel template | 1 | 1.5 |
| 8 | Downstream Bazel-build acceptance gate: `bazel build @libdrm//:libdrm` against converted FDSDK subset | 1.5 | 2 |
| 9 | CMake-side consumer test: downstream `find_package(libdrm)` against the per-repo bundle | 0.5 | 1 |
| | **Total** | **8.5** | **13** |

## Critical files

```
internal/
  cas/
    store.go                  # Store interface: Get(digest) / Put(blob) / GetTree(rootDigest) / PutTree(dir)
    local.go                  # filesystem impl, drop-in for M3a's <out>/cache/actions/<key>/
    grpc.go                   # REAPI client: FindMissingBlobs / BatchRead / BatchUpdate / streaming Read+Write
    tree.go                   # deterministic Directory/FileNode packing; matches REAPI Tree message
    digest.go                 # sha256 + size; Digest type aliased to remoteexecution.Digest
orchestrator/
  internal/
    actionkey/cas_bridge.go   # adapter: action-key store reads/writes through cas.Store
    orchestrator/run.go       # pass --cas flag through to actionkey
  cmd/orchestrate/main.go     # add --cas, --cas-tls-cert, --cas-tls-key, --cas-token-file
bazel/
  converted_pkg_repo.bzl      # Bzlmod module extension: reads converted.json, declares one local_repository per elem
  MODULE.bazel.template       # bazel_dep on toolchain rules + module_extension hookup
testdata/
  bazel/downstream/           # MODULE.bazel + BUILD.bazel for the downstream consumer used in step 8
docs/
  m5-plan.md                  # this plan
  cas-protocol.md             # action-key → output-tree-digest mapping format, versioned
```

## Output shape

### CAS-side: action-key index blob

For each successfully-cached action-key, the orchestrator stores a small
JSON blob in CAS keyed by `sha256("convert-element-v1::" + action-key)`:

```json
{
  "version": 1,
  "schema": "convert-element-v1",
  "action_key": "<sha256 hex>",
  "output_tree": {
    "root_digest": {"hash": "...", "size_bytes": 1234},
    "size_bytes_total": 56789
  },
  "converter_binary": {"hash": "...", "size_bytes": 7890123},
  "produced_at": "2026-04-27T12:00:00Z"
}
```

The `output_tree.root_digest` points at a REAPI `Tree` message describing
the converter's output dir. Materializing it back to a local path is a
straight Tree-walk: read each FileNode, fetch the blob, write at the
recorded relative path.

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
   exact byte digest a parallel `bazel build`-internal packer would
   produce (compare against a checked-in golden REAPI Tree proto).
2. **Local-CAS regression:** M3a's existing determinism test re-runs
   against `--cas=local:<tmpdir>` and the action-key cache behavior
   matches verbatim — same skip / re-run pattern, same outputs.
3. **gRPC integration:** spin up Buildbarn-style CAS in CI (Docker
   compose with `bb_storage`); orchestrator runs with
   `--cas=grpc://localhost:8980`, completes a small FDSDK subset, then
   a second orchestrator on a clean tmpdir hits 100% CAS on identical
   inputs and produces byte-identical outputs.
4. **Cache-share keystone test:** machine A converts FDSDK subset and
   uploads to shared CAS; machine B with no `<out>/cache/` and no local
   converter binary (just the orchestrator and the gRPC endpoint) hits
   CAS for every element, materializes outputs, and the resulting
   `manifest/converted.json` is byte-identical to A's. This is the
   architectural claim — independent instances share work.
5. **Bazel-build downstream gate:** `bazel build @libdrm//:libdrm`
   from `testdata/bazel/downstream/` succeeds against the converted
   FDSDK subset, with the `converted_pkg_repo` extension wiring all
   `local_repository` declarations from `converted.json`.
6. **CMake-consumer drop-in:** an unrelated downstream CMake project
   sets `CMAKE_PREFIX_PATH=$BAZEL_OUT/external/libdrm/cmake/libdrm`,
   does `find_package(libdrm REQUIRED)`, configures successfully.
7. **Cache-corruption resilience:** a malformed CAS blob (manually
   edited mid-test) produces a Tier-3 infrastructure failure with a
   clear "CAS digest mismatch" message, not silent breakage. The
   orchestrator falls through to local re-conversion.

## Open questions

1. **Compression on the wire.** REAPI supports zstd-compressed blobs via
   `Compressor`; converter outputs are mostly small text. Default to
   uncompressed for M5 (simpler debug); add zstd opt-in once the e2e
   numbers show it would matter.
2. **Cache eviction.** M5 doesn't manage CAS retention; that's
   Buildbarn's policy. The orchestrator does need a graceful "blob
   missing" path — treat as cache miss, re-run locally, re-upload.
   Documented as the resilience case in verification step 7.
3. **Converter binary digest.** M3a's action-key already includes
   `sha256(converter binary)`. M5 uploads the binary itself to CAS
   on first use so M3b can later submit it as an input root without
   a separate publish step. Cheap insurance.
4. **Multi-region CAS.** Out of scope. M5 assumes one CAS endpoint;
   multi-region replication is Buildbarn deployment concern.
5. **bzlmod vs WORKSPACE.** Default to bzlmod (`MODULE.bazel`
   extension). Ship a thin WORKSPACE shim only if a real consumer
   demands it.

## What changes downstream

- M3b plugs in **on top of** M5: when remote execution lands, the
  orchestrator submits Actions whose input root references blobs
  M5 already uploaded (converter binary, shadow tree, imports
  manifest, prefix tree). The output tree comes back via the same
  CAS layer M5 just wired. No re-plumbing.
- M4's fingerprint registry is unaffected — it reads
  `<out>/manifest/`, which is materialized regardless of whether
  conversion came from CAS or from local re-run.
- Downstream Bazel projects depending on FDSDK can switch from
  hand-written `cc_library` rules to the `converted_pkg_repo`
  extension, deleting their CMake-side build infra.
