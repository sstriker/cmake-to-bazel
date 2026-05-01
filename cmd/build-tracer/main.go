// build-tracer is the in-action process tracer for the
// trace-driven autotools-to-Bazel converter (see
// docs/trace-driven-autotools.md). Wraps a build invocation
// in a process tracer; the resulting trace artifact is what
// convert-element-autotools reads to recover Bazel targets.
//
// Two backends:
//
//   - Default: native ptrace (linux/amd64). Forks the build
//     command with PTRACE_TRACEME, follows fork/vfork/clone,
//     captures every successful execve's argv via PTRACE_PEEKDATA.
//     Output mirrors strace's text format so the converter
//     parser is unchanged.
//   - Fallback: strace shim (any platform). When --strace is
//     passed, invokes the host strace; useful on platforms
//     where the native backend isn't available, or for
//     comparison testing.
//
// Usage:
//
//	build-tracer --out=<trace.log> -- <cmd> [args...]
//	build-tracer --strace --out=<trace.log> -- <cmd> [args...]
//
// Exits with the wrapped command's exit status. The trace
// artifact is written even if the build fails, so failure
// modes that surface during recovery (link errors against
// cross-element libs, missing source files in the staged
// tree, etc.) can be inspected post-hoc.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

func main() {
	out := flag.String("out", "", "path to write the trace artifact (strace text format)")
	useStrace := flag.Bool("strace", false, "use the host strace binary instead of the native ptrace backend (fallback for non-linux/amd64 hosts)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: build-tracer [--strace] --out=<path> -- <cmd> [args...]")
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

	if *useStrace || !nativeBackendAvailable() {
		os.Exit(runStrace(*out, args))
	}
	os.Exit(runNative(*out, args))
}

// runStrace invokes the host strace binary as a thin wrapper
// around the build command. Used as the fallback backend when
// the native ptrace path isn't available.
func runStrace(out string, args []string) int {
	straceArgs := []string{
		"-f",                 // follow forks
		"-e", "trace=execve", // we only care about exec events
		"-s", "4096", // long enough for argv strings
		"--signal=none", // skip signal noise
		"-o", out,       // trace destination
		"--",
	}
	straceArgs = append(straceArgs, args...)

	cmd := exec.Command("strace", straceArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "build-tracer: %v\n", err)
		return 1
	}
	return 0
}
