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

### find-package STATIC — IMPORTED deps don't surface

**Fixture**: `converter/testdata/sample-projects/find-package/`
(currently SHARED variant; STATIC variant of the same
`target_link_libraries(... ZLIB::ZLIB)` shape doesn't pick up
the dep).

**Surfaced**: For STATIC libs the codemodel's
`target.link.commandFragments` is empty (static libs archive
rather than link), so the imports-manifest rewrite path that
matches link-fragment paths against the manifest's `link_paths`
never fires. `target.dependencies[]` is also empty in the
codemodel for IMPORTED INTERFACE deps because cmake doesn't
materialize them as edges until something actually links.

**Fix shape**: parse cmake's `--trace-expand` output for
`target_link_libraries(<staticTarget> ... <ImportedTarget>)`
calls and surface each `<ImportedTarget>` as a dep edge
through the imports-manifest's `LookupCMakeTarget`. The
trace-expand infrastructure already exists for read-paths
narrowing; this would extend its consumer set.

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

### subdir-library — includes dedup ✓

`converter/internal/lower/lower.go` now dedups the includes
slice at IR-build time (preserving order). Before:
`includes = ["include", "include"]` for a target whose own
`target_include_directories` named "include" plus a PUBLIC dep
that also named "include". After: `includes = ["include"]`.

### subdir-library — hdrs partition by target-name ownership ✓

`filterHeadersByTargetOwnership` drops a header from a
target's hdrs when the header's basename-without-extension
matches a DIFFERENT in-codebase target's name. Before:
`util.c`'s cc_library emitted
`hdrs = ["include/toplib.h", "include/util.h"]` — every header
in the project, because both targets' PUBLIC
`target_include_directories(... include)` propagated through
discoverHeaders. After: `hdrs = ["include/util.h"]` for util,
`hdrs = ["include/toplib.h"]` for toplib.

Conservative narrow: only excludes when the basename match is
unambiguous. Headers without a target-name basename match stay
in every target's hdrs (the conservative-but-correct shape —
shared / interface headers without strong ownership signal
duplicate rather than disappear).

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
