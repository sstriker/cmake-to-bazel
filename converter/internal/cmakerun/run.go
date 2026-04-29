// Package cmakerun invokes cmake configure against a source/build pair
// and returns the File API reply directory.
//
// One Configure call corresponds to exactly one cmake invocation. The
// caller owns the build dir lifecycle (create, clean up); this package
// only writes inside it.
//
// Hermeticity is the caller's responsibility — typically achieved by
// running cmakerun.Configure inside an REAPI Action whose worker
// provides the sandbox. The package sets the deterministic env
// (SOURCE_DATE_EPOCH, locale, find_package suppression) on the cmake
// child process so configure-time outputs stay byte-stable across hosts
// even when no outer sandbox is in play.
package cmakerun

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// SourceDateEpoch is the project-wide fixed timestamp for deterministic
// configure-time outputs. 2020-01-01T00:00:00Z, picked arbitrarily to be
// visibly synthetic and not collide with real package mtimes.
const SourceDateEpoch = "1577836800"

// Options configures one Configure call.
type Options struct {
	// SourceRoot is the cmake source root (-S).
	SourceRoot string

	// BuildDir is the cmake build directory (-B). Caller owns lifecycle.
	BuildDir string

	// PrefixDir, when non-empty, is added to CMAKE_PREFIX_PATH so
	// find_package picks up the synthetic prefix tree of dep
	// <Pkg>Config.cmake bundles produced by previous conversions.
	PrefixDir string

	// ToolchainCMakeFile, when non-empty, is passed via
	// -DCMAKE_TOOLCHAIN_FILE=. Pre-derived by derive-toolchain;
	// skips cmake's compiler-detection probe, cutting per-conversion
	// configure latency.
	ToolchainCMakeFile string

	// BuildType is passed as -DCMAKE_BUILD_TYPE. Defaults to Release.
	BuildType string

	// TracePath, when non-empty, enables `cmake --trace-expand
	// --trace-format=json-v1 --trace-redirect=<TracePath>`.
	TracePath string

	// Stdout/Stderr capture cmake output. Nil discards.
	Stdout, Stderr io.Writer
}

// Reply is the File API reply directory cmake produced.
type Reply struct {
	Path string
}

// Configure runs cmake -B <build> -S <source>, with File API queries
// pre-staged for codemodel-v2, toolchains-v1, cmakeFiles-v1, and cache-v2.
// Returns the reply directory location on success.
func Configure(ctx context.Context, opts Options) (Reply, error) {
	if opts.SourceRoot == "" || opts.BuildDir == "" {
		return Reply{}, fmt.Errorf("cmakerun: SourceRoot and BuildDir required")
	}
	if opts.BuildType == "" {
		opts.BuildType = "Release"
	}

	queryDir := filepath.Join(opts.BuildDir, ".cmake", "api", "v1", "query")
	if err := os.MkdirAll(queryDir, 0o755); err != nil {
		return Reply{}, fmt.Errorf("cmakerun: stage query dir: %w", err)
	}
	for _, kind := range []string{"codemodel-v2", "toolchains-v1", "cmakeFiles-v1", "cache-v2"} {
		f, err := os.Create(filepath.Join(queryDir, kind))
		if err != nil {
			return Reply{}, fmt.Errorf("cmakerun: stage query %s: %w", kind, err)
		}
		_ = f.Close()
	}

	// Empty HOME defeats ~/.cmake/packages reads when no outer sandbox
	// rewrites HOME. Best-effort cleanup; cmake only reads from here.
	homeDir, err := os.MkdirTemp("", "cmakerun-home-*")
	if err != nil {
		return Reply{}, fmt.Errorf("cmakerun: stage home: %w", err)
	}
	defer os.RemoveAll(homeDir)

	argv := []string{
		"-S", opts.SourceRoot,
		"-B", opts.BuildDir,
		"-G", "Ninja",
		"-DCMAKE_BUILD_TYPE=" + opts.BuildType,
		"-DCMAKE_EXPORT_COMPILE_COMMANDS=ON",
	}
	if opts.TracePath != "" {
		argv = append(argv,
			"--trace-expand",
			"--trace-format=json-v1",
			"--trace-redirect="+opts.TracePath,
		)
	}
	if opts.ToolchainCMakeFile != "" {
		// CMake resolves CMAKE_TOOLCHAIN_FILE relative paths against
		// the build-dir first, then the source-dir; neither matches
		// the executor's input-root layout where the file lands at
		// <workdir>/toolchain.cmake. Pass an absolute path so cmake
		// loads the file regardless of cwd / build-dir choice.
		toolchainAbs, err := filepath.Abs(opts.ToolchainCMakeFile)
		if err != nil {
			return Reply{}, fmt.Errorf("cmakerun: abs toolchain file: %w", err)
		}
		argv = append(argv, "-DCMAKE_TOOLCHAIN_FILE="+toolchainAbs)
	}

	cmd := exec.CommandContext(ctx, "cmake", argv...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	cmd.Env = configureEnv(homeDir, opts.PrefixDir)

	if err := cmd.Run(); err != nil {
		return Reply{}, fmt.Errorf("cmakerun: cmake failed: %w", err)
	}

	return Reply{
		Path: filepath.Join(opts.BuildDir, ".cmake", "api", "v1", "reply"),
	}, nil
}

// configureEnv returns the controlled env for the cmake child. PATH is
// inherited so cmake/ninja resolve via whatever the host or worker
// provides; the rest is fixed for cross-host determinism. The
// CMAKE_FIND_USE_*_PATH cluster suppresses host-leak find_package paths
// (see docs/cmake_analysis.md).
func configureEnv(homeDir, prefixDir string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + homeDir,
		"LC_ALL=C",
		"LANG=C",
		"SOURCE_DATE_EPOCH=" + SourceDateEpoch,
		"CMAKE_FIND_USE_CMAKE_ENVIRONMENT_PATH=OFF",
		"CMAKE_FIND_USE_CMAKE_PATH=OFF",
		"CMAKE_FIND_USE_CMAKE_SYSTEM_PATH=OFF",
		"CMAKE_FIND_USE_PACKAGE_REGISTRY=OFF",
		"CMAKE_FIND_USE_SYSTEM_PACKAGE_REGISTRY=OFF",
		"CMAKE_FIND_USE_PACKAGE_ROOT_PATH=ON",
		"CMAKE_FIND_USE_SYSTEM_ENVIRONMENT_PATH=OFF",
		"CMAKE_FIND_PACKAGE_PREFER_CONFIG=ON",
	}
	if prefixDir != "" {
		env = append(env, "CMAKE_PREFIX_PATH="+prefixDir)
	}
	return env
}
