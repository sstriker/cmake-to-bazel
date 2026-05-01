//go:build linux

package casfuse

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
)

// TestMount_RealMountReadFile mounts a small CAS Directory tree
// under a tempdir and asserts that the kernel actually serves
// the bytes through the FUSE mount. Skipped when fusermount
// isn't available (which it isn't in many container CIs without
// FUSE preinstalled).
//
// CI wiring (per the PR plan): the e2e-cas-fuse-fake job
// installs fuse + fuse3 and runs this. Locally, "make test"
// covers it when the dev box has FUSE.
func TestMount_RealMountReadFile(t *testing.T) {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("FUSE userspace helper not available; install fuse / fuse3 to run this test")
		}
	}

	body := []byte("hello from fuse\n")
	bodyHash := hashOf(body)

	root := &repb.Directory{
		Files: []*repb.FileNode{
			{Name: "hello.txt", Digest: &repb.Digest{Hash: bodyHash, SizeBytes: int64(len(body))}},
		},
	}
	rootHash, rootBytes := helperBuildSubDir(t, root)

	client, teardown := startFakeCAS(t, map[string][]byte{
		rootHash: rootBytes,
		bodyHash: body,
	})
	defer teardown()

	tree := NewTree(client, Digest{Hash: rootHash, Size: int64(len(rootBytes))})

	mountPoint := t.TempDir()
	server, err := Mount(tree, mountPoint, MountOptions{})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = server.Unmount() })

	// Give the mount a moment to settle. go-fuse's Mount returns
	// once the kernel mount call succeeds, but a tiny sleep avoids
	// rare flakes where the very first Stat races kernel setup.
	time.Sleep(50 * time.Millisecond)

	got, err := os.ReadFile(filepath.Join(mountPoint, "hello.txt"))
	if err != nil {
		t.Fatalf("read through mount: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("got %q, want %q", got, body)
	}

	// Directory listing should also work.
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "hello.txt" {
		t.Errorf("listing = %v, want [hello.txt]", names)
	}

	// Bazel's --unix_digest_hash_attribute_name path: the file
	// node should serve XattrDigestName with the file's CAS hash,
	// so Bazel can skip re-digesting bytes it's only reading to
	// hash. Probe-then-read pattern (size first, then value).
	full := filepath.Join(mountPoint, "hello.txt")
	size, err := syscall.Getxattr(full, XattrDigestName, nil)
	if err != nil {
		t.Fatalf("Getxattr probe: %v", err)
	}
	buf := make([]byte, size)
	if _, err := syscall.Getxattr(full, XattrDigestName, buf); err != nil {
		t.Fatalf("Getxattr read: %v", err)
	}
	if string(buf) != bodyHash {
		t.Errorf("xattr digest = %q, want %q", buf, bodyHash)
	}
}

// TestMount_MultiDigestRoot proves the bb_clientd-style virtual
// hierarchy works through a real mount: walk
// <mount>/blobs/directory/<hash>-<size>/ and read the contained
// file, with the FUSE adapter resolving each segment lazily.
func TestMount_MultiDigestRoot(t *testing.T) {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("FUSE userspace helper not available")
		}
	}

	body := []byte("multi-digest mount works\n")
	bodyHash := hashOf(body)
	rootDir := &repb.Directory{
		Files: []*repb.FileNode{
			{Name: "x.txt", Digest: &repb.Digest{Hash: bodyHash, SizeBytes: int64(len(body))}},
		},
	}
	rootHash, rootBytes := helperBuildSubDir(t, rootDir)

	client, teardown := startFakeCAS(t, map[string][]byte{
		rootHash: rootBytes,
		bodyHash: body,
	})
	defer teardown()

	root := NewRoot(client)
	mountPoint := t.TempDir()
	server, err := MountRoot(root, mountPoint, MountOptions{})
	if err != nil {
		t.Fatalf("MountRoot: %v", err)
	}
	t.Cleanup(func() { _ = server.Unmount() })
	time.Sleep(50 * time.Millisecond)

	digestPath := filepath.Join(mountPoint, "blobs", "directory",
		Digest{Hash: rootHash, Size: int64(len(rootBytes))}.String())

	got, err := os.ReadFile(filepath.Join(digestPath, "x.txt"))
	if err != nil {
		t.Fatalf("read through multi-digest mount: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("got %q, want %q", got, body)
	}
}
