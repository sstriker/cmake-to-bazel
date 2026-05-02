package main

// Compute CAS Directory digests for graph sources by packing
// the on-disk source-cache trees (the same trees --source-cache
// already resolves at staging time). The packed digest lands in
// each sourceEntry's Digest field, so the rendered sources.json
// carries real CAS coordinates the FUSE-symlink repo rule
// (rules/sources.bzl) can address.
//
// Population strategy is intentionally write-a-side rather than
// inside the module extension: write-a already has the
// --source-cache tree and an absolute path per source identity.
// The extension stays small (read JSON, declare repos), and CAS
// upload (#59 via bst source push) remains an out-of-band step
// the dev runs once.

import (
	"fmt"

	"github.com/sstriker/cmake-to-bazel/internal/casfuse"
)

// populateDigests packs the on-disk source-cache tree for each
// sourceEntry that has one and stamps the resulting Directory
// digest into entry.Digest. Returns a per-key blob map (hash →
// bytes) for every Directory + file blob the packing produced;
// callers either (a) pass it into a fake CAS in tests or (b)
// hand it to bst source push / a CAS uploader in production.
//
// Sources without a source-cache hit (AbsPath empty) get
// Digest left empty — the sources.json carries enough metadata
// to resolve them later, but the FUSE-symlink repo rule will
// fail at evaluation time if asked to resolve such a key.
// That's the right behaviour: it surfaces "I forgot to populate
// the cache for source X" instead of silently producing a broken
// build.
func populateDigests(g *graph, entries []sourceEntry) ([]sourceEntry, map[string][]byte, error) {
	// Index resolvedSource by sourceKey so we can find AbsPath
	// per entry without re-walking the graph.
	keyToPath := map[string]string{}
	for _, elem := range g.Elements {
		for _, rs := range elem.Sources {
			k := sourceKey(rs)
			if k == "" {
				continue
			}
			if _, dup := keyToPath[k]; dup {
				continue
			}
			keyToPath[k] = rs.AbsPath
		}
	}

	allBlobs := map[string][]byte{}
	out := make([]sourceEntry, len(entries))
	for i, e := range entries {
		out[i] = e
		path, ok := keyToPath[e.Key]
		if !ok || path == "" {
			continue
		}
		pt, err := casfuse.PackDir(path)
		if err != nil {
			return nil, nil, fmt.Errorf("pack source %s (%s): %w", e.Key, path, err)
		}
		out[i].Digest = pt.RootDigest.String()
		for h, b := range pt.Blobs {
			allBlobs[h] = b
		}
	}
	return out, allBlobs, nil
}
