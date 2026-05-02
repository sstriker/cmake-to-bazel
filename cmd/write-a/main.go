// Command write-a is the production writer-of-A for the meta-project
// (Bazel-as-orchestrator) shape described in docs/whole-project-plan.md.
// It parses .bst element files, resolves their sources and dependencies,
// and renders project A (the meta workspace whose genrules invoke
// per-kind translator binaries) and project B (the consumer workspace
// built against project A's outputs).
//
// Phase 1 — kind:cmake only, single-element fixtures (hello-world.bst).
// Phase 2 (this file) — multi-element graphs + per-kind dispatch +
// kind:stack. Subsequent phases extend the kind set (kind:manual
// coarse-grained pipeline, then meson, autotools, ...) and the
// source-kind set (git, tar, remote-asset).
//
// Per-kind dispatch is mediated by the kindHandler interface (see
// kindHandler below); each kind's renderer takes the graph + a single
// element and contributes a per-element package to project A and/or
// project B as appropriate. Kinds that don't need an action-graph step
// (stack, filter, import, …) only contribute project-B starlark; the
// driver script's stage step is a no-op for them.
//
// Shadow-tree narrowing (kind:cmake):
//   - With --read-paths-feedback unset: every source file is staged
//     real. First-run / no-feedback shape.
//   - With --read-paths-feedback pointing at a prior run's
//     read_paths.json: only files in the read set (plus all
//     CMakeLists.txt files in the source tree, which the trace
//     never captures because cmake's parser opens them before any
//     trace event fires) get staged. Everything else becomes a
//     zero_files entry — present at the same path inside the
//     genrule's exec root, but with empty content. cmake's
//     directory walks see the entries; reads against zero stubs
//     would be hits on empty files. The action input merkle is
//     content-stable across edits to non-read source files.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// zero_files.bzl is embedded into the binary so the writer doesn't
// depend on its caller's working directory. A future iteration may
// expose the rule via a published bazel module so consumers can
// `bazel_dep` it directly; for now embedding keeps the deployment
// shape one-binary-and-go.
//
//go:embed assets/zero_files.bzl
var zeroFilesBzl string

// bstFile is the YAML shape we parse out of a .bst element file.
// We only read the fields write-a's per-kind dispatch and source
// resolution need; other fields BuildStream understands (e.g.
// `variables:`) are ignored for now and will get plumbed in by the
// per-kind handlers that need them.
type bstFile struct {
	Kind    string      `yaml:"kind"`
	Sources []bstSource `yaml:"sources"`
	// Depends / BuildDepends / RuntimeDepends are the three
	// dependency categories BuildStream defines. `depends` covers
	// both build- and run-time; `build-depends` is build-only;
	// `runtime-depends` is runtime-only. write-a (v1) merges all
	// three into a single dep edge set in element.Deps — the build-
	// vs-runtime distinction matters once the typed-filegroup
	// wrapper for pipeline-kind outputs lets consumers reference
	// runtime-only labels separately, which lands later.
	Depends        []bstDep `yaml:"depends"`
	BuildDepends   []bstDep `yaml:"build-depends"`
	RuntimeDepends []bstDep `yaml:"runtime-depends"`
	// Config is the per-kind freeform configuration block. Each
	// handler picks the keys it cares about (kind:manual reads
	// build-commands / install-commands / etc.; kind:cmake currently
	// uses none). yaml.v3 represents arbitrary YAML as a Node tree;
	// using a Node here lets handlers re-extract specific shapes
	// without forcing every kind to share one struct.
	Config yaml.Node `yaml:"config"`
	// Variables is the per-element BuildStream variable scope. Layered
	// on top of project defaults and the per-kind defaults declared
	// by the handler; consumed via resolveVars in variables.go. Each
	// pipeline-kind handler runs phase commands through
	// substituteCmd against the resolved map.
	Variables map[string]string `yaml:"variables"`
	// Public is the BuildStream public-data block: per-element
	// downstream metadata (split-rules, environment overrides, ...).
	// 33 % of FDSDK elements declare it. For v1 we decode it as a
	// yaml.Node so the file parses but don't act on its contents —
	// kind:filter's domain enforcement (which consumes
	// public.bst.split-rules) is a follow-up.
	Public yaml.Node `yaml:"public"`
}

// bstDep is one entry inside a depends / build-depends / runtime-
// depends list. Real .bst files declare deps in two shapes:
//
//   - String shape:  "- foo.bst"
//   - Map shape:     "- filename: foo.bst, junction: jx.bst, config: {...}"
//
// The map shape carries junction-targeting and per-dep config (e.g.
// kind:filter overriding parent's domain choice). For v1 we only
// consume Filename — junction and config get parsed and recorded
// (so the unmarshal doesn't reject map-form entries) but aren't
// yet acted on.
type bstDep struct {
	Filename string
	Junction string
	Config   yaml.Node
}

// UnmarshalYAML accepts either a scalar (string-form dep) or a
// mapping (map-form dep). yaml.v3 picks per-entry shape via the
// Node's Kind, so a single list can mix both shapes.
func (d *bstDep) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		d.Filename = node.Value
		return nil
	case yaml.MappingNode:
		var raw struct {
			Filename string    `yaml:"filename"`
			Junction string    `yaml:"junction"`
			Config   yaml.Node `yaml:"config"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		if raw.Filename == "" {
			return fmt.Errorf("dep: map-form entry must have a `filename:` key")
		}
		d.Filename = raw.Filename
		d.Junction = raw.Junction
		d.Config = raw.Config
		return nil
	default:
		return fmt.Errorf("dep: expected scalar or mapping, got yaml node kind %d", node.Kind)
	}
}

type bstSource struct {
	Kind string `yaml:"kind"`
	Path string `yaml:"path"`
	// Directory is the optional staging subpath inside the element's
	// source tree (BuildStream defaults to ""). When set, this
	// source's content lands under <element-pkg>/<directory>/ rather
	// than at the package root. 64 of FDSDK's elements use it (most
	// commonly to keep separately-fetched component sources from
	// colliding with the primary source tree).
	Directory string `yaml:"directory"`
	// URL / Ref / Track are non-kind:local source metadata (kind:git_repo
	// / kind:tar / kind:remote / kind:patch_queue / etc.). For v1
	// write-a parses and records them on resolvedSource so the
	// element's bstFile + Sources fully describe what was declared,
	// but doesn't fetch — actual checkout is deferred to a later
	// integration with the existing orchestrator/sourcecheckout
	// layer. Unknown source kinds get the same record-and-skip
	// treatment so write-a's render pass succeeds against full FDSDK
	// content even where bazel-build wouldn't (yet) compile.
	URL   string `yaml:"url"`
	Ref   string `yaml:"ref"`
	Track string `yaml:"track"`
}

// resolvedSource is one entry in element.Sources: a per-source
// record with everything write-a's render layer needs. Kind:local
// sources carry the resolved AbsPath; non-kind:local sources carry
// their URL/Ref metadata (parsed for completeness, ignored at
// staging time pending real source-fetch integration).
type resolvedSource struct {
	Kind      string
	AbsPath   string // populated only for kind:local
	Directory string
	URL       string
	Ref       string
	Track     string
}

type element struct {
	Name string // derived from .bst filename (basename without .bst suffix)
	Bst  *bstFile
	// Sources is the resolved source list for this element — one
	// entry per kind:local source declared in the .bst, with each
	// AbsPath pre-resolved against the .bst's directory. Empty for
	// kinds that don't resolve their own source tree (kind:stack /
	// kind:compose / kind:filter).
	//
	// Single-source elements (most v1 fixtures) have len(Sources) ==
	// 1 with Directory == "". Handlers that pre-date multi-source
	// expect that shape; the staging loops in handler_cmake /
	// handler_pipeline / handler_import iterate Sources so multi-
	// source elements stage all their trees correctly.
	Sources []resolvedSource
	// Deps are the resolved depends-on edges of this element. Populated
	// during loadGraph; parents reference children.
	Deps []*element
	// ReadSet is the source-relative paths a prior run's
	// read_paths.json reported. Populated when
	// --read-paths-feedback is set; empty (and HasFeedback false)
	// otherwise. Only consumed by kind:cmake's handler.
	ReadSet     []string
	HasFeedback bool

	// RealPaths / ZeroPaths are derived during the cmake handler's
	// per-element rendering: real files staged on disk, zero paths
	// handed to the zero_files starlark rule.
	RealPaths []string
	ZeroPaths []string

	// ProjectConfVars is the project-level variable override layer
	// loaded from the meta-project's project.conf (see
	// project_conf.go). Same map across every element resolved from
	// the same project.conf; nil when no project.conf was found
	// walking up from the .bst file's directory.
	ProjectConfVars map[string]string
}

// graph is the loaded set of elements with cross-references resolved.
// Elements is topologically sorted (dependencies before dependents).
type graph struct {
	Elements []*element
	ByName   map[string]*element
}

// stringList is a flag.Value for repeated flags (--bst foo.bst --bst bar.bst).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	log.SetFlags(0)
	var bstPaths stringList
	flag.Var(&bstPaths, "bst", "path to a .bst file. Repeatable; pass once per element.")
	outA := flag.String("out", "", "output directory for project A (the meta workspace whose genrules run convert-element)")
	outB := flag.String("out-b", "", "optional: output directory for project B (the consumer workspace built against project A's outputs). When unset, only project A is rendered.")
	convertBin := flag.String("convert-element", "", "path to the convert-element binary (will be referenced from project-A's tools/)")
	readPathsFeedback := flag.String("read-paths-feedback", "", "optional: path to a prior run's read_paths.json. When set, narrows kind:cmake elements' source-tree staging to that set + CMakeLists.txt files; everything else becomes a zero_files stub. Currently single-element only — multi-element feedback gets a per-element flag in a follow-up.")
	flag.Parse()

	if len(bstPaths) == 0 || *outA == "" || *convertBin == "" {
		flag.Usage()
		os.Exit(2)
	}

	g, err := loadGraph(bstPaths)
	if err != nil {
		log.Fatalf("load graph: %v", err)
	}
	for _, elem := range g.Elements {
		if _, ok := handlers[elem.Bst.Kind]; !ok {
			log.Fatalf("element %q: write-a (Phase 2) supports kinds %s; got %q",
				elem.Name, supportedKinds(), elem.Bst.Kind)
		}
	}

	convertAbs, err := filepath.Abs(*convertBin)
	if err != nil {
		log.Fatalf("resolve convert-element path: %v", err)
	}
	if _, err := os.Stat(convertAbs); err != nil {
		log.Fatalf("convert-element binary at %s: %v", convertAbs, err)
	}

	if *readPathsFeedback != "" {
		feedback, err := loadReadPaths(*readPathsFeedback)
		if err != nil {
			log.Fatalf("load --read-paths-feedback: %v", err)
		}
		// Phase 2 still applies feedback to all kind:cmake elements
		// uniformly. Multi-element feedback (one read_paths.json per
		// element) lands when the FDSDK fixture forces the issue; for
		// now, single-element fixtures are the only consumers.
		for _, elem := range g.Elements {
			if elem.Bst.Kind == "cmake" {
				elem.ReadSet = feedback
				elem.HasFeedback = true
			}
		}
	}

	if err := writeProjectA(g, *outA, convertAbs); err != nil {
		log.Fatalf("write project A: %v", err)
	}
	fmt.Printf("wrote project A at %s (%d elements: %s)\n",
		*outA, len(g.Elements), summarizeKinds(g))

	if *outB != "" {
		if err := writeProjectB(g, *outB); err != nil {
			log.Fatalf("write project B: %v", err)
		}
		fmt.Printf("wrote project B at %s\n", *outB)
	}
}

// loadGraph parses every .bst path in input order, then resolves
// `depends:` / `build-depends:` / `runtime-depends:` references
// into a topologically-sorted element list. Element keying:
//
//   - With project.conf found: element name is the path relative to
//     the project's element-root (project.conf dir + element-path),
//     minus ".bst". So a .bst at <project>/elements/foo/bar.bst keys
//     into the graph as "foo/bar", and a depends-list reference
//     "foo/bar.bst" resolves regardless of which subdir the
//     declaration lives in.
//   - With no project.conf: element name falls back to basename
//     minus ".bst". The pre-project.conf shape; covers single-fixture
//     trees and the existing testdata/meta-project fixtures that
//     don't declare a project.
//
// Unresolved deps surface as errors so typos and missing-from-loader
// elements both surface early.
//
// Project.conf is loaded once per invocation, walking up from the
// first .bst's directory. Multi-junction graphs (where different
// .bsts root different project.confs) aren't supported — they'd
// need a per-junction scope on top of this single-project shape.
func loadGraph(bstPaths []string) (*graph, error) {
	g := &graph{ByName: map[string]*element{}}
	var info projectInfo
	if len(bstPaths) > 0 {
		var err error
		info, err = loadProjectInfoFromBst(bstPaths[0])
		if err != nil {
			return nil, fmt.Errorf("load project.conf: %w", err)
		}
	}
	for _, p := range bstPaths {
		// Element-level (@): includes resolve against the project
		// root when one's known (BuildStream's contract). Without a
		// project.conf, fall back to the .bst's own directory —
		// covers self-contained fixtures with no project setup.
		includeBase := info.ProjectRoot
		if includeBase == "" {
			includeBase = filepath.Dir(p)
		}
		elem, err := loadElement(p, includeBase)
		if err != nil {
			return nil, err
		}
		// Re-key the element by project-relative path when a
		// project.conf is in play. loadElement defaults Name to the
		// basename; here we widen it to the path-relative form.
		if info.ElementRoot != "" {
			absBst, err := filepath.Abs(p)
			if err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(info.ElementRoot, absBst)
			if err != nil {
				return nil, fmt.Errorf("compute element-path-relative name for %s: %w", p, err)
			}
			if strings.HasPrefix(rel, "..") {
				return nil, fmt.Errorf("element %s lives outside the project's element-root %s", p, info.ElementRoot)
			}
			elem.Name = strings.TrimSuffix(rel, ".bst")
		}
		if existing, ok := g.ByName[elem.Name]; ok {
			return nil, fmt.Errorf("element %q declared twice (%s and %s)",
				elem.Name, existing.Name, p)
		}
		elem.ProjectConfVars = info.Variables
		g.ByName[elem.Name] = elem
		g.Elements = append(g.Elements, elem)
	}
	// Resolve dependencies. All three lists (depends, build-depends,
	// runtime-depends) merge into element.Deps for v1 — write-a
	// doesn't yet distinguish build-only from runtime-only edges.
	// Duplicates (a dep listed in both `depends:` and
	// `build-depends:`, say) are tolerated: the dep's *element
	// pointer dedupes downstream (topo sort doesn't care about edge
	// multiplicity).
	for _, elem := range g.Elements {
		seen := map[*element]bool{}
		allDeps := append([]bstDep{}, elem.Bst.Depends...)
		allDeps = append(allDeps, elem.Bst.BuildDepends...)
		allDeps = append(allDeps, elem.Bst.RuntimeDepends...)
		for _, dep := range allDeps {
			// Tolerate `depends: [- foo.bst]` style by stripping the
			// .bst suffix; also accept bare element names.
			depName := strings.TrimSuffix(dep.Filename, ".bst")
			depElem, ok := g.ByName[depName]
			if !ok {
				return nil, fmt.Errorf("element %q depends on %q which is not in the graph", elem.Name, depName)
			}
			if seen[depElem] {
				continue
			}
			seen[depElem] = true
			elem.Deps = append(elem.Deps, depElem)
		}
	}
	// Topological sort (Kahn's algorithm). Stable secondary order on
	// element name so the rendered output is deterministic across
	// invocations regardless of input order.
	sorted, err := topoSort(g.Elements)
	if err != nil {
		return nil, err
	}
	g.Elements = sorted
	return g, nil
}

func topoSort(elems []*element) ([]*element, error) {
	indeg := map[*element]int{}
	for _, e := range elems {
		indeg[e] = 0
	}
	for _, e := range elems {
		for _, d := range e.Deps {
			indeg[e]++
			_ = d // edges are dep -> e; e's in-degree counts incoming edges.
		}
	}
	var ready []*element
	for _, e := range elems {
		if indeg[e] == 0 {
			ready = append(ready, e)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Name < ready[j].Name })

	var out []*element
	for len(ready) > 0 {
		e := ready[0]
		ready = ready[1:]
		out = append(out, e)
		// Decrement in-degree of any element that depends on e.
		for _, other := range elems {
			for _, d := range other.Deps {
				if d == e {
					indeg[other]--
					if indeg[other] == 0 {
						ready = append(ready, other)
					}
				}
			}
		}
		sort.Slice(ready, func(i, j int) bool { return ready[i].Name < ready[j].Name })
	}
	if len(out) != len(elems) {
		return nil, fmt.Errorf("dependency cycle among %d elements", len(elems))
	}
	return out, nil
}

// loadReadPaths parses a convert-element-emitted read_paths.json
// (a JSON array of source-relative paths).
func loadReadPaths(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// loadElement parses one .bst into an *element. includeBase is the
// directory (@): include paths resolve against (the project root,
// matching BuildStream semantics). When no project.conf was found
// for this graph, callers pass filepath.Dir(bstPath) as a fallback
// — covers the existing self-contained fixtures that don't declare
// a project.
func loadElement(bstPath, includeBase string) (*element, error) {
	doc, err := loadAndComposeYAML(bstPath, includeBase, map[string]bool{})
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", bstPath, err)
	}
	var f bstFile
	if err := doc.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode %s: %w", bstPath, err)
	}
	name := strings.TrimSuffix(filepath.Base(bstPath), ".bst")

	elem := &element{Name: name, Bst: &f}

	// Source resolution is per-kind. cmake / manual / autotools /
	// import / … pull a kind:local source tree from disk; stack /
	// filter / compose don't have their own sources. Phase 2's
	// supported kinds use kind:local where present.
	if h, ok := handlers[f.Kind]; ok && h.NeedsSources() {
		if len(f.Sources) == 0 {
			return nil, fmt.Errorf("%s: kind %q requires at least one source; .bst declares none", bstPath, f.Kind)
		}
		for _, src := range f.Sources {
			rs := resolvedSource{
				Kind:      src.Kind,
				Directory: src.Directory,
				URL:       src.URL,
				Ref:       src.Ref,
				Track:     src.Track,
			}
			if src.Kind == "local" {
				// kind:local paths resolve project-root-relative.
				// includeBase is the project root (or the .bst's
				// own directory when no project.conf was found —
				// covers self-contained fixtures). Absolute paths
				// pass through unchanged. Matches BuildStream's
				// kind:local semantics: "the contents of a
				// directory rooted at the project."
				resolved := src.Path
				if !filepath.IsAbs(resolved) {
					resolved = filepath.Join(includeBase, resolved)
				}
				abs, err := filepath.Abs(resolved)
				if err != nil {
					return nil, err
				}
				rs.AbsPath = abs
			}
			// Non-kind:local sources: record metadata; staging will
			// skip them (with a traceable comment) until the
			// orchestrator/sourcecheckout integration lands.
			elem.Sources = append(elem.Sources, rs)
		}
	}
	return elem, nil
}

// writeProjectA renders the meta workspace project A: top-level files
// (MODULE.bazel, BUILD.bazel, rules/, tools/) shared across every
// element, then a per-element package under elements/<name>/ rendered
// by the element's kind handler.
func writeProjectA(g *graph, outDir, convertBin string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	// Top-level files. Project A targets bazel >= 7 (bzlmod).
	// WORKSPACE.bazel was removed in bazel 8; MODULE.bazel is the
	// only module-declaration shape going forward. The meta workspace
	// has no external deps — only genrules — so the MODULE.bazel
	// here is just `module(...)` and bazel resolves nothing from
	// the registry beyond its built-in implicit deps (platforms,
	// rules_license, rules_java, etc., for toolchain bookkeeping).
	if err := writeFile(filepath.Join(outDir, "MODULE.bazel"), moduleBazelA()); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "BUILD.bazel"), "# project A root; per-element packages live under elements/<name>/.\n"); err != nil {
		return err
	}

	// Wire the zero_files rule by writing the embedded .bzl content
	// into project A's rules/ dir. The rule has no deps, so a flat
	// copy works; future iterations may expose it via a published
	// bazel module instead.
	if err := writeFile(filepath.Join(outDir, "rules", "zero_files.bzl"), zeroFilesBzl); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "rules", "BUILD.bazel"), "# rules/ holds the starlark utilities project A's per-element BUILDs use.\n"); err != nil {
		return err
	}

	// Stage the convert-element binary into project A's tools/ so the
	// per-element genrule sees it as a hermetic input via tools = [...].
	// `exports_files` keeps Bazel's load() footprint minimal — no
	// sh_binary, no rules_cc dependency. Production wiring would
	// build convert-element via a go_binary rule.
	if err := os.MkdirAll(filepath.Join(outDir, "tools"), 0o755); err != nil {
		return err
	}
	stagedBin := filepath.Join(outDir, "tools", "convert-element")
	if err := copyFile(convertBin, stagedBin); err != nil {
		return fmt.Errorf("stage convert-element: %w", err)
	}
	if err := os.Chmod(stagedBin, 0o755); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "tools", "BUILD.bazel"), `exports_files(["convert-element"])`+"\n"); err != nil {
		return err
	}

	for _, elem := range g.Elements {
		h := handlers[elem.Bst.Kind]
		elemPkg := filepath.Join(outDir, "elements", elem.Name)
		if err := os.MkdirAll(elemPkg, 0o755); err != nil {
			return err
		}
		if err := h.RenderA(elem, elemPkg); err != nil {
			return fmt.Errorf("render project-A package for %q (kind %q): %w", elem.Name, elem.Bst.Kind, err)
		}
	}

	return nil
}

// writeProjectB renders the consumer workspace project B reads against
// project A's outputs.
func writeProjectB(g *graph, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	if err := writeFile(filepath.Join(outDir, "MODULE.bazel"), moduleBazelB()); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "BUILD.bazel"),
		"# project B root; per-element packages live under elements/<name>/.\n",
	); err != nil {
		return err
	}

	for _, elem := range g.Elements {
		h := handlers[elem.Bst.Kind]
		elemPkg := filepath.Join(outDir, "elements", elem.Name)
		if err := os.RemoveAll(elemPkg); err != nil {
			return err
		}
		if err := os.MkdirAll(elemPkg, 0o755); err != nil {
			return err
		}
		if err := h.RenderB(elem, elemPkg); err != nil {
			return fmt.Errorf("render project-B package for %q (kind %q): %w", elem.Name, elem.Bst.Kind, err)
		}
	}
	return nil
}

func moduleBazelA() string {
	return `module(name = "meta_project_a", version = "0.0.0")

# Project A only runs genrules (one per element invoking the
# per-kind translator). It declares no bazel_dep — bazel pulls in
# its standard implicit modules (platforms / rules_license /
# rules_java / etc.) for toolchain bookkeeping; nothing else is
# needed.
`
}

// moduleBazelB declares rules_cc so project A's converted
// BUILD.bazel.out (which loads cc_library from @rules_cc//cc:defs.bzl)
// resolves cleanly in project B.
func moduleBazelB() string {
	return `module(name = "meta_project_b", version = "0.0.0")

# rules_cc is what the cmake-converter emits load() lines against
# (load("@rules_cc//cc:defs.bzl", "cc_library")). Pin a recent stable
# release; this is downloaded from bcr.bazel.build the first time
# project B's bazel build runs.
bazel_dep(name = "rules_cc", version = "0.0.17")
`
}

// summarizeKinds is for the startup log line: "kind:cmake×2, kind:stack×1".
func summarizeKinds(g *graph) string {
	counts := map[string]int{}
	for _, e := range g.Elements {
		counts[e.Bst.Kind]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("kind:%s×%d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

// supportedKinds is for the unknown-kind error message.
func supportedKinds() string {
	keys := make([]string, 0, len(handlers))
	for k := range handlers {
		keys = append(keys, "kind:"+k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// writeFile writes content to path, creating parent dirs.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// copyFile copies src to dst, creating parent dirs.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// copyTree recursively copies src to dst. Symlinks resolve to their
// targets (they're rare in kind:local trees and Phase 1 doesn't need
// to preserve them).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// stageAllSources copies every kind:local source in elem.Sources
// into dstRoot, honoring each entry's Directory subpath. Used by
// handlers whose staging is "all sources, flat or under their
// declared subdir": kind:cmake's project-B copy, kind:import's
// filegroup root, the pipeline-handler's project-A source mount.
//
// Non-kind:local sources (kind:git_repo / kind:tar / kind:patch /
// kind:remote / etc.) are accepted at parse time and recorded on
// the resolvedSource entry, but skipped here — they need real
// source-fetch integration (an extension of
// orchestrator/internal/sourcecheckout/) before write-a can hand
// real bytes to bazel. Until that lands, render-time succeeds
// against any source kind, but bazel-build of the resulting BUILD
// would fail at action-input merkle time on elements with
// non-kind:local sources. Document those skips inline so a reader
// of the rendered tree sees what was deferred.
func stageAllSources(elem *element, dstRoot string) error {
	for i, src := range elem.Sources {
		if src.Kind != "local" {
			continue
		}
		dst := dstRoot
		if src.Directory != "" {
			dst = filepath.Join(dstRoot, src.Directory)
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("element %q source[%d]: prepare directory %q: %w", elem.Name, i, src.Directory, err)
			}
		}
		if err := copyTree(src.AbsPath, dst); err != nil {
			return fmt.Errorf("element %q source[%d]: stage %s → %s: %w", elem.Name, i, src.AbsPath, dst, err)
		}
	}
	return nil
}

// hasNonLocalSources reports whether any of elem.Sources is not
// kind:local. Handlers that need actual source bytes at render
// time (kind:cmake's narrowing) check this and either error out
// or fall back to a no-narrowing path.
func hasNonLocalSources(elem *element) bool {
	for _, s := range elem.Sources {
		if s.Kind != "local" {
			return true
		}
	}
	return false
}
