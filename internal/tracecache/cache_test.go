package tracecache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sstriker/cmake-to-bazel/internal/tracecache"
)

func TestRegisterLookupRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "cache")

	tracePath := filepath.Join(tmp, "trace.log")
	want := []byte("execve(\"/usr/bin/cc\", [\"cc\", \"-O2\"]) = 0\n")
	if err := os.WriteFile(tracePath, want, 0o644); err != nil {
		t.Fatal(err)
	}

	key := tracecache.Key{SrcKey: "abc123", TracerVersion: "v1"}
	if err := tracecache.Register(root, key, tracePath); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if has, err := tracecache.Has(root, key); err != nil || !has {
		t.Fatalf("Has after Register: has=%v err=%v", has, err)
	}

	out := filepath.Join(tmp, "out.log")
	if err := tracecache.Lookup(root, key, out); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("looked-up trace bytes mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestLookupNotFound(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "cache")
	key := tracecache.Key{SrcKey: "missing", TracerVersion: "v1"}

	if has, err := tracecache.Has(root, key); err != nil || has {
		t.Fatalf("Has on empty cache: has=%v err=%v", has, err)
	}

	out := filepath.Join(tmp, "out.log")
	err := tracecache.Lookup(root, key, out)
	if !errors.Is(err, tracecache.ErrNotFound) {
		t.Fatalf("Lookup empty: want ErrNotFound, got %v", err)
	}
}

// TestRegisterRejectsPathTraversal exercises the validate
// guard: a Key whose SrcKey contains "/" or ".." can't escape
// the cache root.
func TestRegisterRejectsPathTraversal(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "cache")
	tracePath := filepath.Join(tmp, "trace.log")
	if err := os.WriteFile(tracePath, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"../escape",
		"sub/key",
		"",
	} {
		t.Run(bad, func(t *testing.T) {
			err := tracecache.Register(root, tracecache.Key{SrcKey: bad, TracerVersion: "v1"}, tracePath)
			if err == nil {
				t.Errorf("expected validate error for %q, got nil", bad)
			}
		})
	}
}

// TestRegisterOverwrites confirms last-write-wins semantics:
// re-registering for the same key replaces the prior bytes.
func TestRegisterOverwrites(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "cache")
	first := filepath.Join(tmp, "first.log")
	second := filepath.Join(tmp, "second.log")
	if err := os.WriteFile(first, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	key := tracecache.Key{SrcKey: "k", TracerVersion: "v1"}
	if err := tracecache.Register(root, key, first); err != nil {
		t.Fatal(err)
	}
	if err := tracecache.Register(root, key, second); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(tmp, "out.log")
	if err := tracecache.Lookup(root, key, out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "second" {
		t.Errorf("Lookup after overwrite: got %q, want %q", got, "second")
	}
}
