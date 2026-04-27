package orchestrator_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRun_Deterministic_ThreeTmpdirReplay is M3a step 7's acceptance gate:
// the orchestrator must produce byte-identical determinism.json across
// three independent runs of the same source tree into fresh tmpdirs. If
// per-element BUILD.bazel, cmake-config bundles, or read_paths.json
// drift, determinism.json's sha256 entries do too — and the whole
// architectural premise of "same source -> same converted distro across
// machines" needs investigation.
//
// Why three not two: a stable two-run diff could mask alternating
// behaviour (run-N produces A; run-N+1 produces B; run-N+2 produces A).
// Three runs catch that pattern with one test.
func TestRun_Deterministic_ThreeTmpdirReplay(t *testing.T) {
	const N = 3
	hashes := make([]string, N)
	for i := 0; i < N; i++ {
		out := t.TempDir()
		_ = runOrchestrator(t, out, "success")

		body, err := os.ReadFile(filepath.Join(out, "manifest", "determinism.json"))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		hashes[i] = string(body)
	}
	for i := 1; i < N; i++ {
		if hashes[i] != hashes[0] {
			t.Errorf("determinism.json drifted between runs 0 and %d:\n--- run 0 ---\n%s\n--- run %d ---\n%s",
				i, hashes[0], i, hashes[i])
		}
	}
}

// TestRun_Deterministic_ContentEditUnderShadowDoesntDrift demonstrates the
// keystone shadow-tree claim end-to-end at distro scale: editing a non-
// allowlisted source file's content (.c body) leaves determinism.json
// byte-identical because the shadow tree zero-byte-stubs the file.
func TestRun_Deterministic_ContentEditUnderShadowDoesntDrift(t *testing.T) {
	helloC := "../../../converter/testdata/sample-projects/hello-world/hello.c"
	abs, err := filepath.Abs(helloC)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(abs, orig, 0o644) })

	out1 := t.TempDir()
	_ = runOrchestrator(t, out1, "success")
	det1 := mustReadFile(t, filepath.Join(out1, "manifest", "determinism.json"))

	// Mutate hello.c — non-allowlisted, gets stubbed in the shadow tree.
	mutated := append([]byte("/* edit that should not move anything */\n"), orig...)
	if err := os.WriteFile(abs, mutated, 0o644); err != nil {
		t.Fatal(err)
	}

	out2 := t.TempDir()
	_ = runOrchestrator(t, out2, "success")
	det2 := mustReadFile(t, filepath.Join(out2, "manifest", "determinism.json"))

	if string(det1) != string(det2) {
		t.Errorf("content-only edit to non-allowlisted .c shifted determinism.json\n--- before ---\n%s\n--- after ---\n%s",
			det1, det2)
	}
}
