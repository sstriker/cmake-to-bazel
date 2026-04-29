//go:build e2e

// e2e_test runs the orchestrator against the real convert-element binary
// and the fdsdk-subset fixture. Both kind:cmake elements (hello,
// uses-hello) should convert cleanly under bwrap.
//
// Gated behind the `e2e` build tag because it depends on cmake + bwrap
// being installed; CI's e2e job invokes it as part of the M3a acceptance
// suite.
package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/orchestrator"
)

func TestE2E_Orchestrate_StubSubset(t *testing.T) {
	conv, err := exec.LookPath("convert-element")
	if err != nil {
		// CI builds the binary into build/bin/ via the Makefile; fall back
		// to that location so `make e2e-orchestrate` works without the
		// binary being on $PATH.
		repoRoot, _ := filepath.Abs("../../..")
		fallback := filepath.Join(repoRoot, "build", "bin", "convert-element")
		if _, ferr := os.Stat(fallback); ferr == nil {
			conv = fallback
		} else {
			t.Skipf("convert-element not found (PATH=%v fallback=%v)", err, ferr)
		}
	}

	proj, g := mustLoadFixture(t)
	out := t.TempDir()

	res, err := orchestrator.Run(context.Background(), orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             out,
		ConverterBinary: conv,
		Log:             testLog{t},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"components/hello", "components/uses-hello"}
	if !sliceEqual(res.Converted, want) {
		t.Errorf("Converted = %v, want %v", res.Converted, want)
	}
	if len(res.Failed) != 0 {
		t.Errorf("Failed = %v, want []", res.Failed)
	}

	// Per-element artifacts that real convert-element produces.
	for _, want := range []string{
		"elements/components/hello/BUILD.bazel",
		"elements/components/hello/cmake-config/helloConfig.cmake",
		"elements/components/hello/cmake-config/helloTargets.cmake",
		"elements/components/hello/cmake-config/helloTargets-release.cmake",
		"elements/components/uses-hello/BUILD.bazel",
		"elements/components/uses-hello/cmake-config/uses_helloConfig.cmake",
	} {
		if _, err := os.Stat(filepath.Join(out, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	helloBuild := mustReadFile(t, filepath.Join(out, "elements", "components", "hello", "BUILD.bazel"))
	if !strings.Contains(string(helloBuild), `name = "hello"`) {
		t.Errorf("hello BUILD.bazel doesn't declare hello target: %s", helloBuild)
	}

	// Architectural acceptance: uses_hello_bin's deps must include both
	// the in-element dep (:uses_hello) and the cross-element label
	// (//elements/components/hello:hello). The latter only ends up in the
	// codemodel's link.commandFragments as an absolute /opt/prefix path,
	// resolved via the imports manifest's link_paths field.
	usesBuild := mustReadFile(t, filepath.Join(out, "elements", "components", "uses-hello", "BUILD.bazel"))
	for _, want := range []string{
		`":uses_hello"`,
		`"//elements/components/hello:hello"`,
	} {
		if !strings.Contains(string(usesBuild), want) {
			t.Errorf("uses-hello BUILD.bazel missing %s\n%s", want, usesBuild)
		}
	}
}
