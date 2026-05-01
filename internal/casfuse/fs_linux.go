//go:build linux

package casfuse

// FUSE adapter — kernel-facing layer that turns the platform-
// agnostic Tree into a mounted filesystem via go-fuse.
//
// Implements the minimum subset of fs.NodeXxx interfaces the
// kernel exercises during typical Bazel repo-rule consumption
// (Lookup, Readdir, Read, Getattr, Readlink). Writes are not
// supported — sources are immutable per design.

import (
	"context"
	"sync"
	"syscall"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// MountOptions configure Mount.
type MountOptions struct {
	// AllowOther — if true, processes other than the daemon's
	// owning UID can read the mount. Required when bazel runs
	// as a different user than the daemon. Default: false.
	AllowOther bool
	// ReadOnly — always true today; struct field reserved for
	// future per-mount tuning.
}

// Mount attaches Tree at mountPoint and returns the running
// server. Caller calls Wait() to block until unmount, or
// Unmount() to tear down.
func Mount(tree *Tree, mountPoint string, opts MountOptions) (*fuse.Server, error) {
	root := &dirNode{tree: tree}
	server, err := fs.Mount(mountPoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			Name:       "cas-fuse",
			FsName:     "cas-fuse",
			AllowOther: opts.AllowOther,
		},
	})
	if err != nil {
		return nil, err
	}
	return server, nil
}

// dirNode is a virtual directory node backed by a CAS Directory
// digest reachable through Tree. The root dirNode addresses the
// tree's root; subdir lookups follow the same pattern.
type dirNode struct {
	fs.Inode
	tree *Tree
	// dir is the underlying Directory proto. For the root, this
	// is fetched on first Lookup; for subdirs, it's set at
	// construction time from the parent's lookup result.
	dir *repb.Directory

	once sync.Once
}

var _ fs.NodeLookuper = (*dirNode)(nil)
var _ fs.NodeReaddirer = (*dirNode)(nil)
var _ fs.NodeGetattrer = (*dirNode)(nil)

func (n *dirNode) ensureDir(ctx context.Context) syscall.Errno {
	var loadErr syscall.Errno
	n.once.Do(func() {
		if n.dir != nil {
			return
		}
		dir, err := n.tree.Root(ctx)
		if err != nil {
			loadErr = syscall.EIO
			return
		}
		n.dir = dir
	})
	return loadErr
}

func (n *dirNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o555
	return 0
}

func (n *dirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if errno := n.ensureDir(ctx); errno != 0 {
		return nil, errno
	}
	if sub := findDir(n.dir, name); sub != nil {
		subDir, err := n.tree.fetchDir(ctx, Digest{Hash: sub.Digest.Hash, Size: sub.Digest.SizeBytes})
		if err != nil {
			return nil, syscall.EIO
		}
		child := &dirNode{tree: n.tree, dir: subDir}
		// Mark once-init as already done — dir is already populated.
		child.once.Do(func() {})
		out.Mode = fuse.S_IFDIR | 0o555
		return n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
	}
	if file := findFile(n.dir, name); file != nil {
		mode := uint32(0o444)
		if file.IsExecutable {
			mode = 0o555
		}
		child := &fileNode{tree: n.tree, fn: file}
		out.Mode = fuse.S_IFREG | mode
		out.Size = uint64(file.Digest.SizeBytes)
		return n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	}
	if link := findSymlink(n.dir, name); link != nil {
		child := &symlinkNode{target: link.Target}
		out.Mode = fuse.S_IFLNK | 0o777
		return n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFLNK}), 0
	}
	return nil, syscall.ENOENT
}

func (n *dirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if errno := n.ensureDir(ctx); errno != 0 {
		return nil, errno
	}
	entries := make([]fuse.DirEntry, 0, len(n.dir.Files)+len(n.dir.Directories)+len(n.dir.Symlinks))
	for _, d := range n.dir.Directories {
		entries = append(entries, fuse.DirEntry{Mode: fuse.S_IFDIR, Name: d.Name})
	}
	for _, f := range n.dir.Files {
		entries = append(entries, fuse.DirEntry{Mode: fuse.S_IFREG, Name: f.Name})
	}
	for _, s := range n.dir.Symlinks {
		entries = append(entries, fuse.DirEntry{Mode: fuse.S_IFLNK, Name: s.Name})
	}
	return fs.NewListDirStream(entries), 0
}

// fileNode is a virtual file node backed by a CAS blob digest.
// Reads pull bytes from CAS on demand — the kernel page cache
// elides repeats within a short window.
type fileNode struct {
	fs.Inode
	tree *Tree
	fn   *repb.FileNode
}

var _ fs.NodeReader = (*fileNode)(nil)
var _ fs.NodeGetattrer = (*fileNode)(nil)
var _ fs.NodeOpener = (*fileNode)(nil)

func (n *fileNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	mode := uint32(0o444)
	if n.fn.IsExecutable {
		mode = 0o555
	}
	out.Mode = fuse.S_IFREG | mode
	out.Size = uint64(n.fn.Digest.SizeBytes)
	return 0
}

func (n *fileNode) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Read-only mount; reject anything but read access.
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		return nil, 0, syscall.EROFS
	}
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *fileNode) Read(ctx context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	body, err := n.tree.ReadFile(ctx, n.fn)
	if err != nil {
		return nil, syscall.EIO
	}
	if off >= int64(len(body)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(body)) {
		end = int64(len(body))
	}
	return fuse.ReadResultData(body[off:end]), 0
}

// symlinkNode is a virtual symlink. The CAS Directory's
// SymlinkNode carries the target string verbatim; we just hand
// it back on Readlink.
type symlinkNode struct {
	fs.Inode
	target string
}

var _ fs.NodeReadlinker = (*symlinkNode)(nil)
var _ fs.NodeGetattrer = (*symlinkNode)(nil)

func (n *symlinkNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFLNK | 0o777
	out.Size = uint64(len(n.target))
	return 0
}

func (n *symlinkNode) Readlink(_ context.Context) ([]byte, syscall.Errno) {
	return []byte(n.target), 0
}
