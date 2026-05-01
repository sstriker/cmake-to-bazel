package casfuse

// Pack a local on-disk directory tree into REAPI CAS Directory
// protos + companion blob map. Used by write-a to compute real
// CAS Directory digests from a --source-cache tree without
// needing a live CAS endpoint at render time, and by the
// fake-CAS test fixtures to populate themselves with the same
// content the daemon will serve.
//
// Output is a "synthetic CAS index": rootDigest + map[hash]bytes
// for every blob that appears under the root. Callers either
// write that index into a fake CAS server (tests) or upload it
// to a real CAS via PushBlob (production via bst source push,
// PR #59).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

// PackedTree is the result of packing an on-disk directory.
// RootDigest names the top-level Directory; Blobs maps every
// referenced blob's hex hash → its bytes (Directory protos and
// file contents alike).
type PackedTree struct {
	RootDigest Digest
	Blobs      map[string][]byte
}

// PackDir walks the on-disk tree rooted at root and returns the
// CAS encoding. Symlinks are recorded as Symlink entries in their
// parent Directory (target is the literal symlink target — no
// resolution).
//
// File / directory ordering inside each Directory follows the
// REAPI canonical-ordering requirement (lexicographic by name,
// separately for Files / Directories / Symlinks) so digests are
// stable across runs.
func PackDir(root string) (PackedTree, error) {
	pt := PackedTree{Blobs: map[string][]byte{}}
	d, err := packDir(root, &pt)
	if err != nil {
		return PackedTree{}, err
	}
	pt.RootDigest = d
	return pt, nil
}

func packDir(path string, pt *PackedTree) (Digest, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return Digest{}, fmt.Errorf("readdir %s: %w", path, err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	dir := &repb.Directory{}
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		// Use Lstat so symlinks aren't resolved to their target.
		info, err := os.Lstat(full)
		if err != nil {
			return Digest{}, fmt.Errorf("lstat %s: %w", full, err)
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			tgt, err := os.Readlink(full)
			if err != nil {
				return Digest{}, fmt.Errorf("readlink %s: %w", full, err)
			}
			dir.Symlinks = append(dir.Symlinks, &repb.SymlinkNode{
				Name:   e.Name(),
				Target: tgt,
			})
		case e.IsDir():
			subDigest, err := packDir(full, pt)
			if err != nil {
				return Digest{}, err
			}
			dir.Directories = append(dir.Directories, &repb.DirectoryNode{
				Name:   e.Name(),
				Digest: subDigest.toProto(),
			})
		default:
			body, err := os.ReadFile(full)
			if err != nil {
				return Digest{}, fmt.Errorf("read %s: %w", full, err)
			}
			h := sha256.Sum256(body)
			hash := hex.EncodeToString(h[:])
			pt.Blobs[hash] = body
			dir.Files = append(dir.Files, &repb.FileNode{
				Name:         e.Name(),
				Digest:       &repb.Digest{Hash: hash, SizeBytes: int64(len(body))},
				IsExecutable: info.Mode()&0o111 != 0,
			})
		}
	}
	body, err := proto.Marshal(dir)
	if err != nil {
		return Digest{}, fmt.Errorf("marshal Directory at %s: %w", path, err)
	}
	h := sha256.Sum256(body)
	hash := hex.EncodeToString(h[:])
	pt.Blobs[hash] = body
	return Digest{Hash: hash, Size: int64(len(body))}, nil
}
