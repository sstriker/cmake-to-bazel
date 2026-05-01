package bazel_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/bazel"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/lower"
	"github.com/sstriker/cmake-to-bazel/internal/manifest"
)

var update = flag.Bool("update", false, "overwrite *.golden files instead of comparing")

func TestEmit_HelloWorld_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/hello-world")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/hello-world")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatalf("ToIR: %v", err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Scrub the absolute SourceRoot from the header so the golden is
	// machine-portable. Emit writes "Source: <abs path>" in the header
	// comment; replace it with a stable token before comparison.
	got = scrubSourceLine(got, src)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "hello-world", "BUILD.bazel.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update?): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestEmit_SubdirLibrary_Golden exercises the multi-CMakeLists.txt
// shape: top-level CMakeLists pulls in src/util/CMakeLists.txt via
// add_subdirectory; both define cc_library targets. The codemodel
// reply has two targets defined across two source files; the
// emitter should produce one cc_library per target without
// flattening or losing the toplib→util dep edge.
//
// Known deltas captured by the golden (recorded as bugs to fix in
// follow-ups; the golden documents current behaviour so future
// converter changes against this fixture surface as visible diffs):
//   - `includes = ["include", "include"]` on toplib has duplicate
//     entries when a target's include path is repeated by both its
//     own target_include_directories and a transitive PUBLIC dep.
//     Should dedupe at IR-build time.
//   - `hdrs` on both targets enumerates every .h file in the
//     project rather than partitioning by which target's
//     target_include_directories owns the path. Hdrs detection is
//     over-inclusive across a multi-CMakeLists project.
func TestEmit_SubdirLibrary_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/subdir-library")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/subdir-library")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatalf("ToIR: %v", err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got = scrubSourceLine(got, src)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "subdir-library", "BUILD.bazel.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update?): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// scrubSourceLine replaces any literal occurrence of the absolute source root
// with the token <SOURCE_ROOT>. That's enough to make the header line stable
// across machines; Emit does not embed src elsewhere in M1.
func scrubSourceLine(b []byte, src string) []byte {
	abs := []byte(src)
	tok := []byte("<SOURCE_ROOT>")
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if i+len(abs) <= len(b) && string(b[i:i+len(abs)]) == string(abs) {
			out = append(out, tok...)
			i += len(abs)
			continue
		}
		out = append(out, b[i])
		i++
	}
	return out
}

// TestEmit_WithSourceKey_PrefixesLabels asserts the FUSE-sources
// emit path: when Options.SourceKey is set, every src/hdr in
// emitted cc_library/cc_binary/cc_test rules is prefixed with
// @src_<key>//: so project B's compile actions reference source
// bytes by digest-stable Bazel label rather than by relative
// filesystem path.
func TestEmit_WithSourceKey_PrefixesLabels(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.EmitWithOptions(pkg, bazel.Options{SourceKey: "abc123"})
	if err != nil {
		t.Fatalf("EmitWithOptions: %v", err)
	}
	body := string(got)

	// Every src reference should be a @src_abc123//:tree_dir/<path>
	// label (matching the repo rule's tree_dir/ layout). The
	// hello-world fixture has hello.c + include/hello.h.
	for _, want := range []string{
		`@src_abc123//:tree_dir/hello.c`,
		`@src_abc123//:tree_dir/include/hello.h`,
	} {
		if !contains(body, want) {
			t.Errorf("emitted BUILD missing %q; got:\n%s", want, body)
		}
	}
	// Sanity check: no bare unprefixed src filenames leaked from
	// the legacy path.
	if contains(body, `srcs = ["hello.c"]`) {
		t.Errorf("emitted BUILD has bare hello.c reference (legacy path); got:\n%s", body)
	}
}

// TestEmit_NoSourceKey_PreservesLegacyPaths asserts the default
// emit path (no SourceKey) emits relative paths as before — a
// regression guard against the new option leaking into the
// existing test fixtures.
func TestEmit_NoSourceKey_PreservesLegacyPaths(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	body := string(got)
	if contains(body, "@src_") {
		t.Errorf("legacy emit (no SourceKey) should not produce @src_ references; got:\n%s", body)
	}
}

// contains is a tiny strings.Contains alias kept local so the
// import set stays minimal.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestEmit_ConfigureFile_Golden exercises the cmake configure_file
// shape: a .h.in template gets expanded to a build-tree config.h
// at cmake-configure time. The cc_library compiles a .c that
// includes the generated header.
//
// Lower's recovery path: trace records configure_file calls'
// (input, output) pairs; the recording script stashes the
// rendered output bytes in the fixture mirroring the build-dir
// layout. lower reads those bytes, emits a genrule that
// base64-decodes them at Bazel build time, and attaches the
// output to the consuming cc_library's hdrs (matched by the
// target's codemodel-recorded build-dir include).
func TestEmit_ConfigureFile_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/configure-file")
	if err != nil {
		t.Fatal(err)
	}
	replyDir, err := filepath.Abs("../../../testdata/fileapi/configure-file")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load(replyDir)
	if err != nil {
		t.Fatal(err)
	}
	traceRaw, err := os.ReadFile(filepath.Join(replyDir, "trace.jsonl"))
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{
		HostSourceRoot: src,
		BuildDir:       replyDir,
		TraceRaw:       traceRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got = scrubSourceLine(got, src)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "configure-file", "BUILD.bazel.golden")
	if *update {
		_ = os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestEmit_MultiTarget_Golden exercises the cc_library / cc_binary
// rule-kind dispatch + the static / shared distinction. Three
// targets in one project: a STATIC library (linkstatic = True), a
// SHARED library (no linkstatic; -fPIC + <name>_EXPORTS define), a
// binary (cc_binary, hdrs folded into srcs per Bazel 9). All emit
// in one BUILD with the right rule kind per target.
func TestEmit_MultiTarget_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/multi-target")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/multi-target")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got = scrubSourceLine(got, src)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "multi-target", "BUILD.bazel.golden")
	if *update {
		_ = os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestEmit_FindPackage_Golden exercises the imports-manifest
// rewrite path: cmake's find_package(ZLIB) + target_link_libraries
// with ZLIB::ZLIB produces a codemodel link fragment for the
// system libz path; the imports manifest maps that to a Bazel
// label, and the emitter substitutes the link path with the
// label in the cc_library's deps. Confirms the synth-prefix /
// imports-manifest plumbing surfaces real out-of-tree deps as
// stable Bazel labels rather than absolute /usr/lib paths.
func TestEmit_FindPackage_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/find-package")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/find-package")
	if err != nil {
		t.Fatal(err)
	}
	imports, err := manifest.Load(filepath.Join(src, "imports.json"))
	if err != nil {
		t.Fatalf("load imports manifest: %v", err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src, Imports: imports})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got = scrubSourceLine(got, src)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "find-package", "BUILD.bazel.golden")
	if *update {
		_ = os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestEmit_FindPackageStatic_Golden exercises the STATIC
// IMPORTED-dep recovery path. For STATIC archives cmake's
// codemodel records no `dependencies` and no Link
// (no link step for an .a), so an IMPORTED target like
// ZLIB::ZLIB used via target_link_libraries is invisible
// from the codemodel alone. Lower's STATIC fallback
// consults the trace's target_link_libraries call to
// surface the dep.
func TestEmit_FindPackageStatic_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/find-package-static")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/find-package-static")
	if err != nil {
		t.Fatal(err)
	}
	imports, err := manifest.Load(filepath.Join(src, "imports.json"))
	if err != nil {
		t.Fatalf("load imports manifest: %v", err)
	}
	traceRaw, err := os.ReadFile("../../../testdata/fileapi/find-package-static/trace.jsonl")
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src, Imports: imports, TraceRaw: traceRaw})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got = scrubSourceLine(got, src)

	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "find-package-static", "BUILD.bazel.golden")
	if *update {
		_ = os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestEmit_GeneratorExpressions_Golden exercises cmake's $<...>
// generator expressions. The codemodel resolves them at
// configure time, so what surfaces in CompileGroups[].Includes
// / Defines / Compile-fragments is the resolved-for-this-config
// values, not generator-expression literals. Confirms
// convert-element doesn't trip on the expressions and emits
// the resolved values cleanly. Known clean — no gap.
func TestEmit_GeneratorExpressions_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/generator-expressions")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/generator-expressions")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got = scrubSourceLine(got, src)
	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "generator-expressions", "BUILD.bazel.golden")
	if *update {
		_ = os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestEmit_MultiLanguage_Golden exercises C+C++ in a single
// cc_library target. cmake codemodel emits one CompileGroup per
// language; lower's "at most one language per target"
// assumption (cg := t.CompileGroups[0]) drops the second
// language's flags entirely.
//
// Known delta captured by the golden:
//   - copts emitted = first compile group's only.
//     `cxx_part.cpp` would be compiled with `-std=c11` (the C
//     std flag), failing as C++ in C dialect.
//
// Fix shape (deferred): split multi-language targets into one
// cc_library per language. See docs/cmake-conversion-deltas.md.
func TestEmit_MultiLanguage_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/multi-language")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/multi-language")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got = scrubSourceLine(got, src)
	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "multi-language", "BUILD.bazel.golden")
	if *update {
		_ = os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestEmit_Visibility_Golden exercises target_include_directories'
// PUBLIC vs PRIVATE distinction. The codemodel doesn't tag
// individual include entries with visibility — both arms flatten
// into compileGroups[].includes[]. lower recovers the keyword
// arms from cmake's --trace-expand output (parsed in
// internal/shadow). PUBLIC dirs flow into cc_library.includes
// (consumer-visible); PRIVATE dirs flow into copts as
// `-I<dir>` (compile-only, not propagated). PRIVATE-only
// headers don't surface in `hdrs` because discoverHeaders
// only walks the public include set.
func TestEmit_Visibility_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/visibility")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/visibility")
	if err != nil {
		t.Fatal(err)
	}
	traceRaw, err := os.ReadFile("../../../testdata/fileapi/visibility/trace.jsonl")
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	pkg, err := lower.ToIR(r, nil, lower.Options{HostSourceRoot: src, TraceRaw: traceRaw})
	if err != nil {
		t.Fatal(err)
	}
	got, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got = scrubSourceLine(got, src)
	goldenPath := filepath.Join("..", "..", "..", "testdata", "golden", "visibility", "BUILD.bazel.golden")
	if *update {
		_ = os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("BUILD.bazel mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
