package fidelity

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// RunResult captures one process invocation's observable outcome.
// All three fields participate in the behavioral diff — operators
// who care about only stdout (stderr varies between toolchains)
// can call DiffStdoutOnly.
type RunResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Run executes path with args + stdin under ctx and captures
// (exit, stdout, stderr). A non-zero exit is recorded, not
// returned as an error — fidelity tests want to compare
// exit-code mismatches between two binaries, including the case
// where both crash the same way.
//
// Returns a non-nil error only on infrastructure failure
// (binary missing, fork/exec returned an unrecognized OS error).
// `*exec.ExitError` (process exited non-zero) is squashed into
// RunResult.ExitCode and returned with err=nil.
func Run(ctx context.Context, path string, args []string, stdin []byte) (RunResult, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := RunResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, fmt.Errorf("run %s: %w", path, err)
}

// BehaviorDiff is the result of comparing two RunResults.
type BehaviorDiff struct {
	ExitCodeMismatch  bool
	StdoutMismatch    bool
	StderrMismatch    bool

	LeftExitCode, RightExitCode int
	LeftStdout, RightStdout     []byte
	LeftStderr, RightStderr     []byte
}

// Empty reports whether the two runs are observationally
// indistinguishable.
func (d *BehaviorDiff) Empty() bool {
	return !d.ExitCodeMismatch && !d.StdoutMismatch && !d.StderrMismatch
}

// Format renders the diff as operator-readable prose with each
// side's value visible for the mismatched components only.
func (d *BehaviorDiff) Format() string {
	if d.Empty() {
		return "no differences"
	}
	var b strings.Builder
	if d.ExitCodeMismatch {
		fmt.Fprintf(&b, "exit code: cmake=%d bazel=%d\n", d.LeftExitCode, d.RightExitCode)
	}
	if d.StdoutMismatch {
		fmt.Fprintf(&b, "stdout differs:\n  cmake: %q\n  bazel: %q\n", d.LeftStdout, d.RightStdout)
	}
	if d.StderrMismatch {
		fmt.Fprintf(&b, "stderr differs:\n  cmake: %q\n  bazel: %q\n", d.LeftStderr, d.RightStderr)
	}
	return b.String()
}

// DiffBehavior compares two RunResults: ExitCode strict equality,
// stdout/stderr byte-wise equality. Returns the populated
// BehaviorDiff regardless of whether anything mismatches; callers
// check Empty().
func DiffBehavior(left, right RunResult) BehaviorDiff {
	d := BehaviorDiff{
		ExitCodeMismatch: left.ExitCode != right.ExitCode,
		LeftExitCode:     left.ExitCode,
		RightExitCode:    right.ExitCode,
		LeftStdout:       left.Stdout,
		RightStdout:      right.Stdout,
		LeftStderr:       left.Stderr,
		RightStderr:      right.Stderr,
	}
	if !bytes.Equal(left.Stdout, right.Stdout) {
		d.StdoutMismatch = true
	}
	if !bytes.Equal(left.Stderr, right.Stderr) {
		d.StderrMismatch = true
	}
	return d
}

// DiffStdoutOnly is the stricter "stdout matters, stderr drifts"
// variant. Same shape as DiffBehavior but Empty() ignores
// stderr mismatches. Used when toolchain version banners or
// timing-sensitive log lines on stderr would otherwise mask a
// real stdout match.
func DiffStdoutOnly(left, right RunResult) BehaviorDiff {
	d := DiffBehavior(left, right)
	d.StderrMismatch = false
	return d
}
