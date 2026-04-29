// convert-element converts one CMake source tree into a fully-declared
// BUILD.bazel plus a synthetic <Pkg>Config.cmake bundle. Each invocation
// handles exactly one codebase; the M3 orchestrator drives many such
// invocations across a project (one REAPI action per codebase) and also
// runnable standalone for development.
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
	"time"

	"github.com/sstriker/cmake-to-bazel/converter/internal/cli"
	"github.com/sstriker/cmake-to-bazel/converter/internal/cmakerun"
	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/bazel"
	"github.com/sstriker/cmake-to-bazel/converter/internal/emit/cmakecfg"
	"github.com/sstriker/cmake-to-bazel/converter/internal/failure"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/lower"
	"github.com/sstriker/cmake-to-bazel/converter/internal/ninja"
	"github.com/sstriker/cmake-to-bazel/internal/manifest"
	"github.com/sstriker/cmake-to-bazel/internal/shadow"
)

// timings is the on-disk schema for --out-timings. Captured per-phase
// wall-clock seconds let operators see configure-vs-translation ratios
// across a project. version=1 fences future readers.
type timings struct {
	Version            int     `json:"version"`
	CMakeConfigureSecs float64 `json:"cmake_configure_seconds"`
	TranslationSecs    float64 `json:"translation_seconds"`
	TotalSecs          float64 `json:"total_seconds"`
}

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
	t0 := time.Now()
	var configureElapsed time.Duration

	replyDir := a.ReplyDir
	var ninjaPath string
	var hostBuildDir string
	if replyDir == "" {
		ctx := context.Background()

		// Architectural floor: cmake >= 3.20 (codemodel-v2 minimum). The
		// orchestrator (M3) must always run with a pinned cmake; the
		// escape hatch is for local dev only.
		if !a.AllowCMakeVersionMismatch {
			if _, _, _, err := cmakerun.AssertVersion(ctx); err != nil {
				return failure.New(failure.ConfigureFailed, "%v", err)
			}
		}

		// Real-cmake path: spin a tmp build dir, configure cmake against
		// it, then load the reply.
		buildDir, err := os.MkdirTemp("", "convert-element-build-*")
		if err != nil {
			return err
		}
		hostBuildDir = buildDir
		defer os.RemoveAll(buildDir)

		opts := cmakerun.Options{
			SourceRoot:         a.SourceRoot,
			BuildDir:           buildDir,
			PrefixDir:          a.PrefixDir,
			ToolchainCMakeFile: a.ToolchainCMakeFile,
			Stdout:             os.Stderr, // route cmake noise to our stderr
			Stderr:             os.Stderr,
		}
		if a.OutReadPaths != "" {
			opts.TracePath = filepath.Join(buildDir, "trace.jsonl")
		}
		configureStart := time.Now()
		reply, err := cmakerun.Configure(ctx, opts)
		configureElapsed = time.Since(configureStart)
		if err != nil {
			return failure.New(failure.ConfigureFailed, "%v", err)
		}
		replyDir = reply.Path
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

	var imports *manifest.Resolver
	if a.ImportsManifest != "" {
		imports, err = manifest.Load(a.ImportsManifest)
		if err != nil {
			return err
		}
	}

	prefixAbs := ""
	if a.PrefixDir != "" {
		prefixAbs, err = filepath.Abs(a.PrefixDir)
		if err != nil {
			return err
		}
	}
	pkg, err := lower.ToIR(r, g, lower.Options{
		HostSourceRoot: a.SourceRoot,
		HostPrefixDir:  prefixAbs,
		Imports:        imports,
	})
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

	if a.OutTimings != "" {
		total := time.Since(t0)
		// translation = total - configure (configureElapsed is 0 in
		// the --reply-dir offline path, so translation == total there).
		translation := total - configureElapsed
		if translation < 0 {
			translation = 0
		}
		body, _ := json.MarshalIndent(timings{
			Version:            1,
			CMakeConfigureSecs: configureElapsed.Seconds(),
			TranslationSecs:    translation.Seconds(),
			TotalSecs:          total.Seconds(),
		}, "", "  ")
		if err := os.MkdirAll(filepath.Dir(a.OutTimings), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(a.OutTimings, append(body, '\n'), 0o644); err != nil {
			return err
		}
	}

	if a.OutReadPaths != "" && hostBuildDir != "" {
		traceHost := filepath.Join(hostBuildDir, "trace.jsonl")
		raw, err := os.ReadFile(traceHost)
		if err != nil {
			return fmt.Errorf("read trace: %w", err)
		}
		reads := shadow.ExtractReadPaths(raw, a.SourceRoot)
		body, err := json.MarshalIndent(reads, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(a.OutReadPaths), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(a.OutReadPaths, append(body, '\n'), 0o644); err != nil {
			return err
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
