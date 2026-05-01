// Package tracecache stores the B→A process traces the
// trace-driven autotools converter consumes (see
// docs/trace-driven-autotools.md). The cache is keyed by the
// element's srckey + the tracer version, so all nodes that run
// the same source under the same tracer converge on the same
// stored trace.
//
// Spike scope: a local-filesystem cache. The production
// implementation backs onto the REAPI Action Cache (action key
// digest = hash(srckey, tracer_version)) so the cache is shared
// across distributed builders. The Register / Lookup contract
// here is the same shape an REAPI-backed cache would expose;
// migration is a re-implementation of the two functions
// rather than an API change.
//
// Layout: <root>/<srckey>/<tracer_version>/trace.log. srckey
// and tracer_version both need to be filesystem-safe; callers
// pass already-sanitized values.
package tracecache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Key is the cache lookup key. Two builds with the same srckey +
// tracer_version produce the same trace bytes (modulo
// non-determinism the tracer version commits to filtering),
// which is why both fields gate the AC entry.
type Key struct {
	// SrcKey is the content-addressed key of the element's source
	// tree (matches the @src_<key>// repo the cas-fuse layer
	// already uses). Same srckey across nodes => same source
	// inputs to the build.
	SrcKey string
	// TracerVersion identifies the build-tracer's wire-format
	// promise. Bumping the tracer's filtering / output shape
	// requires bumping this so old traces don't leak into a new
	// converter.
	TracerVersion string
}

// ErrNotFound is returned by Lookup when the cache has no entry
// for the requested key.
var ErrNotFound = errors.New("tracecache: no entry for key")

// Register stores a trace artifact under the given key. Reads
// tracePath; writes <root>/<key.SrcKey>/<key.TracerVersion>/trace.log.
// Overwrites any existing entry — last-write-wins, matching the
// semantics REAPI Action Cache uses for action results that
// re-execute.
func Register(root string, key Key, tracePath string) error {
	if err := key.validate(); err != nil {
		return err
	}
	dir := filepath.Join(root, key.SrcKey, key.TracerVersion)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "trace.log")

	in, err := os.Open(tracePath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// Lookup writes the cached trace for key to outPath. Returns
// ErrNotFound if no entry exists. outPath's parent directory
// must already exist; Lookup creates the file but not its
// containing dir.
func Lookup(root string, key Key, outPath string) error {
	if err := key.validate(); err != nil {
		return err
	}
	src := filepath.Join(root, key.SrcKey, key.TracerVersion, "trace.log")
	in, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// Has reports whether a trace exists for the given key. Lighter
// than Lookup when the caller doesn't need the bytes.
func Has(root string, key Key) (bool, error) {
	if err := key.validate(); err != nil {
		return false, err
	}
	path := filepath.Join(root, key.SrcKey, key.TracerVersion, "trace.log")
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// validate rejects keys whose components could escape the
// cache root via path traversal. Defense-in-depth — callers are
// expected to pass already-sanitized digest-shaped values.
func (k Key) validate() error {
	for _, s := range []string{k.SrcKey, k.TracerVersion} {
		if s == "" {
			return fmt.Errorf("tracecache: empty key component")
		}
		if s != filepath.Base(s) {
			return fmt.Errorf("tracecache: key component %q contains a path separator", s)
		}
	}
	return nil
}
