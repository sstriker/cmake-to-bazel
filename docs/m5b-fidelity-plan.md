# M5b: Fidelity gate for converted-vs-cmake builds

## Context

M5 ships the Bazel envelope (`converted_pkg_repo` + `cc_toolchain` derivation) and `TestE2E_BazelBuild_DownstreamConsumesConvertedRepos` proves: cmake project → real `convert-element` → real `BUILD.bazel` → real `bazel build` → real binary, end-to-end. That test asserts the build **succeeds**. It does **not** assert the resulting artifact is **equivalent** to what cmake would have produced from the same source.

Fidelity validation: when the converter says a package converts, that has to mean the bazel-built artifact behaves the same as the cmake-built one. Otherwise "converts cleanly" is just "translation parses cleanly" — a far weaker claim.

## Scope

In scope (one architecture, one toolchain, one cmake project):

- A **fidelity test harness** that builds a sample cmake project two ways:
  1. `cmake -S … -B B-cmake && cmake --build B-cmake` (reference)
  2. `convert-element` + `bazel build` (converted)
  
  …and diffs the resulting artifacts.

- A **diff library** with three tiers, each progressively stricter:
  - **Symbol tier**: `nm --defined-only` symbol sets match across each library / executable. Catches missing translation units, mismatched flags that change inlining/exports, missing dependencies.
  - **Behavioral tier**: for runnable executables, run both binaries with the same input, compare exit code + stdout + stderr.
  - **Byte tier**: stripped binaries are byte-identical (path-stripped via `--strip-debug` + `--strip-unneeded`). Often won't hold (build-id, embedded paths in linker output) but documenting where it breaks is itself useful.

- An **e2e gate** wired into CI that runs the harness on `converter/testdata/sample-projects/hello-world` and asserts symbol-tier + behavioral-tier equivalence. Byte-tier is informational (test logs the diff but doesn't fail).

Out of scope:
- Cross-platform / cross-toolchain matrix (one cell only).
- Sanitizer / coverage variants (those flip exported symbols by design).
- Performance equivalence (different code generators emit different code; not a fidelity concern).
- Static-analysis equivalence (clang vs gcc, etc.).

## Architectural premise

Three things have to be true for fidelity to mean what we want:

1. **The same translation units are compiled.** If cmake builds `hello.c + extras.c → libhello.a` and the converter only emits `hello.c → libhello.a`, the symbol sets diverge. This is the cleanest signal: missing TUs surface as missing symbols.

2. **The same compile flags are applied to each TU.** Different `-D` definitions, different optimization levels, different `-fvisibility` — all change exports. Symbol-tier catches it.

3. **The link sets match.** A static-library consumer sees the union of symbols from each input; if `libhello.a` accidentally drops `gcc_s` from its `cc_library.deps`, the linker will fail at executable-link time but the library itself still builds. Behavioral-tier catches the executable-level breakage.

If those three hold for hello-world today, they're likely to hold for most real-world cmake projects. Failures on real projects then partition cleanly: some are converter bugs (missing TU), some are cmake-side oddities the converter rightfully refuses to emulate (custom build steps, in-source codegen the converter doesn't recover), some are toolchain mismatches (the `derive-toolchain` work covers these).

## Diff tiers in detail

### Symbol tier (`internal/fidelity/symbols.go`)

```
nm --defined-only --no-sort <artifact> | awk '{ print $3 }' | sort -u
```

We parse `nm`'s output ourselves (`<addr> <type> <name>` lines, `T`/`D`/`B` are defined symbols), produce a sorted set per artifact, diff via `(left - right, right - left)`. Dynamic libraries (`*.so`) and static libraries (`*.a`) are handled the same way — `nm` works on both.

Failure surfaces an operator-readable diff:

```
hello-world fidelity: symbol mismatch in libhello.a
  cmake-only:  hello_message
  bazel-only:  __wrap_hello_message
```

### Behavioral tier (`internal/fidelity/behavior.go`)

For each executable in the artifact set:

```
exit_a, stdout_a, stderr_a = run(<cmake-path>, args, stdin)
exit_b, stdout_b, stderr_b = run(<bazel-path>, args, stdin)
```

…with the same `args` + `stdin`. Compare all three. Test fixtures supply (args, stdin, expected-output-pattern); the harness asserts both invocations match the expected pattern AND match each other.

Default fixture for hello-world: no args, no stdin, expect `Hello, World!\n` on stdout. Both binaries must produce it.

### Byte tier (`internal/fidelity/bytes.go`)

```
strip --strip-debug --strip-unneeded <a> -o <a-stripped>
strip --strip-debug --strip-unneeded <b> -o <b-stripped>
sha256sum <a-stripped> <b-stripped>
```

Logs the digests + the diff cause (build-id, embedded path, …). Does not assert; informational.

## Step plan with timing

| # | Step | Days | Days (risk-adj) |
|---|---|---:|---:|
| 1 | docs/m5b-fidelity-plan.md (this) | 0.25 | 0.25 |
| 2 | `internal/fidelity/symbols.go` + tests against fixture nm output | 0.5 | 1 |
| 3 | `internal/fidelity/behavior.go` (exec + diff) | 0.5 | 1 |
| 4 | `internal/fidelity/bytes.go` (strip + sha256 diff, informational) | 0.25 | 0.5 |
| 5 | e2e harness that drives both build paths against hello-world | 1 | 1.5 |
| 6 | CI wiring + `make e2e-fidelity` | 0.25 | 0.5 |
| 7 | Document expected long-tail divergences (build-id, RPATH, …) | 0.25 | 0.5 |
| | **Total** | **3** | **5.25** |

## Critical files

```
docs/
  m5b-fidelity-plan.md              # this plan
internal/
  fidelity/
    symbols.go                      # nm parser + symbol-set diff
    symbols_test.go
    behavior.go                     # exec + (exit, stdout, stderr) diff
    behavior_test.go
    bytes.go                        # strip + sha256 diff (informational)
    artifacts.go                    # walk + classify (.a / .so / executable)
orchestrator/
  internal/orchestrator/
    fidelity_e2e_test.go            # build tag e2e; drives both paths
```

## Acceptance gate

> `make e2e-fidelity` (build tag `e2e`) builds `hello-world` two ways and asserts:
>
>   - Same set of artifact files at the bazel output and the cmake output.
>   - For each library, identical `nm --defined-only` symbol set.
>   - For each executable, identical (exit_code, stdout, stderr) under the test fixture's input.
>
> Byte-tier diff is logged but not asserted.

If the gate fires, either the converter dropped a translation unit or applied different compile flags than cmake; the diff output names the artifact and the missing/extra symbols.

## Why this is M5b

The original M1 plan called out an architectural keystone test: "the synthesized libdrm bundle drop-in test". That was a fidelity check at a different layer (cmake-consumer-resolves-the-bundle), and it landed as `TestE2E_CMakeConsumer`. The artifact-equivalence layer never got a corresponding gate. M5b retroactively closes that gap.

We label this M5b (not M5.x or M5+) because:
- It's the milestone that makes "converts cleanly" actually mean something (artifact equivalence vs. translation-parses-cleanly).
- It's post-M5 (the bazel envelope must already work end-to-end, which it does).
- It doesn't change the architectural surface — pure observability.

## Open questions

1. **What's the right reference for "cmake-built artifact"?** A clean `cmake … && cmake --build` of the upstream sources, with the same toolchain `derive-toolchain` produced. Operators who want to validate a different reference (e.g. an upstream's official binary release) can substitute.

2. **What if cmake-built and bazel-built differ in expected ways?** Some divergences are correct (Bazel uses `cc_library` with `linkstatic = True` by default, which emits archives differently than cmake's `add_library`). We document each known-acceptable divergence in `docs/m5b-fidelity-acceptable-deltas.md` (queued).

3. **Does this generalize to fmt / spdlog / libdrm-equivalent?** Yes — the harness takes any cmake project as a parameter. Wiring those as additional CI gates is post-this-plan; the architectural claim is that hello-world's symbol-tier match implies the converter's translation logic is faithful for the same general shape of project.

4. **What about cmake projects with codegen?** Recovered via the converter's `add_custom_command` codegen path (M2), but the codegen output's content is what determines whether the resulting binaries match. If the converter recovers the codegen tag correctly but the genrule produces different bytes than cmake's `add_custom_command`, symbol-tier catches it. Real test target: an fmt-class project with non-trivial codegen.

## What this enables downstream

- **"Converts cleanly" gains semantic content**: it now implies "produces equivalent artifacts" up to the documented deltas, instead of "translation parses cleanly".
- **Regression detection has teeth**: M4's fingerprint history can include a fidelity-pass-rate metric per element. A converter change that flips fidelity (without flipping exit-code success) gets caught.
- **Triage signal**: when the gate fires on a real package, the symbol diff tells operators which translation unit is missing or which flag drifted.
