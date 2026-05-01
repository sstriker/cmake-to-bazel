package bazel_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/bazel"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/lower"
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

	// Every src reference should be a @src_abc123//: label.
	// The hello-world fixture has hello.c + include/hello.h.
	for _, want := range []string{
		`@src_abc123//:hello.c`,
		`@src_abc123//:include/hello.h`,
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
