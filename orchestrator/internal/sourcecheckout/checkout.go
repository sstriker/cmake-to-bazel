// Package sourcecheckout resolves a BuildStream-style source spec
// to a local source-tree directory the converter can read.
//
// M3a/M3b required pre-staged sources via --sources-base. M3c handles
// the common non-local cases the FDSDK uses:
//
//   - kind: local         relative path; same behavior the orchestrator had.
//   - kind: git           clones url, checks out ref.
//   - kind: remote-asset  M3d: looks up uri+qualifiers via Remote Asset API
//                         and materializes the resulting Directory from CAS.
//                         Used for FDSDK sources already published via
//                         `bst source push`.
//
// Other kinds (tar, ostree, deb, bst-junction) are explicitly out of
// scope and surface as a clear error so the operator knows to either
// implement them or fall back to --sources-base.
//
// Checkouts are cached under cacheDir/<content-hash>/ so repeated
// runs against the same source identity reuse the on-disk tree. The
// hash includes the source kind so we never confuse a tar-derived
// tree with a git-derived one.
package sourcecheckout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/sstriker/cmake-to-bazel/internal/cas"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/element"
)

// Resolver checks out source trees on demand and caches them under
// cacheDir. Safe for sequential use; concurrent callers must serialize
// externally (the orchestrator does — one element at a time).
type Resolver struct {
	// CacheDir is the directory under which provisioned sources land.
	// Created on first checkout. Re-using the same CacheDir across
	// runs gives free incrementality.
	CacheDir string

	// SourcesBase, when non-empty, takes precedence over per-element
	// resolution. Used by tests + by orchestrators that have already
	// staged everything externally.
	SourcesBase string

	// ElementSourceDir locates an element's `.bst` file directory; used
	// to resolve `kind: local` paths relative to the YAML.
	ElementSourceDir func(el *element.Element) string

	// Asset, when non-nil, enables `kind: remote-asset` resolution.
	// The orchestrator's --source-cas flag points it at a CAS+RAA
	// endpoint. Store must also be set for materialization.
	Asset *cas.RemoteAsset

	// Store backs `kind: remote-asset`: after the asset's Directory
	// digest comes back from RAA, MaterializeDirectory walks it
	// against this CAS.
	Store cas.Store
}

// Resolve returns an absolute path to a directory the converter can
// read as the element's source-root. The directory is owned by the
// Resolver's cache (do not mutate).
func (r *Resolver) Resolve(ctx context.Context, el *element.Element) (string, error) {
	if r.SourcesBase != "" {
		p := filepath.Join(r.SourcesBase, filepath.FromSlash(el.Name))
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("element %s: pre-staged source dir %q: %w", el.Name, p, err)
		}
		return p, nil
	}
	if len(el.Sources) == 0 {
		return "", fmt.Errorf("element %s: no sources declared", el.Name)
	}
	// First non-junction source wins. BuildStream allows multiple
	// sources to overlay; the FDSDK subset uses one each, so this is
	// good enough for M3c.
	src := el.Sources[0]
	switch src.Kind {
	case "local":
		return r.resolveLocal(el, src)
	case "git":
		return r.resolveGit(ctx, el, src)
	case "remote-asset":
		return r.resolveRemoteAsset(ctx, el, src)
	default:
		return "", fmt.Errorf("element %s: unsupported source kind %q (M3c handles local, git, remote-asset; pass --sources-base to bypass)",
			el.Name, src.Kind)
	}
}

func (r *Resolver) resolveLocal(el *element.Element, src element.Source) (string, error) {
	path, ok := src.Extra["path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("element %s: kind:local source missing path", el.Name)
	}
	abs := path
	if !filepath.IsAbs(path) {
		base := r.ElementSourceDir(el)
		if base == "" {
			return "", fmt.Errorf("element %s: kind:local source has relative path %q but ElementSourceDir is unset", el.Name, path)
		}
		abs = filepath.Join(base, path)
	}
	abs = filepath.Clean(abs)
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("element %s: source dir %q: %w", el.Name, abs, err)
	}
	return abs, nil
}

// resolveGit clones url and checks out ref under
// CacheDir/<hash>/checkout. Re-checkout against an existing cache
// entry is a no-op (the dir's mere presence is the cache).
func (r *Resolver) resolveGit(ctx context.Context, el *element.Element, src element.Source) (string, error) {
	url, _ := src.Extra["url"].(string)
	ref, _ := src.Extra["ref"].(string)
	if url == "" {
		return "", fmt.Errorf("element %s: kind:git source missing url", el.Name)
	}
	if ref == "" {
		return "", fmt.Errorf("element %s: kind:git source missing ref (must be commit sha or stable tag)", el.Name)
	}

	key := contentKey("git", url, ref)
	dst := filepath.Join(r.CacheDir, key, "checkout")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("element %s: stat cache dir %q: %w", el.Name, dst, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("element %s: prep cache: %w", el.Name, err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), "incoming-*")
	if err != nil {
		return "", fmt.Errorf("element %s: prep cache: %w", el.Name, err)
	}
	defer os.RemoveAll(tmp)

	clone := exec.CommandContext(ctx, "git", "clone", "--no-checkout", url, tmp)
	if out, err := clone.CombinedOutput(); err != nil {
		return "", fmt.Errorf("element %s: git clone %s: %w\n%s", el.Name, url, err, out)
	}
	checkout := exec.CommandContext(ctx, "git", "-C", tmp, "checkout", "--detach", ref)
	if out, err := checkout.CombinedOutput(); err != nil {
		return "", fmt.Errorf("element %s: git checkout %s: %w\n%s", el.Name, ref, err, out)
	}
	if err := os.Rename(tmp, dst); err != nil {
		// Race with a parallel resolver — keep theirs.
		if _, statErr := os.Stat(dst); statErr == nil {
			return dst, nil
		}
		return "", fmt.Errorf("element %s: install cache: %w", el.Name, err)
	}
	return dst, nil
}

// resolveRemoteAsset looks up the source's Directory digest via
// Remote Asset Fetch, then materializes the tree from CAS into the
// per-element cache dir. M3d's primary path: FDSDK elements whose
// source has been published to CAS via `bst source push` are
// referenced by uri+qualifiers, never re-fetched from origin.
func (r *Resolver) resolveRemoteAsset(ctx context.Context, el *element.Element, src element.Source) (string, error) {
	if r.Asset == nil || r.Store == nil {
		return "", fmt.Errorf("element %s: kind:remote-asset needs --source-cas (Asset+Store wired on Resolver)",
			el.Name)
	}
	uri, _ := src.Extra["uri"].(string)
	if uri == "" {
		return "", fmt.Errorf("element %s: kind:remote-asset source missing uri", el.Name)
	}
	qualifiers := remoteAssetQualifiers(src)

	// Cache key over (kind, uri, sorted qualifiers). Hits skip the RAA
	// roundtrip + CAS materialize entirely.
	parts := []string{"remote-asset", uri}
	for _, q := range qualifiers {
		parts = append(parts, q.Name+"="+q.Value)
	}
	key := contentKey(parts...)
	dst := filepath.Join(r.CacheDir, key, "checkout")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("element %s: stat cache dir %q: %w", el.Name, dst, err)
	}

	digest, err := r.Asset.FetchDirectory(ctx, uri, qualifiers...)
	if err != nil {
		return "", fmt.Errorf("element %s: remote-asset fetch %s: %w", el.Name, uri, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("element %s: prep cache: %w", el.Name, err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), "incoming-*")
	if err != nil {
		return "", fmt.Errorf("element %s: prep cache: %w", el.Name, err)
	}
	if err := cas.MaterializeDirectory(ctx, r.Store, digest, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("element %s: materialize %s: %w", el.Name, cas.DigestString(digest), err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		if _, statErr := os.Stat(dst); statErr == nil {
			_ = os.RemoveAll(tmp)
			return dst, nil
		}
		return "", fmt.Errorf("element %s: install cache: %w", el.Name, err)
	}
	return dst, nil
}

// remoteAssetQualifiers extracts the optional `qualifiers` map from a
// remote-asset source spec into a sorted slice. YAML shape:
//
//	sources:
//	- kind: remote-asset
//	  uri: bst:source:components/hello@<unique-key>
//	  qualifiers:
//	    bst-source-kind: git
//	    bst-source-ref: <sha>
func remoteAssetQualifiers(src element.Source) []cas.Qualifier {
	raw, _ := src.Extra["qualifiers"].(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]cas.Qualifier, 0, len(keys))
	for _, k := range keys {
		v, _ := raw[k].(string)
		out = append(out, cas.Qualifier{Name: k, Value: v})
	}
	return out
}

// contentKey hashes the source kind + identifying inputs into a
// short hex string used as the cache subdirectory.
func contentKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
