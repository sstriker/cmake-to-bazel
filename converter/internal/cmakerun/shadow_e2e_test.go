//go:build e2e

package cmakerun_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sstriker/cmake-to-bazel/converter/internal/cmakerun"
	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/bazel"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/lower"
	"github.com/sstriker/cmake-to-bazel/converter/internal/shadow"
)

// TestE2E_HelloWorld_ShadowTree validates the architectural keystone: running
// cmake against the path-only shadow tree must produce a BUILD.bazel
// byte-identical to running it against the real tree. If they diverge the
// shadow allowlist is missing a file cmake actually reads — emit a triage
// hint pointing the operator at trace.jsonl.
func TestE2E_HelloWorld_ShadowTree(t *testing.T) {
	realSrc, err := filepath.Abs("../../testdata/sample-projects/hello-world")
	if err != nil {
		t.Fatal(err)
	}

	realOut := runConvert(t, realSrc, "")
	t.Logf("real-tree BUILD.bazel:\n%s", realOut)

	// Build the shadow tree.
	shadowSrc := filepath.Join(t.TempDir(), "shadow")
	if err := shadow.Build(realSrc, shadowSrc, shadow.DefaultAllowlist()); err != nil {
		t.Fatalf("shadow.Build: %v", err)
	}

	// Where the trace will land on the host. The build dir gets created by
	// runConvert; we tell cmakerun to redirect trace inside the sandbox at
	// /build/trace.jsonl, which maps to <buildDir>/trace.jsonl.
	traceRel := "trace.jsonl"
	shadowOut := runConvert(t, shadowSrc, "/build/"+traceRel)

	// Scrub absolute source paths before comparison: realSrc and shadowSrc
	// differ, but both should reduce to a stable form.
	realOut = []byte(strings.ReplaceAll(string(realOut), realSrc, "<SRC>"))
	shadowOut = []byte(strings.ReplaceAll(string(shadowOut), shadowSrc, "<SRC>"))

	if string(realOut) != string(shadowOut) {
		t.Errorf("shadow-tree output diverges from real-tree output\n--- real ---\n%s\n--- shadow ---\n%s", realOut, shadowOut)
	}
}

// runConvert spins a fresh build dir, runs cmake under bwrap, lowers the
// reply, and emits BUILD.bazel. Returns the emitter output. If tracePath is
// non-empty (in-sandbox absolute path), the trace is also captured and the
// extracted source-relative read paths are logged for triage.
func runConvert(t *testing.T, src, sandboxTracePath string) []byte {
	t.Helper()
	buildDir := t.TempDir()

	reply, err := cmakerun.Configure(t.Context(), cmakerun.Options{
		HostSourceRoot: src,
		HostBuildDir:   buildDir,
		TracePath:      sandboxTracePath,
		Stdout:         testWriter{t},
		Stderr:         testWriter{t},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	if sandboxTracePath != "" {
		// Trace landed at <buildDir>/<basename(sandboxTracePath)>.
		hostTrace := filepath.Join(buildDir, filepath.Base(sandboxTracePath))
		raw, err := os.ReadFile(hostTrace)
		if err != nil {
			t.Logf("trace not produced at %s: %v", hostTrace, err)
		} else {
			reads := shadow.ExtractReadPaths(raw, src)
			t.Logf("trace read paths under %s: %v", src, reads)
		}
	}

	r, err := fileapi.Load(reply.HostPath)
	if err != nil {
		t.Fatalf("fileapi.Load: %v", err)
	}
	pkg, err := lower.ToIR(r, lower.Options{HostSourceRoot: src})
	if err != nil {
		t.Fatalf("ToIR: %v", err)
	}
	out, err := bazel.Emit(pkg)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return out
}
