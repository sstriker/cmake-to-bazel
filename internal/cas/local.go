package cas

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

// LocalStore is a filesystem-backed Store. It serves the
// `--cas=local:<path>` mode and is the test substrate for everything
// that doesn't need a real Buildbarn endpoint.
//
// Layout:
//
//	<root>/cas/<hash>  raw blob bytes (file size MUST equal the
//	                   digest's size_bytes)
//	<root>/ac/<hash>   serialized ActionResult proto, keyed by
//	                   action digest hash
//
// Blob content is verified on Get (cheap insurance against bit rot
// and the cache-corruption resilience case in the M5 plan).
type LocalStore struct {
	Root string
}

// NewLocalStore initializes a LocalStore under root, creating the cas/
// and ac/ subdirs if absent.
func NewLocalStore(root string) (*LocalStore, error) {
	for _, sub := range []string{"cas", "ac"} {
		p := filepath.Join(root, sub)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, fmt.Errorf("local cas mkdir %s: %w", p, err)
		}
	}
	return &LocalStore{Root: root}, nil
}

func (s *LocalStore) blobPath(d *Digest) string {
	return filepath.Join(s.Root, "cas", d.Hash)
}

func (s *LocalStore) acPath(d *Digest) string {
	return filepath.Join(s.Root, "ac", d.Hash)
}

func (s *LocalStore) FindMissing(_ context.Context, digests []*Digest) ([]*Digest, error) {
	var missing []*Digest
	for _, d := range digests {
		_, err := os.Stat(s.blobPath(d))
		switch {
		case err == nil:
			// present
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, d)
		default:
			return nil, fmt.Errorf("findmissing %s: %w", DigestString(d), err)
		}
	}
	return missing, nil
}

func (s *LocalStore) GetBlob(_ context.Context, d *Digest) ([]byte, error) {
	body, err := os.ReadFile(s.blobPath(d))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getblob %s: %w", DigestString(d), err)
	}
	got := DigestOf(body)
	if !DigestEqual(got, d) {
		return nil, fmt.Errorf("getblob %s: stored body has digest %s",
			DigestString(d), DigestString(got))
	}
	return body, nil
}

func (s *LocalStore) PutBlob(_ context.Context, d *Digest, body []byte) error {
	got := DigestOf(body)
	if !DigestEqual(got, d) {
		return fmt.Errorf("putblob: declared %s but body digests to %s",
			DigestString(d), DigestString(got))
	}
	return writeAtomic(s.blobPath(d), body)
}

func (s *LocalStore) GetActionResult(_ context.Context, actionDigest *Digest) (*repb.ActionResult, error) {
	body, err := os.ReadFile(s.acPath(actionDigest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getactionresult %s: %w", DigestString(actionDigest), err)
	}
	ar := &repb.ActionResult{}
	if err := proto.Unmarshal(body, ar); err != nil {
		return nil, fmt.Errorf("getactionresult %s: unmarshal: %w",
			DigestString(actionDigest), err)
	}
	return ar, nil
}

func (s *LocalStore) UpdateActionResult(_ context.Context, actionDigest *Digest, ar *repb.ActionResult) error {
	body, err := MarshalDeterministic(ar)
	if err != nil {
		return fmt.Errorf("updateactionresult %s: marshal: %w",
			DigestString(actionDigest), err)
	}
	return writeAtomic(s.acPath(actionDigest), body)
}

// writeAtomic writes via tmp+rename so concurrent readers never see a
// partial file. fsync is omitted — local CAS is offline scratch, not a
// durability boundary.
func writeAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
