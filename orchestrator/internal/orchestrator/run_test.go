package orchestrator_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/element"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/orchestrator"
)

// TestMain double-duties: when invoked with ORCHESTRATOR_STUB_CONVERTER=1 it
// behaves as a stub `convert-element` binary instead of running the test
// suite. This lets the orchestrator unit tests exec the test binary itself
// as the converter — no separate fixture binary, no shell-script
// dependence.
func TestMain(m *testing.M) {
	if os.Getenv("ORCHESTRATOR_STUB_CONVERTER") == "1" {
		os.Exit(stubConverter())
	}
	os.Exit(m.Run())
}

// stubConverter parses the convert-element flag surface (just enough), then
// produces success / Tier-1 / Tier-2 outputs as directed by env.
func stubConverter() int {
	fs := flag.NewFlagSet("stub", flag.ContinueOnError)
	srcRoot := fs.String("source-root", "", "")
	replyDir := fs.String("reply-dir", "", "")
	outBuild := fs.String("out-build", "", "")
	outBundle := fs.String("out-bundle-dir", "", "")
	outFailure := fs.String("out-failure", "", "")
	outReadPaths := fs.String("out-read-paths", "", "")
	importsManifest := fs.String("imports-manifest", "", "")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 64
	}
	_ = replyDir

	// Optional: record the imports-manifest path so the test can inspect it.
	if recDir := os.Getenv("ORCHESTRATOR_STUB_RECORD_DIR"); recDir != "" {
		_ = os.MkdirAll(recDir, 0o755)
		recPath := filepath.Join(recDir, filepath.Base(*srcRoot)+".imports.txt")
		_ = os.WriteFile(recPath, []byte(*importsManifest), 0o644)
	}

	mode := os.Getenv("ORCHESTRATOR_STUB_MODE")
	if mode == "" {
		mode = "success"
	}
	switch mode {
	case "success":
		body := fmt.Sprintf("# stub BUILD.bazel for %s\n", filepath.Base(*srcRoot))
		if err := os.MkdirAll(filepath.Dir(*outBuild), 0o755); err != nil {
			return 70
		}
		if err := os.WriteFile(*outBuild, []byte(body), 0o644); err != nil {
			return 70
		}
		if *outBundle != "" {
			if err := os.MkdirAll(*outBundle, 0o755); err != nil {
				return 70
			}
			pkg := strings.ToLower(filepath.Base(*srcRoot))
			_ = os.WriteFile(filepath.Join(*outBundle, pkg+"Config.cmake"), []byte("# stub config\n"), 0o644)
			// Realistic Targets.cmake so the orchestrator's exports-extraction
			// picks up the import declaration when building the next element's
			// imports manifest.
			targets := fmt.Sprintf("add_library(%s::%s STATIC IMPORTED)\n", pkg, pkg)
			_ = os.WriteFile(filepath.Join(*outBundle, pkg+"Targets.cmake"), []byte(targets), 0o644)
			_ = os.WriteFile(filepath.Join(*outBundle, pkg+"Targets-release.cmake"), []byte("# stub release\n"), 0o644)
		}
		if *outReadPaths != "" {
			_ = os.MkdirAll(filepath.Dir(*outReadPaths), 0o755)
			_ = os.WriteFile(*outReadPaths, []byte("[]\n"), 0o644)
		}
		return 0
	case "tier1":
		failBody, _ := json.MarshalIndent(map[string]any{
			"tier":    1,
			"code":    "configure-failed",
			"message": "stub Tier-1 failure for testing",
		}, "", "  ")
		_ = os.MkdirAll(filepath.Dir(*outFailure), 0o755)
		_ = os.WriteFile(*outFailure, append(failBody, '\n'), 0o644)
		return 1
	case "tier2":
		fmt.Fprintln(os.Stderr, "stub: Tier-2 unexpected error")
		return 65
	}
	return 70
}

// TestRun_StubSuccess: orchestrator drives the stub binary in success mode
// against the fdsdk-subset fixture and produces the expected outputs.
// The fixture has two kind:cmake elements: hello and uses-hello. Both
// should convert successfully under the stub. Topo order puts hello before
// uses-hello (uses-hello build-deps on hello).
func TestRun_StubSuccess(t *testing.T) {
	out := t.TempDir()
	res := runOrchestrator(t, out, "success")
	want := []string{"components/hello", "components/uses-hello"}
	if !sliceEqual(res.Converted, want) {
		t.Errorf("Converted = %v, want %v", res.Converted, want)
	}
	if len(res.Failed) != 0 {
		t.Errorf("Failed = %v, want []", res.Failed)
	}

	// Per-element artifacts staged for both.
	for _, want := range []string{
		"elements/components/hello/BUILD.bazel",
		"elements/components/hello/cmake-config/hello-worldConfig.cmake",
		"elements/components/hello/read_paths.json",
		"elements/components/uses-hello/BUILD.bazel",
		"elements/components/uses-hello/cmake-config/uses-helloConfig.cmake",
		"elements/components/uses-hello/read_paths.json",
	} {
		if _, err := os.Stat(filepath.Join(out, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	// Global manifest lists both, sorted.
	body := mustReadFile(t, filepath.Join(out, "manifest", "converted.json"))
	for _, want := range []string{`"components/hello"`, `"components/uses-hello"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("converted.json missing %s entry: %s", want, body)
		}
	}
	if i, j := strings.Index(string(body), `"components/hello"`), strings.Index(string(body), `"components/uses-hello"`); i > j {
		t.Errorf("converted.json entries not sorted")
	}
	failBody := mustReadFile(t, filepath.Join(out, "manifest", "failures.json"))
	if !strings.Contains(string(failBody), `"elements": []`) && !strings.Contains(string(failBody), `"elements": null`) {
		t.Errorf("failures.json should be empty: %s", failBody)
	}
}

// TestRun_StubTier1: stub fails Tier-1 for every element; orchestrator
// records both and doesn't abort.
func TestRun_StubTier1(t *testing.T) {
	out := t.TempDir()
	res := runOrchestrator(t, out, "tier1")
	if len(res.Converted) != 0 {
		t.Errorf("Converted = %v, want []", res.Converted)
	}
	if len(res.Failed) != 2 {
		t.Fatalf("Failed = %v, want 2 entries", res.Failed)
	}
	for _, fr := range res.Failed {
		if fr.Code != "configure-failed" || fr.Tier != 1 {
			t.Errorf("Failed entry %+v", fr)
		}
	}

	body := mustReadFile(t, filepath.Join(out, "manifest", "failures.json"))
	if !strings.Contains(string(body), `"configure-failed"`) {
		t.Errorf("failures.json missing code: %s", body)
	}
}

// TestRun_StubTier2: a converter crash bubbles up as an orchestrator-level
// error rather than landing in the failures registry.
func TestRun_StubTier2(t *testing.T) {
	out := t.TempDir()
	_, err := runOrchestratorRaw(t, out, "tier2")
	if err == nil {
		t.Fatal("expected Tier-2 to surface as orchestrator error")
	}
	if !strings.Contains(err.Error(), "components/hello") {
		t.Errorf("err = %v, want to mention element name", err)
	}
}

// TestRun_ImportsManifestForDownstream: the second element to convert
// (uses-hello) sees an --imports-manifest path containing hello's exports
// — the dep-export registry is propagating correctly.
func TestRun_ImportsManifestForDownstream(t *testing.T) {
	out := t.TempDir()
	rec := t.TempDir()
	t.Setenv("ORCHESTRATOR_STUB_RECORD_DIR", rec)
	res := runOrchestrator(t, out, "success")
	if len(res.Failed) != 0 {
		t.Fatalf("Failed = %v", res.Failed)
	}

	// hello converts first; its imports manifest should be empty since
	// `base` (its only dep) is kind:manual, not in the export registry.
	helloRec := mustReadFile(t, filepath.Join(rec, "hello-world.imports.txt"))
	if len(helloRec) != 0 {
		t.Errorf("hello got --imports-manifest=%q, want empty (no cmake deps)", helloRec)
	}

	// uses-hello sees a non-empty imports.json.
	usesRec := mustReadFile(t, filepath.Join(rec, "uses-hello.imports.txt"))
	if len(usesRec) == 0 {
		t.Fatal("uses-hello did not receive --imports-manifest")
	}
	importsBody := mustReadFile(t, string(usesRec))
	for _, want := range []string{
		`"version": 1`,
		`"name": "elem_components_hello"`,
		`"cmake_target": "hello-world::hello-world"`,
		`"bazel_label": "@elem_components_hello//:hello-world"`,
	} {
		if !strings.Contains(string(importsBody), want) {
			t.Errorf("imports.json missing %q\n%s", want, importsBody)
		}
	}
}

// TestRun_RejectsUnknownConverter: missing converter binary surfaces the
// PATH lookup error before iterating.
func TestRun_RejectsUnknownConverter(t *testing.T) {
	proj, g := mustLoadFixture(t)
	_, err := orchestrator.Run(context.Background(), orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             t.TempDir(),
		ConverterBinary: "/no/such/binary",
	})
	if err == nil || !strings.Contains(err.Error(), "/no/such/binary") {
		t.Errorf("err = %v, want PATH lookup failure", err)
	}
}

// ---- shared helpers --------------------------------------------------------

func runOrchestrator(t *testing.T, out, mode string) *orchestrator.Result {
	t.Helper()
	res, err := runOrchestratorRaw(t, out, mode)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func runOrchestratorRaw(t *testing.T, out, mode string) (*orchestrator.Result, error) {
	t.Helper()
	proj, g := mustLoadFixture(t)

	// Self-as-stub: re-invoke the test binary with ORCHESTRATOR_STUB_CONVERTER=1.
	stub := os.Args[0]
	t.Setenv("ORCHESTRATOR_STUB_CONVERTER", "1")
	t.Setenv("ORCHESTRATOR_STUB_MODE", mode)

	return orchestrator.Run(context.Background(), orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             out,
		ConverterBinary: stub,
		Log:             testLog{t},
	})
}

func mustLoadFixture(t *testing.T) (*element.Project, *element.Graph) {
	t.Helper()
	root, err := filepath.Abs("../../testdata/fdsdk-subset")
	if err != nil {
		t.Fatal(err)
	}
	proj, err := element.ReadProject(root, "elements")
	if err != nil {
		t.Fatal(err)
	}
	g, err := element.BuildGraph(proj)
	if err != nil {
		t.Fatal(err)
	}
	return proj, g
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// keep runtime imported for readability of platform-conditional debugging
// when the stub binary detection ever needs it; vet is happy with the
// blank import.
var _ = runtime.GOOS
