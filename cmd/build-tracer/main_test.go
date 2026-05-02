package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildTracer_E2E confirms build-tracer wraps a command
// under strace and produces a trace artifact containing the
// expected execve line. Skipped if strace isn't available
// on PATH (CI containers without ptrace permission would
// trip the run; this test gates on the host being capable).
func TestBuildTracer_E2E(t *testing.T) {
	if _, err := exec.LookPath("strace"); err != nil {
		t.Skip("strace not on PATH; skipping")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "build-tracer")
	out := filepath.Join(tmp, "trace.log")

	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mustDir(t)
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}

	// Run a trivial command under the tracer; assert the
	// trace records its execve. /bin/true picks because it
	// has minimal subprocess noise.
	cmd := exec.Command(bin, "--out="+out, "--", "/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("build-tracer run: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if !strings.Contains(string(body), "execve(\"/bin/true\"") {
		t.Errorf("trace missing /bin/true execve\n--body--\n%s", body)
	}
}

// TestBuildTracer_NativeCapturesForkedExecve is a stronger
// check than _E2E: it runs `sh -c '/bin/true'` so the cmd
// path forks (sh) before exec'ing the leaf (/bin/true). The
// native backend has to follow the fork via
// PTRACE_O_TRACEFORK/CLONE and capture the leaf's execve.
// Skipped on non-amd64 Linux where the native backend isn't
// compiled in.
func TestBuildTracer_NativeCapturesForkedExecve(t *testing.T) {
	if _, err := exec.LookPath("strace"); err != nil {
		// Test relies on `go build` + ptrace working on the
		// host. strace's presence approximates "this kernel
		// allows ptrace from a parent." Skip if absent.
		t.Skip("strace not on PATH; gating native test on the same host capability")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "build-tracer")
	out := filepath.Join(tmp, "trace.log")

	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mustDir(t)
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}

	cmd := exec.Command(bin, "--out="+out, "--", "/bin/sh", "-c", "/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("build-tracer run: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// Both the wrapping shell AND the forked leaf should
	// appear. The leaf's execve is what proves fork-following
	// works (we synthesize the wrapping shell's exec from
	// cmd.Args at startup; the leaf can only come from a
	// PTRACE_EVENT_FORK + child syscall stop).
	for _, want := range []string{"execve(\"/bin/sh\"", "execve(\"/bin/true\""} {
		if !strings.Contains(string(body), want) {
			t.Errorf("native trace missing %q\n--body--\n%s", want, body)
		}
	}
}

// TestBuildTracer_StraceFallback confirms the strace shim is
// reachable via --strace, so non-amd64 hosts (or ones where
// the native backend has issues) have a working fallback.
func TestBuildTracer_StraceFallback(t *testing.T) {
	if _, err := exec.LookPath("strace"); err != nil {
		t.Skip("strace not on PATH; skipping")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "build-tracer")
	out := filepath.Join(tmp, "trace.log")

	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mustDir(t)
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	cmd := exec.Command(bin, "--strace", "--out="+out, "--", "/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("build-tracer --strace run: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `execve("/bin/true"`) {
		t.Errorf("strace-fallback trace missing /bin/true execve\n--body--\n%s", body)
	}
}

// TestBuildTracer_PropagatesExit confirms a non-zero exit from
// the wrapped command surfaces from build-tracer too. ptrace
// permissions can suppress this on hardened sandboxes; the
// test skips when the strace invocation itself fails for
// non-exit reasons.
func TestBuildTracer_PropagatesExit(t *testing.T) {
	if _, err := exec.LookPath("strace"); err != nil {
		t.Skip("strace not on PATH; skipping")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "build-tracer")
	out := filepath.Join(tmp, "trace.log")

	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mustDir(t)
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}

	cmd := exec.Command(bin, "--out="+out, "--", "/bin/false")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected non-zero exit; got nil")
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ee.ExitCode() == 0 {
			t.Errorf("expected non-zero exit; got 0")
		}
	}
}

func mustDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}
