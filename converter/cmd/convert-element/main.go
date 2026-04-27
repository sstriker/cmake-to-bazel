// convert-element converts one CMake source tree into a fully-declared
// BUILD.bazel plus a synthetic <Pkg>Config.cmake bundle. Per-element entry
// point invoked by the M3 orchestrator (one REAPI action per element) and
// also runnable standalone for development.
//
// M1 surface: --source-root for the in-development real-cmake path (NYI in
// step 4) and --reply-dir for offline runs against pre-recorded File API
// fixtures (used by step 3 / golden tests).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sstriker/cmake-to-bazel/converter/internal/cli"
	"github.com/sstriker/cmake-to-bazel/converter/internal/cmakerun"
	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/bazel"
	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/cmakecfg"
	"github.com/sstriker/cmake-to-bazel/converter/internal/failure"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/lower"
	"github.com/sstriker/cmake-to-bazel/converter/internal/ninja"
)

func main() {
	args, code := cli.Parse(os.Args[1:], os.Stderr)
	if code != cli.ExitSuccess {
		os.Exit(code)
	}
	if err := run(args); err != nil {
		os.Exit(handleError(args, err))
	}
}

func run(a cli.Args) error {
	replyDir := a.ReplyDir
	var ninjaPath string
	if replyDir == "" {
		// Real-cmake path: spin a tmp build dir, configure under bwrap, then
		// load the reply produced inside it.
		buildDir, err := os.MkdirTemp("", "convert-element-build-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(buildDir)

		ctx := context.Background()
		reply, err := cmakerun.Configure(ctx, cmakerun.Options{
			HostSourceRoot: a.SourceRoot,
			HostBuildDir:   buildDir,
			Stdout:         os.Stderr, // route cmake noise to our stderr
			Stderr:         os.Stderr,
		})
		if err != nil {
			return failure.New(failure.ConfigureFailed, "%v", err)
		}
		replyDir = reply.HostPath
		ninjaPath = filepath.Join(buildDir, "build.ninja")
	} else {
		// Offline path: a build.ninja is sometimes checked in alongside the
		// reply (recording script captures both); use it if present.
		candidate := filepath.Join(filepath.Dir(replyDir), "..", "..", "..", "build.ninja")
		// fileapi reply directory layout is <build>/.cmake/api/v1/reply, so
		// build.ninja lives four parents up. Resolve and check.
		candidate, _ = filepath.Abs(candidate)
		if _, err := os.Stat(candidate); err == nil {
			ninjaPath = candidate
		}
		// Test fixtures stash build.ninja directly inside the reply dir for
		// convenience; check there too.
		if direct := filepath.Join(replyDir, "build.ninja"); ninjaPath == "" {
			if _, err := os.Stat(direct); err == nil {
				ninjaPath = direct
			}
		}
	}

	r, err := fileapi.Load(replyDir)
	if err != nil {
		return failure.New(failure.FileAPIMissing, "load reply: %v", err)
	}

	var g *ninja.Graph
	if ninjaPath != "" {
		g, err = ninja.ParseFile(ninjaPath)
		if err != nil {
			return failure.New(failure.NinjaParseFailed, "parse %s: %v", ninjaPath, err)
		}
	}
	pkg, err := lower.ToIR(r, g, lower.Options{HostSourceRoot: a.SourceRoot})
	if err != nil {
		return err
	}
	out, err := bazel.Emit(pkg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.OutBuild), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(a.OutBuild, out, 0o644); err != nil {
		return err
	}

	if a.OutBundleDir != "" {
		bundle, err := cmakecfg.Emit(pkg, cmakecfg.Options{})
		if err != nil {
			return err
		}
		if err := os.MkdirAll(a.OutBundleDir, 0o755); err != nil {
			return err
		}
		for name, body := range bundle.Files {
			dst := filepath.Join(a.OutBundleDir, name)
			if err := os.WriteFile(dst, body, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// handleError marshals a typed Tier-1 failure to OutFailure (if requested) and
// returns the appropriate exit code.
func handleError(a cli.Args, err error) int {
	var tier1 *failure.Error
	if errors.As(err, &tier1) {
		fmt.Fprintf(os.Stderr, "convert-element: %s\n", tier1.Error())
		if a.OutFailure != "" {
			payload, _ := json.MarshalIndent(map[string]any{
				"tier":    1,
				"code":    string(tier1.Code),
				"message": tier1.Message,
			}, "", "  ")
			_ = os.MkdirAll(filepath.Dir(a.OutFailure), 0o755)
			_ = os.WriteFile(a.OutFailure, append(payload, '\n'), 0o644)
		}
		return cli.ExitTier1
	}
	fmt.Fprintf(os.Stderr, "convert-element: %v\n", err)
	return cli.ExitTier2
}
