package casfuse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPackDir_RoundTripThroughTree(t *testing.T) {
	// Build a tiny on-disk tree.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "leaf.h"), []byte("#pragma once\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("hello.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "exec.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pt, err := PackDir(root)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}

	// Spin a fake CAS pre-populated from the packed blobs and walk
	// the tree through Tree to verify content.
	client, teardown := startFakeCAS(t, pt.Blobs)
	defer teardown()
	tree := NewTree(client, pt.RootDigest)

	// hello.txt
	got, err := tree.Lookup(context.Background(), "hello.txt")
	if err != nil {
		t.Fatalf("Lookup hello.txt: %v", err)
	}
	if got.Kind != EntryFile {
		t.Errorf("hello.txt kind = %v, want EntryFile", got.Kind)
	}
	body, err := tree.ReadFile(context.Background(), got.FileNode)
	if err != nil {
		t.Fatalf("ReadFile hello.txt: %v", err)
	}
	if string(body) != "hello\n" {
		t.Errorf("hello.txt body = %q, want \"hello\\n\"", body)
	}

	// sub/leaf.h
	got, err = tree.Lookup(context.Background(), "sub/leaf.h")
	if err != nil {
		t.Fatalf("Lookup sub/leaf.h: %v", err)
	}
	body, err = tree.ReadFile(context.Background(), got.FileNode)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "#pragma once\n" {
		t.Errorf("sub/leaf.h body = %q", body)
	}

	// link → hello.txt
	got, err = tree.Lookup(context.Background(), "link")
	if err != nil {
		t.Fatalf("Lookup link: %v", err)
	}
	if got.Kind != EntrySymlink || got.SymlinkNode.Target != "hello.txt" {
		t.Errorf("link entry = %+v, want symlink → hello.txt", got)
	}

	// exec.sh executable bit recorded
	got, err = tree.Lookup(context.Background(), "exec.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !got.FileNode.IsExecutable {
		t.Errorf("exec.sh should be marked executable")
	}
}

func TestPackDir_DeterministicDigest(t *testing.T) {
	// Same content packed twice should produce the same root
	// digest — this is the property repo-rule cache stability
	// hangs on.
	mk := func() string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a"), []byte("alpha"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b"), []byte("beta"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	a, err := PackDir(mk())
	if err != nil {
		t.Fatal(err)
	}
	b, err := PackDir(mk())
	if err != nil {
		t.Fatal(err)
	}
	if a.RootDigest != b.RootDigest {
		t.Errorf("root digest not deterministic: %s vs %s", a.RootDigest, b.RootDigest)
	}
}
