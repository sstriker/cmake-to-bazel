// Package orchestrator drives per-element conversions.
//
// The Run function walks the element graph in dependency-first topo order
// and invokes the converter once per kind:cmake element. M3a uses os/exec
// against a real `convert-element` binary; M3b will swap the same call
// shape for a REAPI Action submission.
//
// Outputs land under <Out>/elements/<elem-name>/. A successful conversion
// produces:
//
//	BUILD.bazel
//	cmake-config/<Pkg>{Config,Targets,Targets-release}.cmake
//	read_paths.json
//
// A Tier-1 failure produces failure.json under the same directory and the
// element is recorded in the global failures registry. Tier-2/3 failures
// (converter crashed, infrastructure error) propagate as Go errors that
// abort the whole orchestrator.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sstriker/cmake-to-bazel/internal/manifest"
	"github.com/sstriker/cmake-to-bazel/internal/shadow"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/actionkey"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/allowlistreg"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/element"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/exports"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/synthprefix"
)

// Options configures one Run call.
type Options struct {
	// Project / Graph are the parsed FDSDK input. Run filters to
	// kind:cmake elements internally; the caller passes the full graph.
	Project *element.Project
	Graph   *element.Graph

	// Out is the output root; the canonical layout
	// (docs/m3-plan.md, "Output layout") is built underneath it.
	Out string

	// SourcesBase, when non-empty, takes precedence over per-element
	// `sources[].path` resolution. The orchestrator looks for each
	// element's source tree at <SourcesBase>/<element-name>/. Used by
	// the test fixture and by orchestrators that pre-stage sources.
	SourcesBase string

	// ConverterBinary is the path to the convert-element binary. Defaults
	// to "convert-element" (PATH lookup).
	ConverterBinary string

	// Log captures orchestrator progress messages and per-element
	// converter stdout/stderr (merged). Defaults to os.Stderr when nil.
	Log io.Writer
}

// Result summarizes a Run.
type Result struct {
	Converted   []string
	Failed      []FailureRecord
	CacheHits   []string // elements whose outputs came from the action cache
	CacheMisses []string // elements that ran convert-element this pass
}

// FailureRecord is the per-element entry the orchestrator collects for the
// global failures.json registry. Mirrors converter Tier-1 failure.json
// schema with an added Element field.
type FailureRecord struct {
	Element string `json:"element"`
	Tier    int    `json:"tier"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// sandboxPrefix is the in-sandbox path the converter's hermetic layer
// mounts --prefix-dir at. Must match
// converter/internal/hermetic/sandbox.go's --ro-bind target.
const sandboxPrefix = "/opt/prefix/"

// Run drives the conversion. Returns a populated Result on success even if
// some elements failed Tier-1; only Tier-2/3 (or orchestrator-level)
// errors return non-nil err.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Project == nil || opts.Graph == nil {
		return nil, errors.New("orchestrator: Project and Graph required")
	}
	if opts.Out == "" {
		return nil, errors.New("orchestrator: Out required")
	}
	conv := opts.ConverterBinary
	if conv == "" {
		conv = "convert-element"
	}
	convAbs, err := exec.LookPath(conv)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: converter binary %q not on PATH: %w", conv, err)
	}

	order, err := opts.Graph.TopoSort()
	if err != nil {
		return nil, err
	}
	cmakeOrder := opts.Graph.FilterByKind(order, "cmake")

	if err := os.MkdirAll(filepath.Join(opts.Out, "elements"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(opts.Out, "manifest"), 0o755); err != nil {
		return nil, err
	}

	// depRecords accumulates per-element data needed to write a downstream
	// element's imports manifest:
	//   raw   — manifest.Export values with CMakeTarget set (no labels yet)
	//   relLinkPaths — cmake_target -> prefix-relative IMPORTED_LOCATION paths
	//   pkg   — the bundle's <Pkg> (drives lib/cmake/<Pkg>/ in the
	//           consumer's synth-prefix)
	//
	// LinkPaths in the consumer's manifest are absolute paths under the
	// consumer's prefix root, so they have to be stitched per-consumer
	// rather than stored once per dep.
	depRecords := map[string]*depRecord{}
	importsRoot := filepath.Join(opts.Out, "imports")
	prefixRoot := filepath.Join(opts.Out, "synth-prefix")
	shadowRoot := filepath.Join(opts.Out, "shadow")
	registryRoot := filepath.Join(opts.Out, "registry", "allowlists")
	if err := os.MkdirAll(importsRoot, 0o755); err != nil {
		return nil, err
	}

	registry := allowlistreg.New(registryRoot)
	actionsCacheRoot := filepath.Join(opts.Out, "cache", "actions")
	if err := os.MkdirAll(actionsCacheRoot, 0o755); err != nil {
		return nil, err
	}

	res := &Result{}
	for _, name := range cmakeOrder {
		el := opts.Project.Elements[name]
		realSrcRoot, err := resolveSource(el, opts.SourcesBase)
		if err != nil {
			return nil, fmt.Errorf("element %s: %w", name, err)
		}
		fmt.Fprintf(logOf(opts), "==> %s\n", name)

		// Load any persisted allowlist entries for this element from a
		// previous orchestrator run; build a shadow tree using the
		// default allowlist union'd with the registered paths. cmake
		// runs against the shadow tree so editing a non-allowlisted
		// .c file's content doesn't change the action key.
		if err := registry.Load(name); err != nil {
			return nil, fmt.Errorf("element %s: load allowlist registry: %w", name, err)
		}
		shadowSrc, err := buildShadowForElement(shadowRoot, name, realSrcRoot, registry.Matcher(name))
		if err != nil {
			return nil, fmt.Errorf("element %s: shadow tree: %w", name, err)
		}

		prefixPath, err := buildPrefixForElement(prefixRoot, name, opts.Graph, depRecords, opts.Out)
		if err != nil {
			return nil, fmt.Errorf("element %s: synth-prefix: %w", name, err)
		}

		importsPath, err := writeImportsForElement(importsRoot, name, opts.Graph, depRecords, prefixPath)
		if err != nil {
			return nil, fmt.Errorf("element %s: imports manifest: %w", name, err)
		}

		// Action-key cache: if the same (shadow, imports, prefix,
		// converter) combination has produced outputs before, reuse them
		// instead of running convert-element again. The plumbing that
		// follows must run regardless (depRecords/registry get updated
		// either way) so cache-hit and cache-miss paths converge.
		key, err := actionkey.Compute(actionkey.Inputs{
			ShadowDir:       shadowSrc,
			ImportsManifest: importsPath,
			PrefixDir:       prefixPath,
			ConverterBin:    convAbs,
		})
		if err != nil {
			return nil, fmt.Errorf("element %s: action key: %w", name, err)
		}
		cachedDir := filepath.Join(actionsCacheRoot, key)
		elemOut := filepath.Join(opts.Out, "elements", name)
		hit, err := tryCacheHit(cachedDir, elemOut)
		if err != nil {
			return nil, fmt.Errorf("element %s: cache lookup: %w", name, err)
		}

		var fr *FailureRecord
		if hit {
			fmt.Fprintf(logOf(opts), "    cache hit %s\n", key[:12])
			res.CacheHits = append(res.CacheHits, name)
		} else {
			fr, err = convertOne(ctx, conv, name, shadowSrc, importsPath, prefixPath, opts)
			if err != nil {
				return nil, err
			}
			if fr == nil {
				if err := populateCache(cachedDir, elemOut); err != nil {
					return nil, fmt.Errorf("element %s: populate cache: %w", name, err)
				}
				res.CacheMisses = append(res.CacheMisses, name)
			}
		}
		if fr != nil {
			res.Failed = append(res.Failed, *fr)
			continue
		}
		res.Converted = append(res.Converted, name)

		// Merge this run's read paths into the persistent registry so
		// the next run uses an augmented shadow allowlist.
		readPaths, err := loadReadPaths(filepath.Join(opts.Out, "elements", name, "read_paths.json"))
		if err != nil {
			return nil, fmt.Errorf("element %s: load read_paths: %w", name, err)
		}
		if err := registry.Update(name, readPaths); err != nil {
			return nil, fmt.Errorf("element %s: update allowlist registry: %w", name, err)
		}

		// Pick up this element's bundle metadata for downstream consumers.
		bundleDir := filepath.Join(opts.Out, "elements", name, "cmake-config")
		raw, err := exports.FromBundle(bundleDir)
		if err != nil {
			return nil, fmt.Errorf("element %s: extract exports: %w", name, err)
		}
		relPaths, err := exports.PrefixRelativeLinkPaths(bundleDir)
		if err != nil {
			return nil, fmt.Errorf("element %s: link paths: %w", name, err)
		}
		pkg, err := synthprefix.PkgFromBundle(bundleDir)
		if err != nil {
			return nil, fmt.Errorf("element %s: pkg-from-bundle: %w", name, err)
		}
		depRecords[name] = &depRecord{
			ElementName:        name,
			Pkg:                pkg,
			RawExports:         raw,
			PrefixRelLinkPaths: relPaths,
		}
	}

	if err := writeManifest(opts.Out, res); err != nil {
		return nil, err
	}
	return res, nil
}

// convertOne runs the converter against one element. Returns (nil, nil) on
// success, (FailureRecord, nil) on Tier-1, (nil, err) on Tier-2/3.
func convertOne(ctx context.Context, conv, name, srcRoot, importsPath, prefixPath string, opts Options) (*FailureRecord, error) {
	elemOut := filepath.Join(opts.Out, "elements", name)
	if err := os.MkdirAll(elemOut, 0o755); err != nil {
		return nil, err
	}

	args := []string{
		"--source-root", srcRoot,
		"--out-build", filepath.Join(elemOut, "BUILD.bazel"),
		"--out-bundle-dir", filepath.Join(elemOut, "cmake-config"),
		"--out-failure", filepath.Join(elemOut, "failure.json"),
		"--out-read-paths", filepath.Join(elemOut, "read_paths.json"),
	}
	if importsPath != "" {
		args = append(args, "--imports-manifest", importsPath)
	}
	if prefixPath != "" {
		args = append(args, "--prefix-dir", prefixPath)
	}

	cmd := exec.CommandContext(ctx, conv, args...)
	cmd.Stdout = logOf(opts)
	cmd.Stderr = logOf(opts)

	err := cmd.Run()
	if err == nil {
		return nil, nil
	}

	// Tier-1: convert-element exited 1 with a written failure.json.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		fr, ferr := loadFailure(name, filepath.Join(elemOut, "failure.json"))
		if ferr != nil {
			return nil, fmt.Errorf("element %s: convert-element exit 1 but failure.json unreadable: %w", name, ferr)
		}
		return fr, nil
	}
	// Tier-2/3 or other unexpected exit. Bubble up.
	return nil, fmt.Errorf("element %s: convert-element: %w", name, err)
}

// resolveSource picks the source tree path for an element. If
// SourcesBase is set, uses <SourcesBase>/<element-name>. Otherwise falls
// back to the first `kind: local` source's path, resolved relative to the
// .bst file's directory.
func resolveSource(el *element.Element, sourcesBase string) (string, error) {
	if sourcesBase != "" {
		// Use the element name (with directory components) under the
		// shared base. e.g. components/hello -> <base>/components/hello.
		p := filepath.Join(sourcesBase, filepath.FromSlash(el.Name))
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("source dir %q: %w", p, err)
		}
		return p, nil
	}
	for _, s := range el.Sources {
		if s.Kind == "local" {
			path, ok := s.Extra["path"].(string)
			if !ok || path == "" {
				return "", errors.New("local source missing path")
			}
			abs := path
			if !filepath.IsAbs(path) {
				abs = filepath.Join(filepath.Dir(el.SourcePath), path)
			}
			abs = filepath.Clean(abs)
			if _, err := os.Stat(abs); err != nil {
				return "", fmt.Errorf("source dir %q: %w", abs, err)
			}
			return abs, nil
		}
	}
	return "", errors.New("no kind:local source; pass --sources-base to override")
}

func loadFailure(name, path string) (*FailureRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Tier    int    `json:"tier"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return &FailureRecord{
		Element: name,
		Tier:    raw.Tier,
		Code:    raw.Code,
		Message: raw.Message,
	}, nil
}

// writeManifest writes converted.json + failures.json + determinism.json
// under <out>/manifest/.
//
// converted.json + failures.json entries are sorted by element name for
// stable diffs. determinism.json is a path -> sha256 map covering every
// per-element output file the orchestrator produced; M3a step 7's
// determinism test compares these across machines. Schema versioned via
// "version": 1 so M4's regression detector can fence on incompatible
// reads.
func writeManifest(out string, res *Result) error {
	conv := append([]string(nil), res.Converted...)
	sort.Strings(conv)
	type elemEntry struct {
		Name string `json:"name"`
	}
	convDoc := struct {
		Version  int         `json:"version"`
		Elements []elemEntry `json:"elements"`
	}{Version: 1}
	for _, n := range conv {
		convDoc.Elements = append(convDoc.Elements, elemEntry{Name: n})
	}
	if err := writeJSON(filepath.Join(out, "manifest", "converted.json"), convDoc); err != nil {
		return err
	}

	fails := append([]FailureRecord(nil), res.Failed...)
	sort.Slice(fails, func(i, j int) bool { return fails[i].Element < fails[j].Element })
	failDoc := struct {
		Version  int             `json:"version"`
		Elements []FailureRecord `json:"elements"`
	}{Version: 1, Elements: fails}
	if err := writeJSON(filepath.Join(out, "manifest", "failures.json"), failDoc); err != nil {
		return err
	}

	return writeDeterminism(out)
}

// writeDeterminism walks every file under <out>/elements/ and hashes it,
// then writes a sorted path -> sha256 map under <out>/manifest/
// determinism.json. The file is what M3a step 7's three-tmpdir test
// compares for byte-equality across machines.
func writeDeterminism(out string) error {
	root := filepath.Join(out, "elements")
	type entry struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	var entries []entry
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		// determinism.json shouldn't include itself.
		sum, err := hashRegularFile(p)
		if err != nil {
			return err
		}
		entries = append(entries, entry{
			Path:   filepath.ToSlash(rel),
			SHA256: sum,
		})
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	doc := struct {
		Version int     `json:"version"`
		Files   []entry `json:"files"`
	}{Version: 1, Files: entries}
	return writeJSON(filepath.Join(out, "manifest", "determinism.json"), doc)
}

func hashRegularFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func logOf(opts Options) io.Writer {
	if opts.Log != nil {
		return opts.Log
	}
	return os.Stderr
}

// depRecord is the per-element bundle/exports state the orchestrator holds
// across the per-element loop.
type depRecord struct {
	ElementName        string
	Pkg                string
	RawExports         []*manifest.Export
	PrefixRelLinkPaths map[string][]string
}

// writeImportsForElement writes a per-element imports.json containing the
// exports of every element transitively reachable from `name` via deps.
// LinkPaths in each export are absolute paths under the consumer's
// prefixPath (the synth-prefix tree built earlier this iteration). When
// prefixPath is empty (no deps with bundles) LinkPaths are omitted.
//
// Returns the host path to the written file, or "" if there are no exports
// to declare (avoids passing --imports-manifest with an empty document).
func writeImportsForElement(importsRoot, name string, g *element.Graph, depRecords map[string]*depRecord, prefixPath string) (string, error) {
	closure := transitiveDeps(g, name)
	var elems []*manifest.Element
	for _, d := range closure {
		rec, ok := depRecords[d]
		if !ok || len(rec.RawExports) == 0 {
			continue
		}
		linkPathsFor := map[string][]string{}
		if prefixPath != "" {
			// CMake's codemodel records link.commandFragments paths as
			// they appear *inside* the converter's bwrap sandbox, where
			// the synth-prefix tree is mounted at /opt/prefix (per
			// converter/internal/hermetic/sandbox.go's --ro-bind line).
			// link_paths in the imports manifest must use the same
			// anchoring so lower's path-match in
			// LookupLinkPath actually fires.
			for tgt, rels := range rec.PrefixRelLinkPaths {
				for _, rel := range rels {
					linkPathsFor[tgt] = append(linkPathsFor[tgt],
						sandboxPrefix+strings.TrimPrefix(rel, "/"))
				}
			}
		}
		elems = append(elems, exports.AsElement(d, rec.RawExports, linkPathsFor))
	}
	if len(elems) == 0 {
		return "", nil
	}
	sort.Slice(elems, func(i, j int) bool { return elems[i].Name < elems[j].Name })

	doc := &manifest.Imports{Version: 1, Elements: elems}
	out := filepath.Join(importsRoot, filepath.FromSlash(name)+".json")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(out, append(body, '\n'), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// buildPrefixForElement assembles a CMAKE_PREFIX_PATH-shaped tree under
// <prefixRoot>/<element-name>/ holding the synthesized cmake-config
// bundles for every dep with exports plus zero-byte stubs for each
// IMPORTED_LOCATION_<CONFIG>. Returns the absolute prefix path the
// converter should mount, or "" if there's nothing to stage.
func buildPrefixForElement(prefixRoot, name string, g *element.Graph, depRecords map[string]*depRecord, outRoot string) (string, error) {
	closure := transitiveDeps(g, name)
	var deps []synthprefix.DepBundle
	for _, d := range closure {
		rec, ok := depRecords[d]
		if !ok || rec.Pkg == "" {
			continue // either non-cmake or had no Config.cmake
		}
		bundleDir := filepath.Join(outRoot, "elements", d, "cmake-config")
		if _, err := os.Stat(bundleDir); err != nil {
			continue
		}
		deps = append(deps, synthprefix.DepBundle{Pkg: rec.Pkg, SourceDir: bundleDir})
	}
	if len(deps) == 0 {
		return "", nil
	}
	dst := filepath.Join(prefixRoot, filepath.FromSlash(name))
	// Build is destructive on existing dst; M3a step 6's cache layer will
	// reuse prefix trees, but until then we recreate from scratch.
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := synthprefix.Build(dst, deps); err != nil {
		return "", err
	}
	return dst, nil
}

// buildShadowForElement creates <shadowRoot>/<element-name>/ as a path-only
// mirror of the real source tree, with file content preserved only for
// allowlisted paths. cmake's access(R_OK)-only configure-time semantics
// (cmake_analysis.md §0) make a shadow tree configure-equivalent to the
// real one for every non-allowlisted file. The orchestrator points
// convert-element at the shadow tree so content-only edits to .c files
// are absorbed at the cache key (M3a step 6).
func buildShadowForElement(shadowRoot, name, realSrc string, m shadow.Matcher) (string, error) {
	dst := filepath.Join(shadowRoot, filepath.FromSlash(name))
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := shadow.Build(realSrc, dst, m); err != nil {
		return "", err
	}
	return dst, nil
}

// tryCacheHit returns true if cachedDir exists and its contents have been
// successfully copied to dst (which is created/cleaned). Missing cachedDir
// is the cache-miss signal — returns (false, nil).
func tryCacheHit(cachedDir, dst string) (bool, error) {
	info, err := os.Stat(cachedDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("cache entry %s is not a directory", cachedDir)
	}
	if err := os.RemoveAll(dst); err != nil {
		return false, err
	}
	if err := copyTree(cachedDir, dst); err != nil {
		return false, err
	}
	return true, nil
}

// populateCache writes srcDir's contents into cacheDir atomically (via a
// sibling tmp + rename) so a crashed populate doesn't leave a half-cached
// entry that future runs would treat as a hit.
func populateCache(cacheDir, srcDir string) error {
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(cacheDir), "incoming-*")
	if err != nil {
		return err
	}
	if err := copyTree(srcDir, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	// If something already exists at cacheDir (race with a parallel
	// orchestrator instance), keep theirs; remove ours and treat as hit.
	if _, err := os.Stat(cacheDir); err == nil {
		_ = os.RemoveAll(tmp)
		return nil
	}
	return os.Rename(tmp, cacheDir)
}

// copyTree mirrors src into dst, regular files only. Symlinks are
// recreated. Other special file types are not part of the converter's
// output set so we don't model them.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			return os.Symlink(t, out)
		case d.IsDir():
			return os.MkdirAll(out, info.Mode().Perm())
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			outf, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(outf, in); err != nil {
				_ = outf.Close()
				return err
			}
			return outf.Close()
		}
		return nil
	})
}

// loadReadPaths reads the converter-emitted read_paths.json into a string
// slice. Missing file is not an error (older converter versions or runs
// without --out-read-paths produce no file); we just return an empty list.
func loadReadPaths(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// transitiveDeps walks the graph from `name` collecting every reachable
// dep in dependency-first order (deps before dependents). Idempotent under
// repeated calls; returns deduplicated names.
func transitiveDeps(g *element.Graph, name string) []string {
	seen := map[string]bool{}
	var order []string
	var walk func(n string)
	walk = func(n string) {
		for _, d := range g.Edges[n] {
			if seen[d] {
				continue
			}
			seen[d] = true
			walk(d)
			order = append(order, d)
		}
	}
	walk(name)
	return order
}
