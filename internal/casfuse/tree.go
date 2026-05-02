package casfuse

// Virtual tree types: a backend-agnostic model of a CAS Directory
// rooted at some digest. Used by the FUSE adapter (fs_linux.go)
// to answer kernel lookups, but also testable in isolation
// without an actual mount — see tree_test.go.
//
// Lookups walk the path one segment at a time, fetching child
// Directory protos lazily via the CASClient. A small in-memory
// cache (per Tree) prevents re-fetching the same Directory on
// repeated walks.

import (
	"context"
	"fmt"
	"sync"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
)

// Tree is a virtual filesystem rooted at a single CAS Directory
// digest. The directory protos are fetched lazily and cached;
// file blob contents are fetched on Read (no caching at this
// layer — the kernel page cache handles that for FUSE).
type Tree struct {
	client *CASClient
	root   Digest

	mu  sync.Mutex
	dir map[string]*repb.Directory // cache by digest.String()
}

// NewTree returns a Tree rooted at the given digest.
func NewTree(client *CASClient, root Digest) *Tree {
	return &Tree{client: client, root: root, dir: map[string]*repb.Directory{}}
}

// Root returns the root Directory proto, fetching on first call.
func (t *Tree) Root(ctx context.Context) (*repb.Directory, error) {
	return t.fetchDir(ctx, t.root)
}

func (t *Tree) fetchDir(ctx context.Context, d Digest) (*repb.Directory, error) {
	t.mu.Lock()
	if cached, ok := t.dir[d.String()]; ok {
		t.mu.Unlock()
		return cached, nil
	}
	t.mu.Unlock()

	dir, err := t.client.GetDirectory(ctx, d)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.dir[d.String()] = dir
	t.mu.Unlock()
	return dir, nil
}

// Lookup walks the path (POSIX-style, "/"-separated, no leading
// slash) from the root and returns either a *repb.FileNode or
// *repb.DirectoryNode for the final segment, plus a flag for
// which it is. Empty path returns the root Directory wrapped as
// a synthetic DirectoryNode.
//
// Errors:
//   - missing intermediate component: ErrNotFound
//   - intermediate is a file (not a dir): ErrNotADirectory
//   - any underlying CAS error: wrapped and returned.
func (t *Tree) Lookup(ctx context.Context, path string) (Entry, error) {
	cur, err := t.Root(ctx)
	if err != nil {
		return Entry{}, err
	}
	if path == "" {
		return Entry{Kind: EntryDir, Dir: cur}, nil
	}

	segments := splitPath(path)
	for i, seg := range segments {
		// Try directory first.
		if subNode := findDir(cur, seg); subNode != nil {
			subDigest := Digest{Hash: subNode.Digest.Hash, Size: subNode.Digest.SizeBytes}
			subDir, err := t.fetchDir(ctx, subDigest)
			if err != nil {
				return Entry{}, err
			}
			if i == len(segments)-1 {
				return Entry{Kind: EntryDir, Dir: subDir, DirNode: subNode}, nil
			}
			cur = subDir
			continue
		}
		// Try file.
		if fileNode := findFile(cur, seg); fileNode != nil {
			if i != len(segments)-1 {
				return Entry{}, fmt.Errorf("%s: %w", seg, ErrNotADirectory)
			}
			return Entry{Kind: EntryFile, FileNode: fileNode}, nil
		}
		// Try symlink.
		if linkNode := findSymlink(cur, seg); linkNode != nil {
			if i != len(segments)-1 {
				return Entry{}, fmt.Errorf("%s: %w", seg, ErrNotADirectory)
			}
			return Entry{Kind: EntrySymlink, SymlinkNode: linkNode}, nil
		}
		return Entry{}, fmt.Errorf("%s: %w", seg, ErrNotFound)
	}
	// Unreachable — segments always non-empty here.
	return Entry{}, ErrNotFound
}

// ReadFile returns a file's bytes. Caller is expected to have
// resolved a FileNode via Lookup.
func (t *Tree) ReadFile(ctx context.Context, fn *repb.FileNode) ([]byte, error) {
	d := Digest{Hash: fn.Digest.Hash, Size: fn.Digest.SizeBytes}
	return t.client.ReadBlob(ctx, d)
}

// EntryKind discriminates Entry contents.
type EntryKind int

const (
	EntryDir EntryKind = iota + 1
	EntryFile
	EntrySymlink
)

// Entry is a Lookup result. Only one of Dir / FileNode /
// SymlinkNode is set per Kind.
type Entry struct {
	Kind        EntryKind
	Dir         *repb.Directory     // EntryDir: the directory's contents
	DirNode     *repb.DirectoryNode // EntryDir: nil for the root, set for subdirs (carries the digest pointer)
	FileNode    *repb.FileNode      // EntryFile
	SymlinkNode *repb.SymlinkNode   // EntrySymlink
}

// Errors. Wrapped %w-style by Lookup so callers can errors.Is them.
var (
	ErrNotFound      = errNotFound{}
	ErrNotADirectory = errNotADir{}
)

type errNotFound struct{}

func (errNotFound) Error() string { return "no such file or directory" }

type errNotADir struct{}

func (errNotADir) Error() string { return "not a directory" }

// splitPath strips leading/trailing slashes and splits on "/".
// Empty input → empty slice.
func splitPath(p string) []string {
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	for len(p) > 0 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	if p == "" {
		return nil
	}
	out := []string{}
	cur := ""
	for _, c := range p {
		if c == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	out = append(out, cur)
	return out
}

func findDir(d *repb.Directory, name string) *repb.DirectoryNode {
	for _, n := range d.Directories {
		if n.Name == name {
			return n
		}
	}
	return nil
}

func findFile(d *repb.Directory, name string) *repb.FileNode {
	for _, n := range d.Files {
		if n.Name == name {
			return n
		}
	}
	return nil
}

func findSymlink(d *repb.Directory, name string) *repb.SymlinkNode {
	for _, n := range d.Symlinks {
		if n.Name == name {
			return n
		}
	}
	return nil
}
