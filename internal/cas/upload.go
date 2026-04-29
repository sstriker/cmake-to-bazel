package cas

import (
	"context"
	"fmt"
	"os"
)

// UploadDir packs a local directory and uploads every Directory proto
// and file blob it contains to store, returning the root Directory
// digest. Already-present blobs are skipped via FindMissing. Used by
// sourcecheckout to register element source trees in CAS so the
// digest is the canonical id of the source content, regardless of the
// host path the orchestrator happened to materialize it at.
func UploadDir(ctx context.Context, store Store, host string) (*Digest, error) {
	tree, err := PackDir(host)
	if err != nil {
		return nil, fmt.Errorf("upload-dir: pack %s: %w", host, err)
	}

	dirBodies := make(map[string][]byte, len(tree.Directories))
	digests := make([]*Digest, 0, len(tree.Directories)+len(tree.Files))
	for h, d := range tree.Directories {
		body, err := MarshalDeterministic(d)
		if err != nil {
			return nil, fmt.Errorf("upload-dir: marshal directory %s: %w", h, err)
		}
		dirBodies[h] = body
		digests = append(digests, &Digest{Hash: h, SizeBytes: int64(len(body))})
	}
	for h, p := range tree.Files {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("upload-dir: stat %s: %w", p, err)
		}
		digests = append(digests, &Digest{Hash: h, SizeBytes: info.Size()})
	}

	missing, err := store.FindMissing(ctx, digests)
	if err != nil {
		return nil, fmt.Errorf("upload-dir: findmissing: %w", err)
	}
	missingSet := make(map[string]bool, len(missing))
	for _, d := range missing {
		missingSet[d.Hash] = true
	}

	for h, body := range dirBodies {
		if !missingSet[h] {
			continue
		}
		if err := store.PutBlob(ctx, &Digest{Hash: h, SizeBytes: int64(len(body))}, body); err != nil {
			return nil, fmt.Errorf("upload-dir: put directory %s: %w", h, err)
		}
	}
	for h, p := range tree.Files {
		if !missingSet[h] {
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("upload-dir: read %s: %w", p, err)
		}
		if err := store.PutBlob(ctx, &Digest{Hash: h, SizeBytes: int64(len(body))}, body); err != nil {
			return nil, fmt.Errorf("upload-dir: put file %s: %w", h, err)
		}
	}
	return tree.RootDigest, nil
}
