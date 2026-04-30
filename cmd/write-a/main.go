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
	Depends []string    `yaml:"depends"`
	// Config is the per-kind freeform configuration block. Each
	// handler picks the keys it cares about (kind:manual reads
	// build-commands / install-commands / etc.; kind:cmake currently
	// uses none). yaml.v3 represents arbitrary YAML as a Node tree;
	// using a Node here lets handlers re-extract specific shapes
	// without forcing every kind to share one struct.
	Config yaml.Node `yaml:"config"`
}

type bstSource struct {
	Kind string `yaml:"kind"`
	Path string `yaml:"path"`
}

type element struct {
	Name string // derived from .bst filename (basename without .bst suffix)
	Bst  *bstFile
	// AbsSourceDir is the absolute path on the host to the resolved
	// element source tree (for kind:local, this is bstDir/<source.path>).
	// Empty for kinds that don't resolve a source tree (kind:stack).
	AbsSourceDir string
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
// `depends:` references to produce a topologically-sorted element
// list. Dep resolution matches by element name (filename basename
// without .bst); unresolved deps are an error so typos surface early.
func loadGraph(bstPaths []string) (*graph, error) {
	g := &graph{ByName: map[string]*element{}}
	for _, p := range bstPaths {
		elem, err := loadElement(p)
		if err != nil {
			return nil, err
		}
		if existing, ok := g.ByName[elem.Name]; ok {
			return nil, fmt.Errorf("element %q declared twice (%s and %s)",
				elem.Name, existing.Name, p)
		}
		g.ByName[elem.Name] = elem
		g.Elements = append(g.Elements, elem)
	}
	// Resolve depends:.
	for _, elem := range g.Elements {
		for _, depName := range elem.Bst.Depends {
			// Tolerate `depends: [- foo.bst]` style by stripping the
			// .bst suffix; also accept bare element names.
			depName = strings.TrimSuffix(depName, ".bst")
			dep, ok := g.ByName[depName]
			if !ok {
				return nil, fmt.Errorf("element %q depends on %q which is not in the graph", elem.Name, depName)
			}
			elem.Deps = append(elem.Deps, dep)
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

func loadElement(bstPath string) (*element, error) {
	body, err := os.ReadFile(bstPath)
	if err != nil {
		return nil, err
	}
	var f bstFile
	if err := yaml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", bstPath, err)
	}
	name := strings.TrimSuffix(filepath.Base(bstPath), ".bst")

	elem := &element{Name: name, Bst: &f}

	// Source resolution is per-kind. cmake / manual / autotools /
	// import / … pull a kind:local source tree from disk; stack /
	// filter / compose don't have their own sources. Phase 2's
	// supported kinds use kind:local where present.
	if h, ok := handlers[f.Kind]; ok && h.NeedsSources() {
		bstDir := filepath.Dir(bstPath)
		if len(f.Sources) != 1 {
			return nil, fmt.Errorf("%s: write-a (Phase 2) requires exactly one source per element of kind %q; got %d", bstPath, f.Kind, len(f.Sources))
		}
		src := f.Sources[0]
		if src.Kind != "local" {
			return nil, fmt.Errorf("%s: write-a (Phase 2) supports only kind:local sources; got %q", bstPath, src.Kind)
		}
		// kind:local path is interpreted relative to the .bst dir if it
		// isn't already absolute (matches BuildStream semantics).
		resolved := src.Path
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(bstDir, resolved)
		}
		abs, err := filepath.Abs(resolved)
		if err != nil {
			return nil, err
		}
		elem.AbsSourceDir = abs
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
