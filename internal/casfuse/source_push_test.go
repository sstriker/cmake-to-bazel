package casfuse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSourcePushRoundtrip simulates the full source-push →
// cas-fuse pipeline at the library level: pack a tree, push
// every blob, then read the same tree back through Tree (which
// is what cas-fuse serves to FUSE clients). Verifies that the
// REAPI wire format we emit + consume is internally consistent
// — the dev-side counterpart of CI's e2e-source-push job which
// runs the same flow against real buildbarn.
func TestSourcePushRoundtrip(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.c"), []byte("int main(){return 0;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "include", "hdr.h"), []byte("#pragma once\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pack on the producer side.
	pt, err := PackDir(src)
	if err != nil {
		t.Fatal(err)
	}

	// Empty CAS, then push every blob across.
	client, teardown := startFakeCAS(t, map[string][]byte{})
	defer teardown()
	for hash, body := range pt.Blobs {
		d := Digest{Hash: hash, Size: int64(len(body))}
		if err := client.PushBlob(context.Background(), d, body); err != nil {
			t.Fatalf("PushBlob(%s): %v", hash, err)
		}
	}

	// Consumer side: walk through Tree and compare against the
	// original on-disk content.
	tree := NewTree(client, pt.RootDigest)
	got, err := tree.Lookup(context.Background(), "main.c")
	if err != nil {
		t.Fatal(err)
	}
	body, err := tree.ReadFile(context.Background(), got.FileNode)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "int main(){return 0;}\n" {
		t.Errorf("main.c body mismatch: %q", body)
	}

	got, err = tree.Lookup(context.Background(), "include/hdr.h")
	if err != nil {
		t.Fatal(err)
	}
	body, err = tree.ReadFile(context.Background(), got.FileNode)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "#pragma once\n" {
		t.Errorf("include/hdr.h body mismatch: %q", body)
	}
}
