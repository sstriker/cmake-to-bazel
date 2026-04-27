# Toolchain milestone: derive Bazel toolchain + platform definitions from cmake

## Context

Bazel users today hand-write `cc_toolchain` / `platform` / `constraint_value`
definitions. For a multi-element project conversion this scales badly: every
cmake project is happy to discover its toolchain via cmake's auto-detection
(or a `CMAKE_TOOLCHAIN_FILE` override), but the converted Bazel build needs
the same toolchain expressed as Bazel rules. Mismatches → non-byte-equivalent
outputs and divergent test pass rates.

This milestone closes that loop: rather than hand-writing toolchain rules,
we **probe** cmake — run a tiny C/C++ project through `cmake -S … -B …`
with each variant we care about — and **derive** Bazel toolchain rules
from cmake's File API output.

## Scope

In scope:

- A `derive-toolchain` binary that takes a probe-project source + a list of
  variants and emits Bazel `cc_toolchain` / `platform` / `constraint_value`
  rules.
- Probe-project fixture (a 2-file C/C++ project that exercises every
  toolchain query path the orchestrator's converted elements would hit).
- Variant matrix: at minimum {Debug, Release, RelWithDebInfo, MinSizeRel},
  optionally cross-compile via `--toolchain-file`.
- Per-language coverage: C, C++. Other languages (Fortran, ASM) plumb
  through but aren't first-class until a real FDSDK element forces them in.

Out of scope (queued for follow-ups):

- Sanitizer / coverage / LTO toolchain features. Same approach (additional
  variants), but the matrix grows and the feature-mapping logic is its
  own design surface.
- Auto-generated `MODULE.bazel` integration. We emit standalone rules;
  consumers wire them into their module manually.
- Capability detection via `CMAKE_REQUIRED_LIBRARIES` / `try_compile`.
  The orchestrator's converted elements already encode their actual
  link deps; the toolchain probe doesn't need to predict capabilities.

## Architectural premise

cmake's File API exposes everything needed:

- **toolchains-v1**: per-language `compiler.path`, `version`,
  `compiler.implicit.includeDirectories`, `compiler.implicit.linkDirectories`,
  `compiler.implicit.linkFrameworkDirectories`, `target` (architecture
  triple).
- **cache-v2**: every CMake cache variable, including `CMAKE_<LANG>_FLAGS`,
  `CMAKE_<LANG>_FLAGS_DEBUG`, `CMAKE_<LANG>_FLAGS_RELEASE`, `CMAKE_AR`,
  `CMAKE_STRIP`, `CMAKE_LINKER`, `CMAKE_HOST_SYSTEM_NAME`,
  `CMAKE_HOST_SYSTEM_PROCESSOR`, `CMAKE_SYSTEM_NAME`,
  `CMAKE_SYSTEM_PROCESSOR`.
- **codemodel-v2**: per-target compile/link command fragments, which let
  us cross-check that the flags we extract match what cmake actually
  invokes.

The variant matrix isolates "what changes per build type":
running cmake N times with `CMAKE_BUILD_TYPE=<v>` gives N flag sets;
diffing against a no-build-type baseline yields the per-variant deltas
that map to Bazel `compilation_mode`.

## Pipeline

```
   probe-project (small C/C++ source)
        │
        ▼
   variants list  ──►  toolchain.Probe
        │             ├── cmake -S/-B per variant (real cmake under bwrap)
        │             ├── fileapi.Load          (already in converter/)
        │             └── extract.FromReply    (NEW)
        ▼
   toolchain.Model  (Go struct)
        │
        ▼
   emit/bazeltoolchain.Emit (NEW)
        │
        ▼
   BUILD.bazel + cc_toolchain_config.bzl
```

Each stage independently testable:

- `Probe` mockable via the existing fileapi pre-recorded fixtures (no
  cmake required for unit tests, same pattern the converter uses).
- `Extract` is a pure function over `fileapi.Reply` + `fileapi.Cache`.
- `Emit` is a pure function over `Model` → bytes; golden-tested.

## Bazel output shape

Per probe run (one per platform), emit:

```
// platform.bzl
platform(
    name = "linux_x86_64",
    constraint_values = [
        "@platforms//os:linux",
        "@platforms//cpu:x86_64",
    ],
)

// cc_toolchain.bzl
load("@bazel_tools//tools/cpp:cc_toolchain_config_lib.bzl", ...)

cc_toolchain_config(
    name = "linux_x86_64_cc_config",
    compiler = "gcc",                  // from fileapi.Toolchains.Compiler.Id
    target_cpu = "x86_64",
    target_libc = "glibc",             // from CMAKE_SYSTEM_NAME + heuristic
    tool_paths = {
        "gcc":     "/usr/bin/gcc",     // from fileapi.Toolchains.Compiler.Path
        "ld":      "/usr/bin/ld",      // from CMAKE_LINKER
        "ar":      "/usr/bin/ar",      // from CMAKE_AR
        "cpp":     "/usr/bin/cpp",
        "nm":      "/usr/bin/nm",
        "objcopy": "/usr/bin/objcopy",
        "objdump": "/usr/bin/objdump",
        "strip":   "/usr/bin/strip",
    },
    cxx_builtin_include_directories = [...],   // implicit.includeDirectories
    compile_flags = [...],                     // CMAKE_<LANG>_FLAGS
    dbg_compile_flags = [...],                 // CMAKE_<LANG>_FLAGS_DEBUG
    opt_compile_flags = [...],                 // CMAKE_<LANG>_FLAGS_RELEASE
    link_flags = [...],
    dbg_link_flags = [...],
    opt_link_flags = [...],
)

cc_toolchain(
    name = "linux_x86_64_cc",
    toolchain_config = ":linux_x86_64_cc_config",
    ...
)

toolchain(
    name = "linux_x86_64_cc_toolchain",
    target_compatible_with = [":linux_x86_64"],
    toolchain = ":linux_x86_64_cc",
    toolchain_type = "@bazel_tools//tools/cpp:toolchain_type",
)
```

Per platform-pair (cross-compile), emit one set per (host, target).

## Step plan with timing

| # | Step | Days | Days (risk-adj) |
|---|---|---:|---:|
| 1 | docs/toolchain-derivation-plan.md (this) + probe-project fixture | 0.5 | 0.5 |
| 2 | `internal/toolchain/extract.go`: fileapi.Reply -> Model (pure fn, fixture-tested) | 1 | 1.5 |
| 3 | `internal/toolchain/probe.go`: cmake variant-loop wrapper around cmakerun.Configure | 1 | 1.5 |
| 4 | `internal/toolchain/diff.go`: derive per-variant flag deltas from N variants | 1 | 1.5 |
| 5 | `internal/emit/bazeltoolchain/emit.go`: Model -> BUILD.bazel + .bzl files; golden-tested | 1.5 | 2 |
| 6 | `cmd/derive-toolchain`: CLI wrapping all of the above | 0.5 | 1 |
| 7 | e2e: derive against host gcc, build a downstream cc_binary using the emitted toolchain | 1 | 1.5 |
| | **Total** | **6.5** | **9.5** |

## Critical files

```
docs/
  toolchain-derivation-plan.md         # this plan
converter/
  cmd/derive-toolchain/main.go         # CLI
  internal/toolchain/
    types.go                           # Model, Variant, Platform, ...
    extract.go                         # fileapi.Reply -> Model
    probe.go                           # variant loop, drives cmakerun
    diff.go                            # per-variant flag deltas
  internal/emit/bazeltoolchain/
    emit.go                            # Model -> BUILD.bazel + .bzl
  testdata/
    toolchain-probe/
      CMakeLists.txt                   # 2-file C/C++ probe project
      probe.c
      probe.cpp
    fileapi/toolchain-probe-{debug,release}/  # pre-recorded variant replies
    golden/toolchain-probe/
      BUILD.bazel.golden
      cc_toolchain_config.bzl.golden
```

## Open questions

1. **target_libc detection.** cmake doesn't directly expose libc identity;
   we'd infer from `CMAKE_SYSTEM_NAME=Linux` + presence of `/usr/include/x86_64-linux-gnu`
   etc. Document the heuristic; expose `--target-libc` as an override.

2. **How many variants is the right matrix?** Debug + Release covers 95%
   of FDSDK; RelWithDebInfo + MinSizeRel are the long tail. Default to all
   four; let `--variant=...` shrink for speed.

3. **Cross-compile.** First milestone targets host-only (CMAKE_TOOLCHAIN_FILE
   not used). Cross-compile flow uses the same probe code with a different
   toolchain file passed in; queued as the obvious follow-up.

4. **Validation against hand-written toolchains.** Once we've derived a
   linux/gcc toolchain, sanity-check that a downstream `bazel build
   //:smoke` against it reaches `gcc` correctly and produces a working
   binary. The e2e gate (step 7).

## What this enables downstream

- **M5+M3b deployments** can derive their worker pool's toolchain definitions
  programmatically instead of hand-writing them — each worker image has cmake
  installed, run `derive-toolchain` once at build time.
