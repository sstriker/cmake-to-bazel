# cmake → Bazel conversion: known deltas

Empirically surfaced by the test corpus under
`converter/testdata/sample-projects/`. Each fixture's golden
records current converter behaviour; this doc captures the
real-world correctness gaps that the goldens accept today, so
they don't get lost as the corpus grows.

The corpus + this doc are the empirical alternative to design-
time correctness arguments — running cmake against a real
fixture surfaces the gaps that a hand-walk through lower / emit
won't find.

## Open deltas

### subdir-library — duplicate / over-broad include + hdr collection

**Fixture**: `converter/testdata/sample-projects/subdir-library/`
(top-level CMakeLists adds `src/util/` via `add_subdirectory`;
both define cc_library targets).

**Surfaced**:
- `cc_library(name = "toplib")` emits `includes = ["include", "include"]`
  — the same path repeated. The dedup happens neither in lower
  (which builds the IR-level include list) nor at emit time
  (which sorts but doesn't dedup).
- `cc_library(name = "util")` emits
  `hdrs = ["include/toplib.h", "include/util.h"]` — every `.h`
  file in the project, even though `util.c` only uses `util.h`.
  Header attribution is over-inclusive across multi-CMakeLists
  projects: the converter folds every header that any target's
  `target_include_directories` exposes into every cc_library's
  `hdrs`. Should partition by which target's include-dirs
  actually own each header path.

**Fix shape**: lower-pass dedup on the IR's per-target include
slice; lower-pass partition of headers by whose
target_include_directories declared the path. Both fixes are
local to `converter/internal/lower/`.

### configure-file — generated header dependency missing

**Fixture**: `converter/testdata/sample-projects/configure-file/`
(`configure_file(config.h.in config.h)` produces a build-tree
header; `cfglib.c` includes it).

**Surfaced**: the emitted `cc_library(name = "cfglib")` has
neither an `hdrs` reference to `config.h` nor a `deps` to a
genrule that produces it. Bazel-build of the converted output
would fail at compile time because the include path resolves to
nothing — Bazel doesn't know where `config.h` comes from.

**Fix shape**: detect `configure_file` invocations from the
codemodel's `cmakeFiles-v1` reply (or by scanning the build
directory for matching `.h` files with their source `.h.in`
template), emit a Bazel `genrule` that runs the same template
substitution (`@VAR@` → cmake-resolved value), and reference the
generated header in the cc_library's `hdrs`. Lives in
`converter/internal/lower/` + a new emit path for genrules of
this shape.

### find-package — only IMPORTED libraries with codemodel link
fragments fire the imports-manifest rewrite path

**Fixture**: `converter/testdata/sample-projects/find-package/`
(uses `find_package(ZLIB REQUIRED)` + a SHARED cc_library that
links against `ZLIB::ZLIB`; the imports manifest maps the link
path to a Bazel label).

**Surfaced**: ✓ works correctly for SHARED libraries (codemodel
records the link fragment for libz.so; lower matches against
imports.json's link_paths and rewrites to
`//elements/zlib:zlib`). But: STATIC libraries don't link
anything (they archive), so the codemodel has no link fragment
for them — which means `target_link_libraries(staticLib
SomeImport)` doesn't produce a dep edge in the converted
BUILD. Whatever consumer eventually links this static lib will
either get the dep transitively (if the consumer's codemodel
records it) or be missing it entirely.

**Fix shape**: walk the codemodel's `target.link.commandFragments`
for SHARED targets — already done — and additionally the
`target.dependencies` field for STATIC targets, which records
INTERFACE deps without surfacing them as link commands. Map
those through the imports manifest by CMake target name (not
just by link path).

## Resolved deltas

(none yet — initial seeding of the corpus)

## Adding a new fixture

1. Drop a cmake project under
   `converter/testdata/sample-projects/<name>/`.
2. `tools/fixtures/record-fileapi.sh <name>` records the
   File API reply into `converter/testdata/fileapi/<name>/`.
3. Run convert-element manually to produce the BUILD; compare
   against expectation. Pin as
   `converter/testdata/golden/<name>/BUILD.bazel.golden` either
   directly or via the test's `-update` flag.
4. Add a `TestEmit_<Name>_Golden` to
   `converter/internal/emit/bazel/emit_test.go` that loads the
   fixture + golden + asserts equivalence.
5. Document any surfaced gaps under "Open deltas" above.
