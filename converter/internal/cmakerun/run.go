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
	"os/exec"
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

	// HostToolchainCMakeFile, when non-empty, is mounted read-only
	// at /toolchain.cmake in the sandbox and passed to cmake via
	// -DCMAKE_TOOLCHAIN_FILE. Pre-derived by derive-toolchain;
	// skips cmake's compiler-detection probe, cutting per-conversion
	// configure latency.
	HostToolchainCMakeFile string

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

	cmakeBin, extraBinds, err := resolveToolchainBinaries()
	if err != nil {
		return Reply{}, err
	}

	cmakeArgv := []string{
		cmakeBin,
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
	if opts.HostToolchainCMakeFile != "" {
		// The toolchain file gets mounted at /toolchain.cmake in
		// the sandbox; pass that in-sandbox path to cmake.
		cmakeArgv = append(cmakeArgv, "-DCMAKE_TOOLCHAIN_FILE=/toolchain.cmake")
		extraBinds = append(extraBinds, [2]string{
			opts.HostToolchainCMakeFile, "/toolchain.cmake",
		})
	}

	sb := hermetic.Sandbox{
		SourceRoot:   opts.HostSourceRoot,
		BuildDir:     opts.HostBuildDir,
		PrefixDir:    opts.HostPrefixDir,
		Argv:         cmakeArgv,
		Stdout:       opts.Stdout,
		Stderr:       opts.Stderr,
		ExtraROBinds: extraBinds,
	}

	if err := sb.Run(ctx); err != nil {
		return Reply{}, fmt.Errorf("cmakerun: cmake failed: %w", err)
	}

	return Reply{
		HostPath:    filepath.Join(opts.HostBuildDir, ".cmake", "api", "v1", "reply"),
		SandboxPath: "/build/.cmake/api/v1/reply",
	}, nil
}

// resolveToolchainBinaries finds cmake and ninja on the host PATH and returns
// the cmake invocation path plus any extra read-only bind mounts the sandbox
// needs to make the resolved binaries reachable inside it.
//
// On developer machines cmake is usually at /usr/bin/cmake — already covered
// by the standard /usr ro-bind. On GitHub Actions runners the toolcache puts
// cmake at /usr/local/bin/cmake (still under /usr) symlinked to
// /opt/hostedtoolcache/cmake/<ver>/x64/bin/cmake — that target is outside
// /usr and needs an extra mount. The same applies to ninja, which cmake
// invokes by absolute path baked into CMakeCache.txt at configure time.
func resolveToolchainBinaries() (cmakeArgv0 string, extraBinds [][2]string, err error) {
	cmakeOnPath, err := exec.LookPath("cmake")
	if err != nil {
		return "", nil, fmt.Errorf("cmakerun: cmake not on PATH: %w", err)
	}
	binds, err := mountsForBinary(cmakeOnPath)
	if err != nil {
		return "", nil, err
	}
	extraBinds = append(extraBinds, binds...)

	if ninjaOnPath, err := exec.LookPath("ninja"); err == nil {
		nb, err := mountsForBinary(ninjaOnPath)
		if err != nil {
			return "", nil, err
		}
		extraBinds = append(extraBinds, nb...)
	}
	// dedupe
	extraBinds = dedupeBinds(extraBinds)

	return cmakeOnPath, extraBinds, nil
}

// mountsForBinary returns the ro-bind pairs required to make a host binary
// accessible inside the sandbox at the same path. If the binary's symlink
// target is outside /usr, both the symlink dir and the target dir are
// mounted; otherwise no extra mount is needed.
func mountsForBinary(host string) ([][2]string, error) {
	target, err := filepath.EvalSymlinks(host)
	if err != nil {
		return nil, fmt.Errorf("cmakerun: eval %s: %w", host, err)
	}
	var out [][2]string
	for _, p := range []string{filepath.Dir(host), filepath.Dir(target)} {
		if isUnderUsr(p) {
			continue
		}
		out = append(out, [2]string{p, p})
	}
	return out, nil
}

func isUnderUsr(p string) bool {
	return p == "/usr" || len(p) >= 5 && p[:5] == "/usr/"
}

func dedupeBinds(in [][2]string) [][2]string {
	seen := map[[2]string]struct{}{}
	out := make([][2]string, 0, len(in))
	for _, m := range in {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}
