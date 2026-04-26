// Package hermetic builds the bwrap argv that runs cmake in an isolated
// sandbox.
//
// M1 keeps the mount layout broad (read-only bind /usr, /lib, /lib64) so
// system gcc / cmake / ninja resolve their dependencies without per-binary
// curation. M3 narrows this onto a pinned, content-addressed toolchain dir,
// at which point we drop the host /usr/lib bind and add only the toolchain
// prefix.
//
// Environment is fully cleared and re-seeded with the minimum cmake needs:
// PATH, HOME (pointed at a tmpfs to defeat ~/.cmake/packages), LC_ALL/LANG=C,
// SOURCE_DATE_EPOCH for deterministic timestamps, and the
// CMAKE_FIND_USE_*_PATH=OFF cluster from cmake_analysis.md to suppress
// host-leak find_package paths. find_package itself isn't exercised by
// hello-world but the env should match what we'll feed real packages.
package hermetic

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// Sandbox runs a single command inside bwrap with hermetic mounts and env.
//
// SourceRoot is mounted read-only at /src; BuildDir is mounted read-write at
// /build. Both must exist on the host. PrefixDir, when non-empty, is mounted
// read-only at /opt/prefix (M1: not used by hello-world; M3: dependency
// prefix).
type Sandbox struct {
	SourceRoot string
	BuildDir   string
	PrefixDir  string

	// Argv is the command + args to run inside the sandbox. Resolved against
	// the in-sandbox PATH (typically /usr/bin/cmake plus its arguments).
	Argv []string

	// Stdout/Stderr capture the in-sandbox process output. Nil discards.
	Stdout io.Writer
	Stderr io.Writer

	// ExtraSetEnv is k/v pairs added on top of the standard hermetic env.
	// Use sparingly — anything here defeats hermeticity by definition.
	ExtraSetEnv map[string]string
}

// SOURCEDateEpoch is the project-wide fixed timestamp for deterministic
// configure-time outputs. 2020-01-01T00:00:00Z, picked arbitrarily to be
// visibly synthetic and not collide with real package mtimes.
const SOURCEDateEpoch = "1577836800"

// Build builds the bwrap argv but does not execute it. Exposed for tests
// that want to inspect the mount/env layout without actually running.
func (s Sandbox) Build() ([]string, error) {
	if s.SourceRoot == "" {
		return nil, fmt.Errorf("hermetic: SourceRoot required")
	}
	if s.BuildDir == "" {
		return nil, fmt.Errorf("hermetic: BuildDir required")
	}
	if len(s.Argv) == 0 {
		return nil, fmt.Errorf("hermetic: empty Argv")
	}

	args := []string{
		// Mounts. M1 broad host bind; M3 narrows to content-addressed
		// toolchain.
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc/alternatives", "/etc/alternatives",
		"--ro-bind", "/etc/ld.so.conf", "/etc/ld.so.conf",
		"--ro-bind-try", "/etc/ld.so.conf.d", "/etc/ld.so.conf.d",

		// Recreate the merged-/usr symlinks so /bin/sh, /sbin/ldconfig, and
		// the dynamic linker at /lib64/ld-linux-x86-64.so.2 resolve. Without
		// these, ninja's posix_spawn("/bin/sh", ...) returns ENOENT inside
		// the sandbox.
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/sbin", "/sbin",

		// Source / build / prefix.
		"--ro-bind", s.SourceRoot, "/src",
		"--bind", s.BuildDir, "/build",

		// Filesystem ephemera.
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
		"--tmpfs", "/home",
		"--dir", "/home/build",

		// Environment: clear everything, set the minimum needed.
		"--clearenv",
		"--setenv", "PATH", "/usr/bin:/usr/local/bin",
		"--setenv", "HOME", "/home/build",
		"--setenv", "LC_ALL", "C",
		"--setenv", "LANG", "C",
		"--setenv", "SOURCE_DATE_EPOCH", SOURCEDateEpoch,
		"--setenv", "CMAKE_FIND_USE_CMAKE_ENVIRONMENT_PATH", "OFF",
		"--setenv", "CMAKE_FIND_USE_CMAKE_PATH", "OFF",
		"--setenv", "CMAKE_FIND_USE_CMAKE_SYSTEM_PATH", "OFF",
		"--setenv", "CMAKE_FIND_USE_PACKAGE_REGISTRY", "OFF",
		"--setenv", "CMAKE_FIND_USE_SYSTEM_PACKAGE_REGISTRY", "OFF",
		"--setenv", "CMAKE_FIND_USE_PACKAGE_ROOT_PATH", "ON",
		"--setenv", "CMAKE_FIND_USE_SYSTEM_ENVIRONMENT_PATH", "OFF",
		"--setenv", "CMAKE_FIND_PACKAGE_PREFER_CONFIG", "ON",

		// Process attributes.
		"--unshare-all",
		"--share-net", // cmake doesn't need network but bwrap fails on
		// some kernels without --share-net under unprivileged user
		// namespaces. M3 removes this.
		"--die-with-parent",
		"--new-session",
	}

	if s.PrefixDir != "" {
		args = append(args, "--ro-bind", s.PrefixDir, "/opt/prefix")
		args = append(args, "--setenv", "CMAKE_PREFIX_PATH", "/opt/prefix")
	}

	for k, v := range s.ExtraSetEnv {
		args = append(args, "--setenv", k, v)
	}

	args = append(args, "--")
	args = append(args, s.Argv...)
	return args, nil
}

// Run builds the argv and execs bwrap. Inherits the calling process's
// stdout/stderr if the Sandbox doesn't override them.
func (s Sandbox) Run(ctx context.Context) error {
	args, err := s.Build()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	if s.Stdout != nil {
		cmd.Stdout = s.Stdout
	}
	if s.Stderr != nil {
		cmd.Stderr = s.Stderr
	}
	return cmd.Run()
}
