package cas

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
)

func TestLocalStore_BlobRoundTrip(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	body := []byte("hello world")
	d := DigestOf(body)

	if err := s.PutBlob(context.Background(), d, body); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	got, err := s.GetBlob(context.Background(), d)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body mismatch: got %q want %q", got, body)
	}
}

func TestLocalStore_GetBlob_NotFound(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	d := DigestOf([]byte("missing"))
	_, err = s.GetBlob(context.Background(), d)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestLocalStore_PutBlob_RejectsDigestMismatch(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	body := []byte("the truth")
	wrongDigest := DigestOf([]byte("a lie"))
	if err := s.PutBlob(context.Background(), wrongDigest, body); err == nil {
		t.Fatalf("PutBlob should reject digest/body mismatch, got nil err")
	}
}

func TestLocalStore_GetBlob_DetectsCorruption(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	body := []byte("real content")
	d := DigestOf(body)
	if err := s.PutBlob(context.Background(), d, body); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}

	// Tamper with the on-disk blob.
	if err := os.WriteFile(filepath.Join(root, "cas", d.Hash), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if _, err := s.GetBlob(context.Background(), d); err == nil {
		t.Errorf("GetBlob should detect corruption, got nil err")
	}
}

func TestLocalStore_FindMissing(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	present := []byte("here")
	pd := DigestOf(present)
	if err := s.PutBlob(context.Background(), pd, present); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}

	absent := DigestOf([]byte("not stored"))
	missing, err := s.FindMissing(context.Background(), []*Digest{pd, absent})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if len(missing) != 1 || !DigestEqual(missing[0], absent) {
		t.Errorf("expected [%s], got %v", DigestString(absent), missing)
	}
}

func TestLocalStore_ActionResultRoundTrip(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	ad := DigestOf([]byte("action"))
	ar := &repb.ActionResult{
		ExitCode: 0,
		OutputFiles: []*repb.OutputFile{
			{Path: "out/BUILD.bazel", Digest: DigestOf([]byte("BUILD"))},
		},
	}
	if err := s.UpdateActionResult(context.Background(), ad, ar); err != nil {
		t.Fatalf("UpdateActionResult: %v", err)
	}

	got, err := s.GetActionResult(context.Background(), ad)
	if err != nil {
		t.Fatalf("GetActionResult: %v", err)
	}
	if got.ExitCode != ar.ExitCode {
		t.Errorf("exit_code: got %d want %d", got.ExitCode, ar.ExitCode)
	}
	if len(got.OutputFiles) != 1 || got.OutputFiles[0].Path != "out/BUILD.bazel" {
		t.Errorf("output_files: got %+v", got.OutputFiles)
	}
}

func TestLocalStore_GetActionResult_NotFound(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	_, err = s.GetActionResult(context.Background(), DigestOf([]byte("none")))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
