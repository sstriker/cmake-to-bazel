package cas

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

// MaterializeDirectory walks the Directory tree rooted at d (in CAS)
// and writes every entry to dst on the local filesystem. The directory
// is created (and its parents) if absent; existing entries at the same
// paths are overwritten.
//
// Used by M3d's source-CAS resolver to drop a Buildbarn-resident
// source tree into a per-element checkout dir, and by anything else
// that needs to project a CAS Directory back to disk for a tool that
// reads files (cmake, the converter, ...).
//
// Returns ErrMissingBlob (wrapping ErrNotFound) when a referenced
// blob is absent from the store, so callers can distinguish "stale
// digest" from real I/O failures.
func MaterializeDirectory(ctx context.Context, store Store, d *Digest, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("materialize: mkdir %s: %w", dst, err)
	}
	return materializeDirRecurse(ctx, store, d, dst)
}

// ErrMissingBlob wraps ErrNotFound with the digest of the missing
// blob so callers can report a precise error.
type ErrMissingBlob struct {
	Digest *Digest
	Err    error
}

func (e *ErrMissingBlob) Error() string {
	return fmt.Sprintf("missing blob %s: %v", DigestString(e.Digest), e.Err)
}
func (e *ErrMissingBlob) Unwrap() error { return e.Err }

func materializeDirRecurse(ctx context.Context, store Store, d *Digest, dst string) error {
	body, err := store.GetBlob(ctx, d)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &ErrMissingBlob{Digest: d, Err: err}
		}
		return err
	}
	dir := &repb.Directory{}
	if err := proto.Unmarshal(body, dir); err != nil {
		return fmt.Errorf("materialize: unmarshal directory %s: %w", DigestString(d), err)
	}

	for _, f := range dir.Files {
		body, err := store.GetBlob(ctx, f.Digest)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return &ErrMissingBlob{Digest: f.Digest, Err: err}
			}
			return err
		}
		mode := os.FileMode(0o644)
		if f.IsExecutable {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dst, f.Name), body, mode); err != nil {
			return fmt.Errorf("materialize: write %s: %w", filepath.Join(dst, f.Name), err)
		}
	}
	for _, sl := range dir.Symlinks {
		path := filepath.Join(dst, sl.Name)
		if err := os.Symlink(sl.Target, path); err != nil {
			return fmt.Errorf("materialize: symlink %s -> %s: %w", path, sl.Target, err)
		}
	}
	for _, sub := range dir.Directories {
		next := filepath.Join(dst, sub.Name)
		if err := os.MkdirAll(next, 0o755); err != nil {
			return fmt.Errorf("materialize: mkdir %s: %w", next, err)
		}
		if err := materializeDirRecurse(ctx, store, sub.Digest, next); err != nil {
			return err
		}
	}
	return nil
}
