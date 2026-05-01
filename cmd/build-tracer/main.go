// build-tracer is the in-action process tracer for the
// trace-driven autotools-to-Bazel converter (see
// docs/trace-driven-autotools.md). Wraps a build invocation in
// strace; the resulting trace artifact is what
// convert-element-autotools reads to recover Bazel targets.
//
// Spike scope: thin shim around `strace -f -e trace=execve`.
// All process tree + argv data we care about flows through
// strace's text format. Future iterations may swap strace for a
// native ptrace implementation (no host strace dependency,
// finer-grained event filtering, JSONL output for byte-stable
// trace artifacts) — but for spike-validation, leveraging
// strace lets us focus on the cross-event correlation logic.
//
// Usage:
//
//	build-tracer --out=<trace.log> -- <cmd> [args...]
//
// The wrapper exits with the build command's exit status. The
// trace artifact is written even if the build fails, so failure
// modes that surface during recovery (link errors against
// cross-element libs, missing source files in the staged tree,
// etc.) can be inspected post-hoc.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

func main() {
	out := flag.String("out", "", "path to write the trace artifact (strace text format)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: build-tracer --out=<path> -- <cmd> [args...]")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	straceArgs := []string{
		"-f",                 // follow forks
		"-e", "trace=execve", // we only care about exec events
		"-s", "4096", // long enough for argv strings
		"--signal=none", // skip signal noise
		"-o", *out,      // trace destination
		"--",
	}
	straceArgs = append(straceArgs, args...)

	cmd := exec.Command("strace", straceArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		// strace propagates the wrapped command's exit status when
		// not handling it via --signal. ExitError gives us the
		// wrapped command's exit; other errors (strace itself
		// missing, exec failed) are non-zero from us.
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "build-tracer: %v\n", err)
		os.Exit(1)
	}
}
