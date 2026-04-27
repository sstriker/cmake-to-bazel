package cas

import (
	"os"
	"path/filepath"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
)

func TestPackDir_Empty(t *testing.T) {
	dir := t.TempDir()
	tree, err := PackDir(dir)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	if len(tree.Root.Files) != 0 || len(tree.Root.Directories) != 0 || len(tree.Root.Symlinks) != 0 {
		t.Fatalf("empty dir should have no children, got %+v", tree.Root)
	}
	// Empty Directory proto serializes to zero bytes; digest matches EmptyDigest.
	if !DigestEqual(tree.RootDigest, EmptyDigest()) {
		t.Fatalf("empty root digest mismatch: got %s want %s",
			DigestString(tree.RootDigest), DigestString(EmptyDigest()))
	}
}

func TestPackDir_DeterministicAcrossRuns(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.txt":          "hello",
		"sub/b.txt":      "world",
		"sub/inner/c.go": "package x\n",
		"z.cmake":        "set(X 1)\n",
	})

	first, err := PackDir(dir)
	if err != nil {
		t.Fatalf("first PackDir: %v", err)
	}
	second, err := PackDir(dir)
	if err != nil {
		t.Fatalf("second PackDir: %v", err)
	}
	if !DigestEqual(first.RootDigest, second.RootDigest) {
		t.Fatalf("root digest unstable: %s vs %s",
			DigestString(first.RootDigest), DigestString(second.RootDigest))
	}
	if len(first.Files) != len(second.Files) {
		t.Fatalf("file count drifted: %d vs %d", len(first.Files), len(second.Files))
	}
}

func TestPackDir_LeafEditPropagatesUp(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"sub/inner/leaf.txt": "before",
		"unrelated.txt":      "stays",
	})
	before, err := PackDir(dir)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "sub", "inner", "leaf.txt"), []byte("after"), 0o644); err != nil {
		t.Fatalf("rewrite leaf: %v", err)
	}
	after, err := PackDir(dir)
	if err != nil {
		t.Fatalf("re-PackDir: %v", err)
	}

	if DigestEqual(before.RootDigest, after.RootDigest) {
		t.Fatalf("root digest should change after leaf edit, got %s == %s",
			DigestString(before.RootDigest), DigestString(after.RootDigest))
	}

	// The unrelated subtree's contents shouldn't have moved — its file
	// digest is unchanged. Locate the file by name in Root.
	beforeFile := findFile(before.Root.Files, "unrelated.txt")
	afterFile := findFile(after.Root.Files, "unrelated.txt")
	if beforeFile == nil || afterFile == nil {
		t.Fatalf("unrelated.txt missing: before=%v after=%v", beforeFile, afterFile)
	}
	if !DigestEqual(beforeFile.Digest, afterFile.Digest) {
		t.Fatalf("unrelated.txt digest changed: %s vs %s",
			DigestString(beforeFile.Digest), DigestString(afterFile.Digest))
	}
}

func TestPackDir_ChildrenSortedByName(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"z.txt": "z",
		"a.txt": "a",
		"m.txt": "m",
	})
	tree, err := PackDir(dir)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	want := []string{"a.txt", "m.txt", "z.txt"}
	if got := len(tree.Root.Files); got != len(want) {
		t.Fatalf("file count: got %d want %d", got, len(want))
	}
	for i, name := range want {
		if got := tree.Root.Files[i].Name; got != name {
			t.Fatalf("file[%d]: got %q want %q", i, got, name)
		}
	}
}

func TestPackDir_ExecutableBit(t *testing.T) {
	dir := t.TempDir()
	exec := filepath.Join(dir, "go.sh")
	if err := os.WriteFile(exec, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	plain := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
		t.Fatalf("write plain: %v", err)
	}

	tree, err := PackDir(dir)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}

	for _, f := range tree.Root.Files {
		switch f.Name {
		case "go.sh":
			if !f.IsExecutable {
				t.Errorf("go.sh: IsExecutable should be true")
			}
		case "data.txt":
			if f.IsExecutable {
				t.Errorf("data.txt: IsExecutable should be false")
			}
		}
	}
}

func TestPackDir_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tree, err := PackDir(dir)
	if err != nil {
		t.Fatalf("PackDir: %v", err)
	}
	if len(tree.Root.Symlinks) != 1 {
		t.Fatalf("expected 1 symlink, got %d", len(tree.Root.Symlinks))
	}
	if tree.Root.Symlinks[0].Name != "link" || tree.Root.Symlinks[0].Target != "target.txt" {
		t.Errorf("symlink content: %+v", tree.Root.Symlinks[0])
	}
}

func TestPackDir_AsReapiTreeIsDeterministic(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a/b.txt": "B",
		"a/c.txt": "C",
		"d.txt":   "D",
	})
	t1, _ := PackDir(dir)
	t2, _ := PackDir(dir)

	tree1Bytes, err := MarshalDeterministic(t1.AsReapiTree())
	if err != nil {
		t.Fatalf("marshal tree1: %v", err)
	}
	tree2Bytes, err := MarshalDeterministic(t2.AsReapiTree())
	if err != nil {
		t.Fatalf("marshal tree2: %v", err)
	}
	if string(tree1Bytes) != string(tree2Bytes) {
		t.Fatalf("Tree proto not stable across runs: %d vs %d bytes", len(tree1Bytes), len(tree2Bytes))
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return dir
}

func findFile(files []*repb.FileNode, name string) *repb.FileNode {
	for _, f := range files {
		if f.Name == name {
			return f
		}
	}
	return nil
}
