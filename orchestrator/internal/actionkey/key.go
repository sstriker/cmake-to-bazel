// Package actionkey computes a content-addressed fingerprint for one
// per-element conversion. Identical fingerprints across runs and machines
// imply the convert-element invocation would produce identical outputs;
// the orchestrator uses that to short-circuit re-runs when nothing
// observable has changed.
//
// What goes into the key:
//
//   - Shadow tree contents (recursive: path + mode + size + sha256 of
//     each regular file). The shadow tree already absorbs content-only
//     edits to non-allowlisted files via zero-byte stubs, so a content-
//     only `.c` change leaves the key untouched.
//   - Imports manifest JSON (verbatim file contents). Cross-element dep
//     fingerprints flow in here.
//   - Synth-prefix tree contents (recursive). When a dep's bundle
//     changes, downstream elements get fresh keys.
//   - Converter binary sha256. Updating the converter invalidates every
//     element's cache.
//
// Out of scope for M3a: toolchain (cmake/ninja/bwrap) version, host
// libc, kernel. M3b's REAPI integration pins those at action-input level.
package actionkey

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Inputs bundles every path/file that contributes to a key. Empty fields
// are tolerated — the corresponding section just doesn't fold into the
// hash. Callers pass the orchestrator-side host paths.
type Inputs struct {
	ShadowDir       string
	ImportsManifest string // path to imports.json (or "" if none)
	PrefixDir       string // synth-prefix root (or "" if none)
	ConverterBin    string // path to convert-element binary
}

// Compute returns the action key as a lowercase hex string of a sha256.
// Inputs that don't exist on disk return errors; the caller decides
// whether that's a cache-miss or a propagation failure.
func Compute(in Inputs) (string, error) {
	h := sha256.New()

	if err := writeSection(h, "shadow", func() error {
		if in.ShadowDir == "" {
			return nil
		}
		return hashDirInto(h, in.ShadowDir)
	}); err != nil {
		return "", err
	}

	if err := writeSection(h, "imports", func() error {
		if in.ImportsManifest == "" {
			return nil
		}
		return hashFileInto(h, in.ImportsManifest)
	}); err != nil {
		return "", err
	}

	if err := writeSection(h, "prefix", func() error {
		if in.PrefixDir == "" {
			return nil
		}
		return hashDirInto(h, in.PrefixDir)
	}); err != nil {
		return "", err
	}

	if err := writeSection(h, "converter", func() error {
		if in.ConverterBin == "" {
			return nil
		}
		return hashFileInto(h, in.ConverterBin)
	}); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeSection prefixes each contributing input with a tagged length-
// delimited header so two separate sections can't alias by collision
// (e.g. an empty shadow + non-empty prefix vs the inverse).
func writeSection(h io.Writer, tag string, contribute func() error) error {
	if _, err := fmt.Fprintf(h, "[section:%s]\n", tag); err != nil {
		return err
	}
	return contribute()
}

func hashDirInto(h io.Writer, root string) error {
	type entry struct {
		Rel  string
		Mode os.FileMode
		Size int64
		Hash []byte
	}
	var entries []entry

	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{Rel: filepath.ToSlash(rel), Mode: info.Mode(), Size: info.Size()}
		switch {
		case info.Mode().IsRegular():
			fh, err := hashFile(p)
			if err != nil {
				return err
			}
			e.Hash = fh
		case info.IsDir():
			// no content; mode + path captures it
		case info.Mode()&os.ModeSymlink != 0:
			// link target as content
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256([]byte(t))
			e.Hash = sum[:]
		}
		entries = append(entries, e)
		return nil
	}); err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Rel < entries[j].Rel })
	for _, e := range entries {
		if _, err := fmt.Fprintf(h, "%s\t%o\t%d\t", e.Rel, e.Mode, e.Size); err != nil {
			return err
		}
		if _, err := h.Write(e.Hash); err != nil {
			return err
		}
		if _, err := h.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return nil
}

func hashFileInto(h io.Writer, path string) error {
	sum, err := hashFile(path)
	if err != nil {
		return err
	}
	rel := filepath.Base(path)
	_, err = fmt.Fprintf(h, "%s\t%x\n", rel, sum)
	return err
}

func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// IsMissingDir reports whether err is an "input directory missing" error.
// Callers can use this to distinguish "first run, nothing to hash" from
// genuine I/O errors.
func IsMissingDir(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
