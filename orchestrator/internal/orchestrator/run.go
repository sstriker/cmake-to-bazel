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
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/sstriker/cmake-to-bazel/internal/cas"
	"github.com/sstriker/cmake-to-bazel/internal/manifest"
	"github.com/sstriker/cmake-to-bazel/internal/reapi"
	"github.com/sstriker/cmake-to-bazel/internal/shadow"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/allowlistreg"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/element"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/exports"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/sourcecheckout"
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

	// Store is the CAS+ActionCache backing the per-element conversion
	// cache. When nil, Run constructs a LocalStore at <Out>/cache so
	// existing tests and offline runs work unchanged. Pass a GRPCStore
	// to share cache hits across orchestrator instances.
	Store cas.Store

	// Executor, when non-nil, takes over per-element execution on AC
	// miss: instead of running convert-element locally via os/exec,
	// the orchestrator submits the Action to Executor.Execute and
	// uses the returned ActionResult. M3b's GRPCExecutor goes through
	// REAPI's Execution service. Nil = local exec (M5 default).
	Executor reapi.Executor

	// Platform encodes the action's platform properties (OS family,
	// arch, tool versions). When empty, Run uses defaultPlatform —
	// linux/x86_64 with the cmake/ninja/bwrap pins from the Makefile.
	// Two orchestrators must use the same Platform to share cache hits.
	Platform []reapi.PlatformProperty

	// Concurrency caps how many elements process in parallel. <=0 falls
	// back to runtime.NumCPU(). Topological ordering is preserved —
	// dependents wait for their deps to finish — but independent
	// subgraphs run in parallel. Set to 1 for sequential, deterministic
	// log output (tests + dev loops).
	Concurrency int

	// SourceAsset, when non-nil, enables `kind: remote-asset` source
	// resolution: the orchestrator looks up sources by uri+qualifiers
	// against the Remote Asset endpoint and materializes the resulting
	// Directory from Store. Used for FDSDK sources published via
	// `bst source push`. Local + git checkouts work without it.
	SourceAsset *cas.RemoteAsset

	// Log captures orchestrator progress messages and per-element
	// converter stdout/stderr (merged). Defaults to os.Stderr when nil.
	Log io.Writer
}

// defaultPlatform mirrors the toolchain pins enforced by the Makefile
// and by hermetic.AssertCMakeVersion. Bumping any pin invalidates every
// element's cache by changing the Action digest — that's the contract.
var defaultPlatform = []reapi.PlatformProperty{
	{Name: "Arch", Value: "x86_64"},
	{Name: "OSFamily", Value: "linux"},
	{Name: "bwrap-version", Value: "0.8.0"},
	{Name: "cmake-version", Value: "3.28.3"},
	{Name: "ninja-version", Value: "1.11.1"},
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
	// rather than stored once per dep. The runner owns this map.
	importsRoot := filepath.Join(opts.Out, "imports")
	prefixRoot := filepath.Join(opts.Out, "synth-prefix")
	shadowRoot := filepath.Join(opts.Out, "shadow")
	registryRoot := filepath.Join(opts.Out, "registry", "allowlists")
	if err := os.MkdirAll(importsRoot, 0o755); err != nil {
		return nil, err
	}

	registry := allowlistreg.New(registryRoot)

	store := opts.Store
	if store == nil {
		ls, err := cas.NewLocalStore(filepath.Join(opts.Out, "cache"))
		if err != nil {
			return nil, fmt.Errorf("orchestrator: init local cas: %w", err)
		}
		store = ls
	}
	platform := opts.Platform
	if len(platform) == 0 {
		platform = defaultPlatform
	}

	r := &runner{
		opts:         opts,
		conv:         conv,
		convAbs:      convAbs,
		store:        store,
		platform:     platform,
		resolver:     newResolver(opts, store),
		importsRoot:  importsRoot,
		prefixRoot:   prefixRoot,
		shadowRoot:   shadowRoot,
		registry:     registry,
		depRecords:   map[string]*depRecord{},
		res:          &Result{},
	}

	if err := r.driveElements(ctx, cmakeOrder); err != nil {
		return nil, err
	}

	// Topo-order driver may complete elements out of input order under
	// concurrency; sort the result lists for stable diffs.
	sort.Strings(r.res.Converted)
	sort.Strings(r.res.CacheHits)
	sort.Strings(r.res.CacheMisses)
	sort.Slice(r.res.Failed, func(i, j int) bool { return r.res.Failed[i].Element < r.res.Failed[j].Element })

	if err := writeManifest(opts.Out, r.res); err != nil {
		return nil, err
	}
	return r.res, nil
}

// runner holds the per-Run state shared across element-processing
// goroutines. The mutex protects the maps and slices written by
// completing elements (depRecords, res) and the registry's persistent
// state.
type runner struct {
	opts     Options
	conv     string
	convAbs  string
	store    cas.Store
	platform []reapi.PlatformProperty
	resolver *sourcecheckout.Resolver

	importsRoot, prefixRoot, shadowRoot string
	registry                            *allowlistreg.Registry

	mu         sync.Mutex
	depRecords map[string]*depRecord
	res        *Result
}

// driveElements fans the cmake-only topo-ordered slice across a
// goroutine pool. Each element waits for its deps' done channels
// before processing; the pool size caps in-flight work without
// reshaping topology. Tier-1 failures stay local to the element;
// Tier-2/3 cancel the run via ctx.
func (r *runner) driveElements(ctx context.Context, cmakeOrder []string) error {
	concurrency := r.opts.Concurrency
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}

	// One done channel per element so dependents can wait without
	// polling. Pre-allocate so racy readers don't see a missing entry.
	done := make(map[string]chan struct{}, len(cmakeOrder))
	for _, n := range cmakeOrder {
		done[n] = make(chan struct{})
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		errMu    sync.Mutex
		firstErr error
	)
	recordErr := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
			cancel()
		}
		errMu.Unlock()
	}

	// Concurrency cap. Independent subgraphs share the slot pool.
	sem := make(chan struct{}, concurrency)

	var wg sync.WaitGroup
	for _, name := range cmakeOrder {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Always close the done channel — even on error or abort —
			// so dependents that haven't picked up runCtx cancellation
			// yet aren't stuck waiting.
			defer close(done[name])

			// Wait for cmake-kind deps to land. Non-cmake deps don't
			// have done channels; skip them.
			for _, dep := range r.opts.Graph.Edges[name] {
				ch, ok := done[dep]
				if !ok {
					continue
				}
				select {
				case <-ch:
				case <-runCtx.Done():
					return
				}
			}
			if runCtx.Err() != nil {
				return
			}

			// Acquire a concurrency slot.
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				return
			}
			defer func() { <-sem }()

			if err := r.processElement(runCtx, name); err != nil {
				recordErr(err)
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// processElement runs the full per-element pipeline. Returns nil on
// success or Tier-1 (failure recorded in r.res); error on Tier-2/3.
func (r *runner) processElement(ctx context.Context, name string) error {
	// Short-circuit dependents of failed elements with a synthetic
	// Tier-1: trying to convert them would fail at find_package
	// (synth-prefix is missing the failed dep's bundle) with a less
	// informative configure-failed code. dep-failed surfaces the root
	// cause cleanly.
	if failedDep, ok := r.firstFailedDep(name); ok {
		r.appendFailure(FailureRecord{
			Element: name,
			Tier:    1,
			Code:    "dep-failed",
			Message: fmt.Sprintf("transitive cmake dep %s failed Tier-1; skipped to surface root cause", failedDep),
		})
		fmt.Fprintf(logOf(r.opts), "==> %s\n    skipped: dep %s failed\n", name, failedDep)
		return nil
	}

	el := r.opts.Project.Elements[name]
	realSrcRoot, err := r.resolver.Resolve(ctx, el)
	if err != nil {
		return fmt.Errorf("element %s: %w", name, err)
	}
	fmt.Fprintf(logOf(r.opts), "==> %s\n", name)

	// allowlist registry is shared persistent state — serialize.
	r.mu.Lock()
	if err := r.registry.Load(name); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("element %s: load allowlist registry: %w", name, err)
	}
	matcher := r.registry.Matcher(name)
	r.mu.Unlock()

	shadowSrc, err := buildShadowForElement(r.shadowRoot, name, realSrcRoot, matcher)
	if err != nil {
		return fmt.Errorf("element %s: shadow tree: %w", name, err)
	}

	// depRecords is populated by completed dependents. Snapshot under
	// the lock and let buildPrefixForElement / writeImportsForElement
	// work on the immutable copy.
	r.mu.Lock()
	depSnapshot := make(map[string]*depRecord, len(r.depRecords))
	for k, v := range r.depRecords {
		depSnapshot[k] = v
	}
	r.mu.Unlock()

	prefixPath, err := buildPrefixForElement(r.prefixRoot, name, r.opts.Graph, depSnapshot, r.opts.Out)
	if err != nil {
		return fmt.Errorf("element %s: synth-prefix: %w", name, err)
	}
	importsPath, err := writeImportsForElement(r.importsRoot, name, r.opts.Graph, depSnapshot, prefixPath)
	if err != nil {
		return fmt.Errorf("element %s: imports manifest: %w", name, err)
	}

	built, err := reapi.Build(reapi.Inputs{
		ShadowDir:       shadowSrc,
		ImportsManifest: importsPath,
		PrefixDir:       prefixPath,
		ConverterBin:    r.convAbs,
		Platform:        r.platform,
	})
	if err != nil {
		return fmt.Errorf("element %s: build action: %w", name, err)
	}
	elemOut := filepath.Join(r.opts.Out, "elements", name)
	hit, fr, err := tryActionCacheHit(ctx, r.store, built, elemOut)
	if err != nil {
		return fmt.Errorf("element %s: action cache lookup: %w", name, err)
	}

	if hit {
		fmt.Fprintf(logOf(r.opts), "    cache hit %s\n", built.ActionDigest.Hash[:12])
		r.appendCacheHit(name)
	} else {
		if err := os.RemoveAll(elemOut); err != nil {
			return fmt.Errorf("element %s: clear elemOut: %w", name, err)
		}
		if r.opts.Executor != nil {
			fr, err = remoteExecute(ctx, r.store, r.opts.Executor, built, elemOut, name)
		} else {
			fr, err = convertOne(ctx, r.conv, name, shadowSrc, importsPath, prefixPath, r.opts)
			if err == nil && fr == nil {
				if err = publishActionResult(ctx, r.store, built, elemOut); err != nil {
					err = fmt.Errorf("element %s: publish action result: %w", name, err)
				}
			}
		}
		if err != nil {
			return err
		}
		if fr == nil {
			r.appendCacheMiss(name)
		}
	}
	if fr != nil {
		r.appendFailure(*fr)
		return nil
	}

	readPaths, err := loadReadPaths(filepath.Join(r.opts.Out, "elements", name, "read_paths.json"))
	if err != nil {
		return fmt.Errorf("element %s: load read_paths: %w", name, err)
	}

	bundleDir := filepath.Join(r.opts.Out, "elements", name, "cmake-config")
	raw, err := exports.FromBundle(bundleDir)
	if err != nil {
		return fmt.Errorf("element %s: extract exports: %w", name, err)
	}
	relPaths, err := exports.PrefixRelativeLinkPaths(bundleDir)
	if err != nil {
		return fmt.Errorf("element %s: link paths: %w", name, err)
	}
	pkg, err := synthprefix.PkgFromBundle(bundleDir)
	if err != nil {
		return fmt.Errorf("element %s: pkg-from-bundle: %w", name, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.registry.Update(name, readPaths); err != nil {
		return fmt.Errorf("element %s: update allowlist registry: %w", name, err)
	}
	r.depRecords[name] = &depRecord{
		ElementName:        name,
		Pkg:                pkg,
		RawExports:         raw,
		PrefixRelLinkPaths: relPaths,
	}
	r.res.Converted = append(r.res.Converted, name)
	return nil
}

func (r *runner) appendCacheHit(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.res.CacheHits = append(r.res.CacheHits, name)
}

func (r *runner) appendCacheMiss(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.res.CacheMisses = append(r.res.CacheMisses, name)
}

func (r *runner) appendFailure(fr FailureRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.res.Failed = append(r.res.Failed, fr)
}

// firstFailedDep walks name's transitive cmake-deps closure and returns
// the first dep already recorded in r.res.Failed. Concurrency-safe;
// reads r.res.Failed under the mutex.
//
// Topological scheduling guarantees deps complete before dependents,
// so this snapshot is always consistent: a missing failed-record means
// the dep succeeded (or was non-cmake), never "still in flight".
func (r *runner) firstFailedDep(name string) (string, bool) {
	closure := transitiveDeps(r.opts.Graph, name)
	r.mu.Lock()
	defer r.mu.Unlock()
	failed := make(map[string]bool, len(r.res.Failed))
	for _, fr := range r.res.Failed {
		failed[fr.Element] = true
	}
	for _, d := range closure {
		if failed[d] {
			return d, true
		}
	}
	return "", false
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

// newResolver builds a sourcecheckout.Resolver wired to the
// orchestrator's source cache (under <Out>/sources/) and respecting
// the --sources-base override when set. SourceAsset+Store hand the
// resolver the M3d remote-asset path.
func newResolver(opts Options, store cas.Store) *sourcecheckout.Resolver {
	return &sourcecheckout.Resolver{
		CacheDir:    filepath.Join(opts.Out, "sources"),
		SourcesBase: opts.SourcesBase,
		ElementSourceDir: func(el *element.Element) string {
			return filepath.Dir(el.SourcePath)
		},
		Asset: opts.SourceAsset,
		Store: store,
	}
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

// tryActionCacheHit looks up the Action's digest in ActionCache. On
// hit, materializes every output blob to elemOut and returns
// (true, fr, nil) — fr is non-nil when the cached ActionResult
// represents a Tier-1 failure (failure.json present in outputs).
//
// Returns (false, nil, nil) on cache miss, blob-eviction (stale AC
// entry), or any other recoverable lookup failure — caller falls
// through to local execution.
func tryActionCacheHit(ctx context.Context, store cas.Store, built *reapi.BuiltAction, elemOut string) (bool, *FailureRecord, error) {
	ar, err := store.GetActionResult(ctx, built.ActionDigest)
	if errors.Is(err, cas.ErrNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if err := os.RemoveAll(elemOut); err != nil {
		return false, nil, err
	}
	if err := os.MkdirAll(elemOut, 0o755); err != nil {
		return false, nil, err
	}
	if err := reapi.MaterializeResult(ctx, store, ar, elemOut); err != nil {
		// Stale AC entry (referenced blob evicted) — treat as miss
		// and fall through. Per the M5 plan's resilience case.
		var miss *reapi.ErrMissingBlob
		if errors.As(err, &miss) {
			_ = os.RemoveAll(elemOut)
			return false, nil, nil
		}
		return false, nil, err
	}
	if ar.ExitCode == 1 {
		fr, ferr := loadFailure("", filepath.Join(elemOut, "failure.json"))
		if ferr != nil {
			return false, nil, ferr
		}
		return true, fr, nil
	}
	return true, nil, nil
}

// publishActionResult uploads every output produced by a successful
// local conversion to CAS, then writes the ActionResult to AC so future
// runs (on this or any other machine) hit cache.
func publishActionResult(ctx context.Context, store cas.Store, built *reapi.BuiltAction, elemOut string) error {
	ar, err := reapi.SynthesizeResult(ctx, store, elemOut, built.OutputPaths, 0, nil, nil)
	if err != nil {
		return err
	}
	return store.UpdateActionResult(ctx, built.ActionDigest, ar)
}

// remoteExecute submits the Action to the configured Executor (M3b's
// REAPI Execution path), then materializes the returned ActionResult
// into elemOut. The worker is responsible for publishing the
// ActionResult to AC; the orchestrator only consumes it.
//
// Tier-1 failure detection: the converter writes failure.json on
// exit-1, which the worker collects as an OutputFile. Same behavior
// shape as convertOne returning a *FailureRecord.
func remoteExecute(ctx context.Context, store cas.Store, exec reapi.Executor, built *reapi.BuiltAction, elemOut, name string) (*FailureRecord, error) {
	ar, err := exec.Execute(ctx, store, built)
	if err != nil {
		return nil, fmt.Errorf("element %s: remote execute: %w", name, err)
	}
	if err := os.MkdirAll(elemOut, 0o755); err != nil {
		return nil, fmt.Errorf("element %s: mkdir elemOut: %w", name, err)
	}
	if err := reapi.MaterializeResult(ctx, store, ar, elemOut); err != nil {
		return nil, fmt.Errorf("element %s: materialize result: %w", name, err)
	}
	if ar.ExitCode == 1 {
		fr, ferr := loadFailure(name, filepath.Join(elemOut, "failure.json"))
		if ferr != nil {
			return nil, fmt.Errorf("element %s: remote action exit 1 but failure.json unreadable: %w", name, ferr)
		}
		return fr, nil
	}
	if ar.ExitCode != 0 {
		return nil, fmt.Errorf("element %s: remote action exit %d", name, ar.ExitCode)
	}
	return nil, nil
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
