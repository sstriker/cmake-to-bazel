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

The bar: every fix that *removes* information from the converted
output (header narrowing, dep filtering, etc.) needs a high-
confidence signal — preferably from cmake's trace-expand record
of resolved command calls, never a basename-match heuristic.
Cosmetic-only deltas (information that's redundant but
correct) stay open as documentation rather than getting
heuristic fixes that introduce false-positive risk.

### subdir-library — over-broad hdr collection (cosmetic, not correctness)

**Fixture**: `converter/testdata/sample-projects/subdir-library/`
(top-level CMakeLists adds `src/util/` via `add_subdirectory`;
both define cc_library targets).

**Surfaced**:
`cc_library(name = "util")` emits
`hdrs = ["include/toplib.h", "include/util.h"]` — every `.h`
file in the project. The `target_include_directories(...
PUBLIC include)` from both CMakeLists declared `include/`, so
discoverHeaders' walk returns every header to every target.

**Why this is OK today**: Bazel allows the same file in
multiple cc_libraries' hdrs. The redundancy is cosmetic noise
in the BUILD output, not a build-time correctness issue —
both libraries compile and consumers link correctly.

**Why an early heuristic fix was reverted**: a basename-match
narrow ("drop `util.h` from toplib's hdrs because `util` is a
different target's name") false-positives on projects where a
header coincidentally shares a name with an unrelated target
(e.g. a `util` executable that has nothing to do with a
`util.h` header in a different library).

**Why we won't pursue the deterministic alternative**: scanning
source files for `#include "..."` directives is deterministic
(no name guessing), but expands the converter's action input
set to include every `.c` / `.cpp` source file it reads. That
means every source-file edit invalidates convert-element's
cache and triggers a re-run. The current behaviour (read only
the codemodel / cmakeFiles / compile_commands / build.ninja)
keeps convert-element re-runs gated on CMakeLists / cmake-cache
changes, which are rare. Trading rare re-runs for precise hdrs
isn't worth it — the hdrs duplication is a cosmetic BUILD-file
diff, not a build-time correctness issue.

This delta stays open as documentation; no fix is planned.

### configure-file — generated header dependency missing (correctness)

**Fixture**: `converter/testdata/sample-projects/configure-file/`
(`configure_file(config.h.in config.h)` produces a build-tree
header; `cfglib.c` includes it).

**Surfaced**: the emitted `cc_library(name = "cfglib")` has
neither an `hdrs` reference to `config.h` nor a `deps` to a
genrule that produces it. Bazel-build of the converted output
would fail at compile time because the include path resolves to
nothing — Bazel doesn't know where `config.h` comes from.

**Why heuristics aren't safe here**: cmakeFiles-v1's `inputs`
records `.in` files but doesn't record their output paths
(configure_file's `output` arg is arbitrary, not derivable
from the input name). Walking the build dir for files whose
basename matches an `.in` input's basename-without-`.in`
false-positives on unrelated build artifacts that happen to
share the name.

**Fix shape (high confidence)**: extend cmake's trace-expand
infrastructure (already wired for read-paths narrowing) to
parse `configure_file(<input> <output> ...)` calls. The trace
JSON records the literal arguments resolved to cmake's
variable scope — exactly the input/output pairing we need,
with no inference. With that pairing in hand:
1. Read the resolved generated file from the build dir.
2. Emit a Bazel genrule whose cmd writes the resolved bytes
   verbatim (snapshotting cmake-configure-time state).
3. Reference the genrule's output in the consuming
   cc_library's `hdrs`.

The trace-expand approach also closes the find-package STATIC
delta below — same parser, different command.

### multi-language — only first compile group's flags emitted (correctness)

**Fixture**: `converter/testdata/sample-projects/multi-language/`
(one cc_library with both `c_part.c` and `cxx_part.cpp` plus
per-language `target_compile_options($<COMPILE_LANGUAGE:C>:...)`
and `$<COMPILE_LANGUAGE:CXX>:...`).

**Surfaced**: emitted `cc_library` has
`copts = ["-O3", "-std=c11"]` — only the C compile group's
flags. The C++ compile group's `-std=c++17` is dropped because
lower assumes "at most one language per target" (per the
`cg := t.CompileGroups[0]` line in lowerTarget). Bazel-build
of the converted output would compile `cxx_part.cpp` with
`-std=c11`, which fails (C++ source compiled in C dialect).

**Fix shape**: split a multi-language cc_library into one
cc_library per language, link them via a third cc_library /
filegroup that groups them. cmake codemodel emits one
CompileGroup per language with the right per-language flags;
walk all CompileGroups (not just `[0]`) and emit one Bazel
target per language, with srcs partitioned by source extension
+ language. Each emitted target's `copts` carries that
language's flags. The original target's name aggregates them
via `deps = [":<name>_c", ":<name>_cxx"]` (or similar).

This is structural: changes the 1:1 cmake-target → Bazel-target
mapping. Defer until FDSDK actually surfaces multi-language
targets in the curated probe set (most kind:cmake elements are
single-language); track here so a future PR can pick it up.

### find-package STATIC — IMPORTED deps don't surface (correctness)

**Fixture**: `converter/testdata/sample-projects/find-package/`
(uses `find_package(ZLIB REQUIRED)` + a SHARED cc_library that
links against `ZLIB::ZLIB`; the imports manifest maps the link
path to a Bazel label).

**Surfaced**: ✓ works correctly for SHARED libraries (codemodel
records the link fragment for libz.so; lower matches against
imports.json's link_paths and rewrites to
`//elements/zlib:zlib`). But: STATIC libraries' codemodel has
empty `target.dependencies[]` AND empty `target.link.commandFragments[]`
(verified empirically against `add_library(t STATIC ...)
target_link_libraries(t PUBLIC ZLIB::ZLIB)`) — cmake doesn't
materialize the IMPORTED INTERFACE dep anywhere in the codemodel
for static targets, because no actual link happens at archive
time. So `target_link_libraries(staticLib SomeImport)`
doesn't produce a dep edge in the converted BUILD.

**Fix shape (high confidence)**: same trace-expand parser as
configure_file. cmake's trace records every
`target_link_libraries(<target> ... <ImportedTarget>)` call
with arguments resolved; lower can read those records to
surface IMPORTED-target deps that the codemodel drops on the
floor for static libs. Map each `<ImportedTarget>` through
`imports.LookupCMakeTarget` exactly like the existing dep-
resolution path does for SHARED targets.

## Resolved deltas

### subdir-library — includes dedup ✓

`converter/internal/lower/lower.go` now dedups the includes
slice at IR-build time (preserving order). Before:
`includes = ["include", "include"]` for a target whose own
`target_include_directories` named "include" plus a PUBLIC dep
that also named "include". After: `includes = ["include"]`.

High-confidence fix: identical-string dedup, no semantic call
required.

## Reverted heuristics

### subdir-library hdrs — target-name basename match (REVERTED)

A previous attempt narrowed each target's hdrs by dropping
headers whose basename matched a different target's name.
Reverted because: a project with a header coincidentally
matching an unrelated target's name (e.g. a `util` executable
plus a `util.h` for a different library) would silently lose
the header from the right target. The cosmetic redundancy is
preferable to a false-positive narrow.

The right narrow uses `#include` scanning of source files —
deterministic, no name guessing — but is deferred until an
FDSDK-shape fixture surfaces the cosmetic noise as actually
mattering.

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
