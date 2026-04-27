package cas

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDigestOf_KnownVectors(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		hash string
		size int64
	}{
		{
			name: "empty",
			in:   nil,
			hash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			size: 0,
		},
		{
			name: "abc",
			in:   []byte("abc"),
			hash: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
			size: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := DigestOf(tc.in)
			if d.Hash != tc.hash {
				t.Errorf("hash: got %s want %s", d.Hash, tc.hash)
			}
			if d.SizeBytes != tc.size {
				t.Errorf("size: got %d want %d", d.SizeBytes, tc.size)
			}
		})
	}
}

func TestEmptyDigest_IsSha256OfEmptyString(t *testing.T) {
	d := EmptyDigest()
	if d.Hash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" || d.SizeBytes != 0 {
		t.Errorf("empty digest mismatch: %s/%d", d.Hash, d.SizeBytes)
	}
}

func TestDigestFile_MatchesByteSlice(t *testing.T) {
	dir := t.TempDir()
	body := []byte("the quick brown fox\n")
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	fromFile, err := DigestFile(path)
	if err != nil {
		t.Fatalf("DigestFile: %v", err)
	}
	fromBytes := DigestOf(body)
	if !DigestEqual(fromFile, fromBytes) {
		t.Errorf("file vs bytes: %s vs %s", DigestString(fromFile), DigestString(fromBytes))
	}
}

func TestDigestEqual(t *testing.T) {
	a := DigestOf([]byte("a"))
	b := DigestOf([]byte("a"))
	c := DigestOf([]byte("b"))
	if !DigestEqual(a, b) {
		t.Errorf("a==b should be equal")
	}
	if DigestEqual(a, c) {
		t.Errorf("a==c should not be equal")
	}
	if DigestEqual(nil, a) || DigestEqual(a, nil) {
		t.Errorf("nil should not equal non-nil digest")
	}
	if !DigestEqual(nil, nil) {
		t.Errorf("nil should equal nil")
	}
}
