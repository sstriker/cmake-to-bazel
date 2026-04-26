// Package cmakerun invokes cmake against a sandboxed source/build pair and
// returns the resolved File API reply directory.
//
// One Configure call corresponds to exactly one cmake invocation. The caller
// is responsible for the build dir lifecycle (create, clean up); this package
// only writes inside it.
package cmakerun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sstriker/cmake-to-bazel/converter/internal/hermetic"
)

// Options configures one Configure call. M1 surface is intentionally narrow.
type Options struct {
	// HostSourceRoot, HostBuildDir are real paths on the host. They get
	// mounted into the sandbox at /src and /build respectively.
	HostSourceRoot string
	HostBuildDir   string

	// HostPrefixDir is the optional dependency-prefix tree, mounted at
	// /opt/prefix.
	HostPrefixDir string

	// BuildType is passed as -DCMAKE_BUILD_TYPE. M1 always passes "Release".
	BuildType string

	// TracePath, when non-empty, enables `cmake --trace-expand
	// --trace-format=json-v1 --trace-redirect=<path>`. The path is
	// in-sandbox; cmakerun bind-mounts BuildDir at /build, so a TracePath
	// like "/build/trace.jsonl" lands at filepath.Join(HostBuildDir,
	// "trace.jsonl") on the host afterward.
	TracePath string

	// Stdout/Stderr capture cmake output. Nil discards.
	Stdout, Stderr interface {
		Write([]byte) (int, error)
	}
}

// Reply is the File API reply directory cmake produced, expressed both as
// the host-visible absolute path and the in-sandbox path. Callers that load
// the reply outside the sandbox use Host; loaders that work in-sandbox use
// Sandbox.
type Reply struct {
	HostPath    string
	SandboxPath string
}

// Configure runs cmake -B /build -S /src under bwrap, with File API queries
// pre-staged for codemodel-v2, toolchains-v1, cmakeFiles-v1, and cache-v2.
// Returns the reply directory location on success.
func Configure(ctx context.Context, opts Options) (Reply, error) {
	if opts.HostSourceRoot == "" || opts.HostBuildDir == "" {
		return Reply{}, fmt.Errorf("cmakerun: HostSourceRoot and HostBuildDir required")
	}
	if opts.BuildType == "" {
		opts.BuildType = "Release"
	}

	// Stage File API query files. cmake reads these on configure to decide
	// which reply objects to emit.
	queryDir := filepath.Join(opts.HostBuildDir, ".cmake", "api", "v1", "query")
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

	cmakeArgv := []string{
		"/usr/bin/cmake",
		"-S", "/src",
		"-B", "/build",
		"-G", "Ninja",
		"-DCMAKE_BUILD_TYPE=" + opts.BuildType,
		"-DCMAKE_EXPORT_COMPILE_COMMANDS=ON",
	}
	if opts.TracePath != "" {
		cmakeArgv = append(cmakeArgv,
			"--trace-expand",
			"--trace-format=json-v1",
			"--trace-redirect="+opts.TracePath,
		)
	}

	sb := hermetic.Sandbox{
		SourceRoot: opts.HostSourceRoot,
		BuildDir:   opts.HostBuildDir,
		PrefixDir:  opts.HostPrefixDir,
		Argv:       cmakeArgv,
		Stdout:     opts.Stdout,
		Stderr:     opts.Stderr,
	}

	if err := sb.Run(ctx); err != nil {
		return Reply{}, fmt.Errorf("cmakerun: cmake failed: %w", err)
	}

	return Reply{
		HostPath:    filepath.Join(opts.HostBuildDir, ".cmake", "api", "v1", "reply"),
		SandboxPath: "/build/.cmake/api/v1/reply",
	}, nil
}
