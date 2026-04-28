# Known fidelity deltas

Catalog of observed differences between cmake-built and converted-then-bazel-built
artifacts under the M5b fidelity gate (`make e2e-fidelity`). Each entry pairs the
fixture, the affected target, and the symptom; root-cause + status track the
converter bug it represents.

This file grows over time as the harness exercises real-world fixtures (Track 5).
Empty deltas list = converter is faithful for that fixture.

## hello-world (`converter/testdata/sample-projects/hello-world`)

Status: ✅ symbol-tier passes.

No known deltas. The single-file fixture exists to smoke-test the harness itself
rather than the converter; if hello-world ever fails, it's the harness, not the
converter.

## fmt (FMT_VERSION = 11.0.2, `make fetch-fmt`)

Status: 🔧 in-progress. The harness plumbing is wired (`TestE2E_Fidelity_Fmt_SymbolEquivalent`)
but the bazel-build step exercises converter surfaces hello-world doesn't (genex
resolution, multi-TU `cc_library`, `<INSTALL_INTERFACE:>` filtering). Each delta
the harness surfaces lands here as a discrete sub-section with reproducer +
triage notes.

### Format

When a delta is observed, append a section like:

```
### <Fixture> / <Target> / <symptom-tier>: <one-line summary>

**Reproducer**: which test, which target, what the diff looks like.
**Root cause**: which converter file/function emits the problematic shape.
**Status**: open | fix-in-progress | wontfix-with-rationale.
```

`SYM_LOST` and `SYM_NEW` mean "in cmake but not bazel" / "in bazel but not cmake"
in `fidelity.DiffSymbols.Format()` output.
