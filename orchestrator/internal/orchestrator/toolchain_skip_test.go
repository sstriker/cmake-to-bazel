//go:build e2e

// e2e_toolchain_skip exercises the configure-skip optimization end-
// to-end: run the orchestrator twice against the fdsdk-subset, once
// without --toolchain-cmake-file and once with derive-toolchain's
// output, and assert the second pass's cumulative cmake_configure
// time is shorter than the first.
//
// Why this is the right gate: the toolchain.cmake's only purpose is
// to skip cmake's compiler-detection probe, which is a measurable
// fraction of every per-element configure. If with-file isn't faster
// than without, either we generated the wrong file or we wired
// -DCMAKE_TOOLCHAIN_FILE wrong; either way the test fires.
//
// Conservative assertion: B < A. Any improvement counts. The ratio
// is logged for operator visibility but not asserted as a number to
// avoid CI flakiness on noisy runners.
package orchestrator_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/orchestrator"
)

func TestE2E_Toolchain_SkipReducesConfigureTime(t *testing.T) {
	conv := lookupConverter(t)
	deriveBin := lookupDeriveToolchain(t)

	// Pass 1: cold orchestrator run with NO --toolchain-cmake-file.
	// This populates the per-element timing baseline AND, as a side
	// effect, leaves a fileapi reply we can hand to derive-toolchain
	// to produce the toolchain.cmake we'll test in pass 2.
	out1 := t.TempDir()
	res1 := runOrchestratorWithoutToolchain(t, out1, conv)
	logTimings(t, "pass1 (no toolchain.cmake)", res1)

	// Take one element's reply directory and run derive-toolchain
	// against it. The fdsdk-subset's hello build dir lives at
	// <out>/sources/<sha>/checkout/CMakeLists.txt processed under
	// the orchestrator's hermetic flow; we don't have direct access
	// to its build dir post-conversion. Easier: run a one-shot
	// hello-world configure ourselves to produce a fresh reply.
	tcFile := deriveToolchainCMake(t, deriveBin)

	// Pass 2: same fixture, --toolchain-cmake-file points at the
	// freshly-derived toolchain.cmake. Cache must stay cold so we
	// re-run the converter (otherwise the AC short-circuit would
	// hide the configure-time delta).
	out2 := t.TempDir()
	res2 := runOrchestratorWithToolchain(t, out2, conv, tcFile)
	logTimings(t, "pass2 (with toolchain.cmake)", res2)

	if res2.Timings.TotalCMakeConfigureSecs >= res1.Timings.TotalCMakeConfigureSecs {
		t.Errorf("toolchain.cmake did not reduce configure time:\n"+
			"  pass1 (without): %.2fs\n"+
			"  pass2 (with):    %.2fs",
			res1.Timings.TotalCMakeConfigureSecs,
			res2.Timings.TotalCMakeConfigureSecs)
	}

	improvement := res1.Timings.TotalCMakeConfigureSecs - res2.Timings.TotalCMakeConfigureSecs
	pct := 100.0 * improvement / res1.Timings.TotalCMakeConfigureSecs
	t.Logf("toolchain.cmake configure-time win: %.2fs absolute, %.1f%% relative",
		improvement, pct)
}

func runOrchestratorWithoutToolchain(t *testing.T, out, conv string) *orchestrator.Result {
	proj, g := mustLoadFixture(t)
	res, err := orchestrator.Run(context.Background(), orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             out,
		ConverterBinary: conv,
		Concurrency:     1, // serial keeps timing assertions clean
		Log:             testLog{t},
	})
	if err != nil {
		t.Fatalf("orchestrator (no toolchain): %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("Failed = %v, want []", res.Failed)
	}
	return res
}

func runOrchestratorWithToolchain(t *testing.T, out, conv, tcFile string) *orchestrator.Result {
	proj, g := mustLoadFixture(t)
	res, err := orchestrator.Run(context.Background(), orchestrator.Options{
		Project:            proj,
		Graph:              g,
		Out:                out,
		ConverterBinary:    conv,
		ToolchainCMakeFile: tcFile,
		Concurrency:        1,
		Log:                testLog{t},
	})
	if err != nil {
		t.Fatalf("orchestrator (with toolchain): %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("Failed = %v, want []", res.Failed)
	}
	return res
}

// deriveToolchainCMake runs cmake against the converter's hello-world
// sample to produce a reply, then derive-toolchain against the reply
// to emit toolchain.cmake. Returns the absolute path to the file.
//
// Real cmake invocation (not bwrap-sandboxed) — derive-toolchain is
// host-side tooling that runs once per host, separate from the
// orchestrator's hermetic pipeline.
func deriveToolchainCMake(t *testing.T, deriveBin string) string {
	hostSrc, err := filepath.Abs("../../../converter/testdata/sample-projects/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	// Stage File API queries so the reply contains toolchains-v1 +
	// cache-v2 (what FromReply needs).
	queryDir := filepath.Join(build, ".cmake", "api", "v1", "query")
	if err := os.MkdirAll(queryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"codemodel-v2", "toolchains-v1", "cmakeFiles-v1", "cache-v2"} {
		if err := os.WriteFile(filepath.Join(queryDir, q), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("cmake", "-S", hostSrc, "-B", build, "-G", "Ninja")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("cmake configure for derive-toolchain probe: %v\n%s", err, buf.String())
	}

	// derive-toolchain --reply-dir <build>/.cmake/api/v1/reply --out <tmp>
	tcOut := t.TempDir()
	reply := filepath.Join(build, ".cmake", "api", "v1", "reply")
	cmd = exec.Command(deriveBin, "--reply-dir", reply, "--out", tcOut)
	cmd.Stdout = testLog{t}
	cmd.Stderr = testLog{t}
	if err := cmd.Run(); err != nil {
		t.Fatalf("derive-toolchain: %v", err)
	}
	return filepath.Join(tcOut, "toolchain.cmake")
}

func lookupConverter(t *testing.T) string {
	if p, err := exec.LookPath("convert-element"); err == nil {
		return p
	}
	repoRoot, _ := filepath.Abs("../../..")
	fallback := filepath.Join(repoRoot, "build", "bin", "convert-element")
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	t.Skip("convert-element not on PATH and not in build/bin/")
	return ""
}

func lookupDeriveToolchain(t *testing.T) string {
	if p, err := exec.LookPath("derive-toolchain"); err == nil {
		return p
	}
	repoRoot, _ := filepath.Abs("../../..")
	fallback := filepath.Join(repoRoot, "build", "bin", "derive-toolchain")
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	t.Skip("derive-toolchain not on PATH and not in build/bin/ — run `make derive-toolchain` first")
	return ""
}

func logTimings(t *testing.T, label string, res *orchestrator.Result) {
	t.Helper()
	t.Logf("%s: cmake=%.2fs translate=%.2fs total=%.2fs ratio=%.2f",
		label,
		res.Timings.TotalCMakeConfigureSecs,
		res.Timings.TotalTranslationSecs,
		res.Timings.TotalConverterSecs,
		res.Timings.ConfigureToTranslationRatio,
	)
	_ = fmt.Sprintf // keep fmt imported in case future log lines need it
}
