// derive-toolchain probes cmake's view of the host toolchain and
// emits a complete Bazel cc_toolchain_config / cc_toolchain /
// platform / toolchain bundle that downstream `bazel build` consumes
// directly.
//
// First-iteration surface:
//
//	derive-toolchain --reply-dir <dir> --out <dir>
//
// Reads a pre-recorded cmake File API reply (from the probe-project
// fixture, or from any cmake-configured project), runs it through
// toolchain.FromReply -> bazeltoolchain.Emit, and writes the bundle
// under --out.
//
// Probing live cmake (running cmake on the probe project across a
// variant matrix) is the obvious next step; for now the operator
// drives the configure step themselves and points us at the reply.
// Documented in docs/toolchain-derivation-plan.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/bazeltoolchain"
	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/cmaketoolchain"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/toolchain"
)

func main() {
	fs := flag.NewFlagSet("derive-toolchain", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	replyDir := fs.String("reply-dir", "", "cmake File API reply directory (e.g. <build>/.cmake/api/v1/reply). Required.")
	outDir := fs.String("out", "", "output directory; created if absent. BUILD.bazel + cc_toolchain_config.bzl land here.")
	pkgName := fs.String("package-name", "toolchain", "Bazel package name for the emitted rules (purely cosmetic; affects the toolchain identifier suffix).")
	targetLibc := fs.String("target-libc", "", "target libc identifier (glibc, musl, macosx, ...). Auto-derived from CMAKE_SYSTEM_NAME when empty.")

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(64)
	}
	if *replyDir == "" || *outDir == "" {
		fmt.Fprintln(os.Stderr, "derive-toolchain: --reply-dir and --out are required")
		fs.Usage()
		os.Exit(64)
	}

	r, err := fileapi.Load(*replyDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "derive-toolchain: load reply: %v\n", err)
		os.Exit(1)
	}
	m, err := toolchain.FromReply(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "derive-toolchain: extract: %v\n", err)
		os.Exit(1)
	}

	bundle, err := bazeltoolchain.Emit(m, bazeltoolchain.Config{
		PackageName: *pkgName,
		TargetLibc:  *targetLibc,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "derive-toolchain: emit: %v\n", err)
		os.Exit(1)
	}

	// CMake-side toolchain file lets the orchestrator's per-element
	// cmake invocations skip the compiler-detection probe — a
	// measurable per-conversion latency win at project scale.
	cmakeBody, err := cmaketoolchain.Emit(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "derive-toolchain: emit cmake toolchain: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "derive-toolchain: mkdir %s: %v\n", *outDir, err)
		os.Exit(1)
	}
	for name, body := range bundle.Files {
		dst := filepath.Join(*outDir, name)
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "derive-toolchain: write %s: %v\n", dst, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "derive-toolchain: wrote %s (%d bytes)\n", dst, len(body))
	}
	cmakeDst := filepath.Join(*outDir, "toolchain.cmake")
	if err := os.WriteFile(cmakeDst, cmakeBody, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "derive-toolchain: write %s: %v\n", cmakeDst, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "derive-toolchain: wrote %s (%d bytes)\n", cmakeDst, len(cmakeBody))
}
