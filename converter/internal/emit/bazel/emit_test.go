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
// Known delta captured by the golden: the generated config.h
// dependency isn't represented in the BUILD output. cfglib.c
// includes config.h, but the cc_library has neither an `hdrs`
// reference nor a `deps` to a genrule that produces it.
// Bazel-build of the converted output would fail because Bazel
// doesn't know where config.h comes from. Real fix: emit a
// genrule for configure_file (template substitution) + reference
// it in the cc_library's hdrs.
func TestEmit_ConfigureFile_Golden(t *testing.T) {
	src, err := filepath.Abs("../../../testdata/sample-projects/configure-file")
	if err != nil {
		t.Fatal(err)
	}
	r, err := fileapi.Load("../../../testdata/fileapi/configure-file")
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
