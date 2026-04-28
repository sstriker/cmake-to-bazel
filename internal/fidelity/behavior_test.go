package fidelity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRun_CapturesAllThreeOutcomeAxes: a small shell script that
// writes to both stdout and stderr and exits with a known code.
// Run() must populate RunResult.{ExitCode, Stdout, Stderr}.
func TestRun_CapturesAllThreeOutcomeAxes(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "x.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho out-line\necho err-line >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), script, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if string(res.Stdout) != "out-line\n" {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	if string(res.Stderr) != "err-line\n" {
		t.Errorf("Stderr = %q", res.Stderr)
	}
}

func TestRun_StdinPiped(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cat.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), script, nil, []byte("hello\n"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "hello\n" {
		t.Errorf("Stdout = %q, want hello\\n", res.Stdout)
	}
}

func TestDiffBehavior_AllMatch(t *testing.T) {
	a := RunResult{ExitCode: 0, Stdout: []byte("hi\n"), Stderr: []byte{}}
	b := RunResult{ExitCode: 0, Stdout: []byte("hi\n"), Stderr: []byte{}}
	d := DiffBehavior(a, b)
	if !d.Empty() {
		t.Errorf("identical runs should diff empty: %s", d.Format())
	}
}

func TestDiffBehavior_ExitCodeMismatchReported(t *testing.T) {
	a := RunResult{ExitCode: 0, Stdout: []byte("x"), Stderr: []byte{}}
	b := RunResult{ExitCode: 1, Stdout: []byte("x"), Stderr: []byte{}}
	d := DiffBehavior(a, b)
	if !d.ExitCodeMismatch || d.StdoutMismatch || d.StderrMismatch {
		t.Errorf("ExitCodeMismatch only: %+v", d)
	}
}

func TestDiffBehavior_StdoutMismatchReported(t *testing.T) {
	a := RunResult{Stdout: []byte("a")}
	b := RunResult{Stdout: []byte("b")}
	d := DiffBehavior(a, b)
	if !d.StdoutMismatch {
		t.Errorf("StdoutMismatch should fire: %+v", d)
	}
}

func TestDiffStdoutOnly_IgnoresStderrDrift(t *testing.T) {
	a := RunResult{Stdout: []byte("hi\n"), Stderr: []byte("v1.2.3\n")}
	b := RunResult{Stdout: []byte("hi\n"), Stderr: []byte("v1.2.4\n")}
	if d := DiffStdoutOnly(a, b); !d.Empty() {
		t.Errorf("DiffStdoutOnly should ignore stderr drift: %s", d.Format())
	}
	if d := DiffBehavior(a, b); d.Empty() {
		t.Errorf("DiffBehavior should still flag stderr drift")
	}
}

func TestRun_BinaryNotFound(t *testing.T) {
	_, err := Run(context.Background(), "/no/such/path", nil, nil)
	if err == nil {
		t.Error("expected infra error for missing binary")
	}
}
