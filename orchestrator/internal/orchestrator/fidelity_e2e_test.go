//go:build e2e

// fidelity_e2e_test is the M5b architectural keystone: build
// hello-world two ways (cmake vs convert-element + bazel build)
// and assert the resulting libraries' `nm --defined-only` symbol
// sets are equivalent.
//
// Why this test matters: TestE2E_BazelBuild_DownstreamConsumesConvertedRepos
// proves the conversion plumbing works end-to-end (bazel build
// succeeds), but says nothing about whether the bazel-built
// artifact is faithful to what cmake would have produced. Without
// fidelity validation, "converts cleanly" just means "translation
// parses cleanly", not "produces correct binaries".
//
// Symbol-tier is the load-bearing assertion: same set of defined
// symbols means same translation units compiled with effectively-
// equivalent flags. Behavioral-tier (hello-world is library-only,
// no executable) is queued for a future fixture.
//
// Gated behind the existing `e2e` build tag; depends on real
// cmake + bazel/bazelisk + convert-element + nm.
package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sstriker/cmake-to-bazel/internal/fidelity"
)

func TestE2E_Fidelity_HelloWorld_SymbolEquivalent(t *testing.T) {
	if _, err := exec.LookPath("nm"); err != nil {
		t.Skipf("nm not on PATH: %v", err)
	}
	if _, err := exec.LookPath("cmake"); err != nil {
		t.Skipf("cmake not on PATH: %v", err)
	}
	bazel := lookupBazel(t) // existing helper; skips if absent
	conv := lookupConverter(t)

	helloSrc, err := filepath.Abs("../../../converter/testdata/sample-projects/hello-world")
	if err != nil {
		t.Fatal(err)
	}

	// Path A: cmake build out-of-band. Same toolchain the
	// converter would use; we don't pass --toolchain-cmake-file
	// because we WANT cmake to do its native probe — that's the
	// reference.
	cmakeBuild := t.TempDir()
	mustRun(t, exec.CommandContext(context.Background(), "cmake",
		"-S", helloSrc, "-B", cmakeBuild, "-G", "Ninja",
		"-DCMAKE_BUILD_TYPE=Release",
	))
	mustRun(t, exec.CommandContext(context.Background(), "cmake",
		"--build", cmakeBuild,
	))
	cmakeLib := filepath.Join(cmakeBuild, "libhello.a")
	if _, err := os.Stat(cmakeLib); err != nil {
		t.Fatalf("cmake build produced no libhello.a: %v\n  contents: %v",
			err, dirEntries(cmakeBuild))
	}

	// Path B: convert-element + bazel build. We do a direct
	// convert-element call (no orchestrator) so the test isolates
	// the converter's translation logic from the orchestrator's
	// plumbing — failures here fall on the converter alone.
	convOut := t.TempDir()
	mustRun(t, exec.CommandContext(context.Background(), conv,
		"--source-root", helloSrc,
		"--out-build", filepath.Join(convOut, "BUILD.bazel"),
		"--out-bundle-dir", filepath.Join(convOut, "cmake-config"),
	))

	// Stage a Bazel workspace that consumes the emitted BUILD.bazel.
	// Source files (hello.c + include/hello.h) are copied next to
	// BUILD.bazel because the converter's emitter writes paths
	// relative to the package.
	ws := t.TempDir()
	mustCopyFile(t, filepath.Join(convOut, "BUILD.bazel"), filepath.Join(ws, "BUILD.bazel"))
	mustCopyFile(t, filepath.Join(helloSrc, "hello.c"), filepath.Join(ws, "hello.c"))
	if err := os.MkdirAll(filepath.Join(ws, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustCopyFile(t, filepath.Join(helloSrc, "include", "hello.h"), filepath.Join(ws, "include", "hello.h"))
	mustWriteString(t, filepath.Join(ws, "MODULE.bazel"),
		"module(name = \"hello\", version = \"0.0.0\")\n"+
			"bazel_dep(name = \"rules_cc\", version = \"0.0.10\")\n",
	)

	// bazel build //:hello — the target name comes from
	// cc_library(name = "hello", ...) which the converter emits.
	cmd := exec.CommandContext(context.Background(), bazel, "build", "//:hello")
	cmd.Dir = ws
	cmd.Stdout = testLog{t}
	cmd.Stderr = testLog{t}
	if err := cmd.Run(); err != nil {
		t.Fatalf("bazel build //:hello: %v", err)
	}

	// Locate the bazel-built libhello.a. Bazel's static-library
	// output convention for cc_library(linkstatic=True) is
	// bazel-bin/lib<name>.a; cquery confirms the precise path.
	queryCmd := exec.CommandContext(context.Background(), bazel,
		"cquery", "--output=files", "//:hello",
	)
	queryCmd.Dir = ws
	out, err := queryCmd.Output()
	if err != nil {
		t.Fatalf("bazel cquery: %v", err)
	}
	bazelLib := pickStaticArchive(strings.TrimSpace(string(out)))
	if bazelLib == "" {
		t.Fatalf("bazel cquery returned no .a archive: %q", out)
	}
	if !filepath.IsAbs(bazelLib) {
		bazelLib = filepath.Join(ws, bazelLib)
	}
	if _, err := os.Stat(bazelLib); err != nil {
		t.Fatalf("bazel-built %s missing: %v", bazelLib, err)
	}

	cmakeSyms, err := fidelity.SymbolSet(cmakeLib)
	if err != nil {
		t.Fatalf("nm cmake: %v", err)
	}
	bazelSyms, err := fidelity.SymbolSet(bazelLib)
	if err != nil {
		t.Fatalf("nm bazel: %v", err)
	}

	t.Logf("cmake symbols (%d): %v", len(cmakeSyms), keysOf(cmakeSyms))
	t.Logf("bazel symbols (%d): %v", len(bazelSyms), keysOf(bazelSyms))

	diff := fidelity.DiffSymbols(cmakeSyms, bazelSyms)
	if !diff.Empty() {
		t.Errorf("hello-world fidelity: symbol mismatch in libhello.a\n%s", diff.Format())
	}
}

// pickStaticArchive picks the first whitespace-separated token in s
// that ends with ".a". `bazel cquery --output=files` for a
// cc_library can list multiple files (the archive plus interface
// metadata); we only diff the archive.
func pickStaticArchive(s string) string {
	for _, tok := range strings.Fields(s) {
		if strings.HasSuffix(tok, ".a") {
			return tok
		}
	}
	return ""
}

func keysOf(m map[string]fidelity.Symbol) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func mustRun(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	cmd.Stdout = testLog{t}
	cmd.Stderr = testLog{t}
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %v", cmd.Args, err)
	}
}

func mustCopyFile(t *testing.T, src, dst string) {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func mustWriteString(t *testing.T, dst, body string) {
	t.Helper()
	if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func dirEntries(dir string) []string {
	es, _ := os.ReadDir(dir)
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}
