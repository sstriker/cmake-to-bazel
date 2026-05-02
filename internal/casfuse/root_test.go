package casfuse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRoot_LookupPathServesDigest(t *testing.T) {
	// Pack a tiny on-disk tree to get realistic digest +
	// blobs map.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pt, err := PackDir(src)
	if err != nil {
		t.Fatal(err)
	}

	client, teardown := startFakeCAS(t, pt.Blobs)
	defer teardown()

	root := NewRoot(client)

	// Empty instance shape: blobs/directory/<digest>/...
	entry, _, err := root.LookupPath(context.Background(),
		"blobs/directory/"+pt.RootDigest.String()+"/f.txt")
	if err != nil {
		t.Fatalf("LookupPath: %v", err)
	}
	if entry.Kind != EntryFile || entry.FileNode.Name != "f.txt" {
		t.Errorf("got %+v", entry)
	}

	// Named-instance shape: foo/blobs/directory/<digest>/f.txt
	entry, _, err = root.LookupPath(context.Background(),
		"foo/blobs/directory/"+pt.RootDigest.String()+"/f.txt")
	if err != nil {
		t.Fatalf("named-instance LookupPath: %v", err)
	}
	if entry.Kind != EntryFile {
		t.Errorf("named-instance got %+v", entry)
	}

	// Bare digest path (no sub-path) returns the root Directory.
	entry, _, err = root.LookupPath(context.Background(),
		"blobs/directory/"+pt.RootDigest.String())
	if err != nil {
		t.Fatalf("bare-digest LookupPath: %v", err)
	}
	if entry.Kind != EntryDir {
		t.Errorf("bare-digest got %+v", entry)
	}
}

func TestRoot_LookupPathInvalid(t *testing.T) {
	client, teardown := startFakeCAS(t, nil)
	defer teardown()
	root := NewRoot(client)

	cases := []string{
		"",                 // empty
		"random/garbage",   // missing /blobs/directory
		"blobs/file/abc-1", // wrong type segment
		"blobs/directory/not-a-valid-digest-form", // size not numeric
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, _, err := root.LookupPath(context.Background(), c)
			if !errors.Is(err, ErrInvalidPath) {
				t.Errorf("path %q: expected ErrInvalidPath, got %v", c, err)
			}
		})
	}
}

func TestRoot_TreeForSharesCacheAcrossLookups(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pt, err := PackDir(src)
	if err != nil {
		t.Fatal(err)
	}
	client, teardown := startFakeCAS(t, pt.Blobs)
	defer teardown()
	root := NewRoot(client)
	t1 := root.TreeFor("", pt.RootDigest)
	t2 := root.TreeFor("", pt.RootDigest)
	if t1 != t2 {
		t.Errorf("TreeFor returned a fresh Tree on second call; expected cache hit")
	}
}
