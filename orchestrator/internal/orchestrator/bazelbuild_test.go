//go:build e2e

// bazelbuild_test exercises the M5 downstream-Bazel acceptance gate:
//
//  1. Run the orchestrator against the fdsdk-subset fixture using the
//     real convert-element binary. Outputs land at <out>/elements/<name>/.
//  2. Stage testdata/bazel-downstream/{MODULE.bazel, BUILD.bazel,
//     smoke.c} into a fresh tmpdir, with MODULE.bazel.tmpl's
//     placeholders filled in to point at the orchestrator's converted.json
//     and the cmake-to-bazel module root.
//  3. Run `bazel build //:smoke` from that tmpdir. The build pulls in
//     @elem_components_uses_hello//:uses_hello_bin via the
//     converted_pkg_repo extension; success means the BUILD.bazel the
//     converter emitted is consumable by Bazel + rules_cc end-to-end.
//
// Gated behind the `e2e` build tag and skips if `bazel` (or `bazelisk`)
// is not on PATH. CI runs this via `make e2e-bazel-build`.
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

func TestE2E_BazelBuild_DownstreamConsumesConvertedRepos(t *testing.T) {
	bazel := lookupBazel(t)

	conv, err := exec.LookPath("convert-element")
	if err != nil {
		repoRoot, _ := filepath.Abs("../../..")
		fallback := filepath.Join(repoRoot, "build", "bin", "convert-element")
		if _, ferr := os.Stat(fallback); ferr == nil {
			conv = fallback
		} else {
			t.Skipf("convert-element not found (%v / %v)", err, ferr)
		}
	}

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}

	proj, g := mustLoadFixture(t)
	out := t.TempDir()

	// Step 1: run the orchestrator.
	if _, err := orchestrator.Run(context.Background(), orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             out,
		ConverterBinary: conv,
		Log:             testLog{t},
	}); err != nil {
		t.Fatalf("orchestrator: %v", err)
	}

	// Step 2: stage the downstream consumer.
	downstream := t.TempDir()
	tmplBody, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bazel-downstream", "MODULE.bazel.tmpl"))
	if err != nil {
		t.Fatalf("read tmpl: %v", err)
	}
	manifest := filepath.Join(out, "manifest", "converted.json")
	moduleBody := strings.NewReplacer(
		"__ROOT__", repoRoot,
		"__MANIFEST__", manifest,
	).Replace(string(tmplBody))
	if err := os.WriteFile(filepath.Join(downstream, "MODULE.bazel"), []byte(moduleBody), 0o644); err != nil {
		t.Fatalf("write MODULE.bazel: %v", err)
	}
	for _, f := range []string{"BUILD.bazel", "smoke.c"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bazel-downstream", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(downstream, f), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	// Step 3: bazel build.
	cmd := exec.CommandContext(context.Background(), bazel, "build", "//:smoke")
	cmd.Dir = downstream
	cmd.Stdout = testLog{t}
	cmd.Stderr = testLog{t}
	if err := cmd.Run(); err != nil {
		t.Fatalf("bazel build //:smoke: %v", err)
	}
}

func lookupBazel(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"bazelisk", "bazel"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skipf("bazel/bazelisk not on PATH; skipping M5 downstream-build acceptance")
	return ""
}
