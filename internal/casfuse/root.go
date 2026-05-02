package casfuse

// Multi-digest virtual root.
//
// A single mount serves any CAS Directory by digest, addressed
// at <instance>/blobs/directory/<hash>-<size>/...  This mirrors
// bb_clientd's path layout so the daemon can serve all the
// per-source repos a Bazel build references with one mount
// (per-repo mounts would mean hundreds at FDSDK scale; macOS
// NFSv4 needs sudo per mount which would be a deal-breaker).
//
// Resolution: the kernel walks
//   <mount>/<instance>/blobs/directory/<hash>-<size>/sub/file.h
// the Root node lazily resolves "<instance>" → instanceNode,
// "blobs" → blobsNode, "directory" → directoryNode, the
// "<hash>-<size>" segment is parsed as a Digest and a Tree
// rooted at it is constructed on the fly. Subsequent components
// are served by the Tree's underlying Directory walk.

import (
	"context"
	"errors"
	"sync"
)

// Root is the multi-digest entry point. It owns a CASClient and
// hands off subtree walks to per-digest Tree instances. A small
// cache of (instance, digest) → *Tree avoids re-creating
// per-digest state on every kernel lookup.
type Root struct {
	client *CASClient

	mu    sync.Mutex
	trees map[string]*Tree // key: "<instance>/<digest>"
}

// NewRoot wraps a CASClient. The returned Root is consumed by
// the FUSE adapter (Linux) or used directly in tests.
func NewRoot(client *CASClient) *Root {
	return &Root{client: client, trees: map[string]*Tree{}}
}

// TreeFor returns a Tree rooted at the given (instance, digest).
// Cached so repeated lookups for the same source share Directory
// caches.
//
// instance is currently a name kept on the Root via the embedded
// CASClient — multi-instance support (one daemon serving many
// REAPI instance names) is a follow-up; this signature is
// future-proofed for it.
func (r *Root) TreeFor(_ string, d Digest) *Tree {
	key := d.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.trees[key]; ok {
		return t
	}
	t := NewTree(r.client, d)
	r.trees[key] = t
	return t
}

// LookupPath walks the multi-digest virtual root by absolute path
// from the mount point. Path shape:
//
//	<instance>/blobs/directory/<hash>-<size>[/<sub-path>...]
//
// The <instance> segment is the REAPI instance name ("" → the
// daemon's default instance from the client). For the empty
// instance shape, the path starts with "blobs/...".
//
// Returns ErrInvalidPath when the path doesn't conform.
func (r *Root) LookupPath(ctx context.Context, p string) (Entry, *Tree, error) {
	segs := splitPath(p)
	// Allow either "blobs/..." (default instance) or
	// "<instance>/blobs/..." (named instance).
	var instance string
	if len(segs) > 0 && segs[0] != "blobs" {
		instance = segs[0]
		segs = segs[1:]
	}
	if len(segs) < 3 || segs[0] != "blobs" || segs[1] != "directory" {
		return Entry{}, nil, ErrInvalidPath
	}
	d, err := ParseDigest(segs[2])
	if err != nil {
		return Entry{}, nil, ErrInvalidPath
	}
	tree := r.TreeFor(instance, d)
	subPath := ""
	if len(segs) > 3 {
		// Re-join the remainder with "/".
		for i, s := range segs[3:] {
			if i > 0 {
				subPath += "/"
			}
			subPath += s
		}
	}
	entry, err := tree.Lookup(ctx, subPath)
	return entry, tree, err
}

// ErrInvalidPath is returned by LookupPath for paths that don't
// match the <instance>/blobs/directory/<digest>/ layout.
var ErrInvalidPath = errors.New("path not in <instance>/blobs/directory/<hash>-<size>/ shape")
