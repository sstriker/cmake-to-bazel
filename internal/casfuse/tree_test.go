package casfuse

import (
	"context"
	"errors"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

// helperBuildSubDir packs a Directory into proto bytes + returns
// the digest hash (the fake CAS server keys by hash). All test
// blobs go into the returned map.
func helperBuildSubDir(t *testing.T, dir *repb.Directory) (string, []byte) {
	t.Helper()
	body, err := proto.Marshal(dir)
	if err != nil {
		t.Fatal(err)
	}
	return hashOf(body), body
}

// TestTree_LookupRootDir asserts an empty path returns the root
// Directory.
func TestTree_LookupRootDir(t *testing.T) {
	root := &repb.Directory{
		Files: []*repb.FileNode{{Name: "x", Digest: &repb.Digest{Hash: "h", SizeBytes: 0}}},
	}
	rootHash, rootBytes := helperBuildSubDir(t, root)
	client, teardown := startFakeCAS(t, map[string][]byte{rootHash: rootBytes})
	defer teardown()

	tree := NewTree(client, Digest{Hash: rootHash, Size: int64(len(rootBytes))})
	got, err := tree.Lookup(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != EntryDir || len(got.Dir.Files) != 1 || got.Dir.Files[0].Name != "x" {
		t.Errorf("got %+v", got)
	}
}

// TestTree_LookupNestedFile walks two directory levels into a
// CAS tree and asserts the final FileNode resolves correctly.
func TestTree_LookupNestedFile(t *testing.T) {
	leafDir := &repb.Directory{
		Files: []*repb.FileNode{{Name: "leaf.h", Digest: &repb.Digest{Hash: "leafhash", SizeBytes: 7}}},
	}
	leafHash, leafBytes := helperBuildSubDir(t, leafDir)

	libDir := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			{Name: "include", Digest: &repb.Digest{Hash: leafHash, SizeBytes: int64(len(leafBytes))}},
		},
	}
	libHash, libBytes := helperBuildSubDir(t, libDir)

	root := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			{Name: "lib", Digest: &repb.Digest{Hash: libHash, SizeBytes: int64(len(libBytes))}},
		},
	}
	rootHash, rootBytes := helperBuildSubDir(t, root)

	client, teardown := startFakeCAS(t, map[string][]byte{
		rootHash: rootBytes,
		libHash:  libBytes,
		leafHash: leafBytes,
	})
	defer teardown()

	tree := NewTree(client, Digest{Hash: rootHash, Size: int64(len(rootBytes))})
	got, err := tree.Lookup(context.Background(), "lib/include/leaf.h")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != EntryFile || got.FileNode.Name != "leaf.h" {
		t.Errorf("got %+v", got)
	}
}

// TestTree_LookupNotFound checks the missing-component error.
func TestTree_LookupNotFound(t *testing.T) {
	rootHash, rootBytes := helperBuildSubDir(t, &repb.Directory{})
	client, teardown := startFakeCAS(t, map[string][]byte{rootHash: rootBytes})
	defer teardown()

	tree := NewTree(client, Digest{Hash: rootHash, Size: int64(len(rootBytes))})
	_, err := tree.Lookup(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestTree_LookupTraverseFileFails checks that walking *through*
// a file (e.g. "file.txt/more") errors with NotADirectory.
func TestTree_LookupTraverseFileFails(t *testing.T) {
	root := &repb.Directory{
		Files: []*repb.FileNode{{Name: "file.txt", Digest: &repb.Digest{Hash: "h", SizeBytes: 0}}},
	}
	rootHash, rootBytes := helperBuildSubDir(t, root)
	client, teardown := startFakeCAS(t, map[string][]byte{rootHash: rootBytes})
	defer teardown()

	tree := NewTree(client, Digest{Hash: rootHash, Size: int64(len(rootBytes))})
	_, err := tree.Lookup(context.Background(), "file.txt/extra")
	if !errors.Is(err, ErrNotADirectory) {
		t.Errorf("expected ErrNotADirectory, got %v", err)
	}
}

// TestTree_DirectoryCacheHitsAvoidRefetch verifies the in-memory
// cache: walking the same path twice should issue exactly one
// CAS GetDirectory per unique digest.
func TestTree_DirectoryCacheHitsAvoidRefetch(t *testing.T) {
	leafDir := &repb.Directory{Files: []*repb.FileNode{{Name: "f", Digest: &repb.Digest{Hash: "fh", SizeBytes: 0}}}}
	leafHash, leafBytes := helperBuildSubDir(t, leafDir)
	root := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			{Name: "sub", Digest: &repb.Digest{Hash: leafHash, SizeBytes: int64(len(leafBytes))}},
		},
	}
	rootHash, rootBytes := helperBuildSubDir(t, root)
	client, teardown := startFakeCAS(t, map[string][]byte{rootHash: rootBytes, leafHash: leafBytes})
	defer teardown()

	tree := NewTree(client, Digest{Hash: rootHash, Size: int64(len(rootBytes))})
	for i := 0; i < 3; i++ {
		_, err := tree.Lookup(context.Background(), "sub/f")
		if err != nil {
			t.Fatal(err)
		}
	}
	// Cache should have exactly two entries (root, sub).
	tree.mu.Lock()
	got := len(tree.dir)
	tree.mu.Unlock()
	if got != 2 {
		t.Errorf("dir cache size = %d, want 2 (root + sub)", got)
	}
}

func TestSplitPath(t *testing.T) {
	cases := map[string][]string{
		"":          nil,
		"/":         nil,
		"a":         {"a"},
		"a/b":       {"a", "b"},
		"/a/b/":     {"a", "b"},
		"a/b/c.txt": {"a", "b", "c.txt"},
	}
	for in, want := range cases {
		got := splitPath(in)
		if len(got) != len(want) {
			t.Errorf("splitPath(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("splitPath(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}
