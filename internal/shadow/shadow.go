// Package shadow materializes the path-only mirror of a CMake source tree
// that the architecture relies on for cache-key invariance.
//
// Build(src, dst, m) walks src and creates dst as an exact replica of
// directories + symlinks + filenames + permissions. File content is preserved
// only for paths matched by Matcher m; every other file becomes a zero-byte
// stub. The trick is that CMake's configure phase only `access(R_OK)`s source
// files (verified at Source/cmSourceFile.cxx:184; see docs/cmake_analysis.md) — so
// from cmake's perspective the stub is indistinguishable from the real file.
//
// Call Build before pointing cmakerun.Configure at the destination via
// Options.HostSourceRoot. Lower's header discovery walks the same destination
// (extension-based; .h presence is enough), so editing a .c file's content
// outside the allowlist leaves the conversion output byte-identical — that's
// the cache-key pivot the orchestrator (M3) leans on.
package shadow

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Build creates dst as a path-only mirror of src. dst must not exist; Build
// creates it. Returns an error if any walk step fails.
//
// Symlinks: copied as symlinks (not followed). Their target string is taken
// verbatim; the orchestrator must ensure resolved-relative-target validity in
// its sandbox layout. M1 hello-world has none.
func Build(src, dst string, m Matcher) error {
	if m == nil {
		return fmt.Errorf("shadow.Build: nil Matcher")
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("shadow.Build: dst already exists: %s", dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)

		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())

		case info.Mode().IsRegular():
			mode := info.Mode().Perm()
			if m.Allowed(rel) {
				return copyFile(p, target, mode)
			}
			return writeStub(target, mode)
		}
		// Sockets, pipes, devices: skip silently. CMake source trees don't
		// legitimately contain these.
		return nil
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeStub(dst string, mode os.FileMode) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	return f.Close()
}
