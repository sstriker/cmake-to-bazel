// Command write-a is the production writer-of-A for the meta-project
// (Bazel-as-orchestrator) shape described in docs/whole-project-plan.md.
// It parses .bst element files, resolves their sources, and renders a
// project-A workspace tree where each element gets a per-element
// generated BUILD.bazel containing one genrule that invokes the
// per-kind translator binary (convert-element for kind:cmake) under
// Bazel's action graph.
//
// Phase 1 scope (cmake-only acceptance per the plan):
//   - kind:cmake elements only.
//   - kind:local sources only.
//   - no cross-element deps.
//   - no toolchain handling (uses the host's cc_toolchain).
//
// Subsequent phases extend this to multi-element graphs (Phase 3+),
// non-cmake kinds (Phase 3 / 4), additional source kinds (via the
// existing orchestrator/internal/sourcecheckout package), and a real
// bzlmod module extension shape that lazily generates per-element
// repos. This binary's interface (--bst, --out, --convert-element,
// --read-paths-feedback) is stable across those extensions; what
// grows is the writer's per-kind dispatch and graph handling.
//
// Shadow-tree narrowing:
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

type bstFile struct {
	Kind    string      `yaml:"kind"`
	Sources []bstSource `yaml:"sources"`
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
	AbsSourceDir string
	// ReadSet is the source-relative paths a prior run's
	// read_paths.json reported. Populated when
	// --read-paths-feedback is set; empty (and HasFeedback false)
	// otherwise.
	ReadSet     []string
	HasFeedback bool

	// Derived during writeProjectA: the partitioned source-tree
	// paths. RealPaths get staged into project A as files;
	// ZeroPaths become entries in the zero_files starlark rule.
	RealPaths []string
	ZeroPaths []string
}

func main() {
	log.SetFlags(0)
	bstPath := flag.String("bst", "", "path to the .bst file")
	outA := flag.String("out", "", "output directory for project A (the meta workspace whose genrules run convert-element)")
	outB := flag.String("out-b", "", "optional: output directory for project B (the consumer workspace built against project A's outputs). When unset, only project A is rendered.")
	convertBin := flag.String("convert-element", "", "path to the convert-element binary (will be referenced from project-A's tools/)")
	readPathsFeedback := flag.String("read-paths-feedback", "", "optional: path to a prior run's read_paths.json. When set, narrows the source-tree staging to that set + CMakeLists.txt files; everything else becomes a zero_files stub.")
	flag.Parse()

	if *bstPath == "" || *outA == "" || *convertBin == "" {
		flag.Usage()
		os.Exit(2)
	}

	elem, err := loadElement(*bstPath)
	if err != nil {
		log.Fatalf("load element: %v", err)
	}
	if elem.Bst.Kind != "cmake" {
		log.Fatalf("write-a (Phase 1) supports only kind:cmake; got %q", elem.Bst.Kind)
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
		elem.ReadSet = feedback
		elem.HasFeedback = true
	}

	if err := writeProjectA(elem, *outA, convertAbs); err != nil {
		log.Fatalf("write project A: %v", err)
	}
	fmt.Printf("wrote project A for element %q at %s (real=%d, zero=%d)\n",
		elem.Name, *outA, len(elem.RealPaths), len(elem.ZeroPaths))

	if *outB != "" {
		if err := writeProjectB(elem, *outB); err != nil {
			log.Fatalf("write project B: %v", err)
		}
		fmt.Printf("wrote project B for element %q at %s\n", elem.Name, *outB)
	}
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

	bstDir := filepath.Dir(bstPath)
	if len(f.Sources) != 1 {
		return nil, fmt.Errorf("write-a (Phase 1) requires exactly one source per element; got %d", len(f.Sources))
	}
	src := f.Sources[0]
	if src.Kind != "local" {
		return nil, fmt.Errorf("write-a (Phase 1) supports only kind:local sources; got %q", src.Kind)
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
	return &element{Name: name, Bst: &f, AbsSourceDir: abs}, nil
}

func writeProjectA(elem *element, outDir, convertBin string) error {
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
	if err := writeFile(filepath.Join(outDir, "MODULE.bazel"), moduleBazel(elem)); err != nil {
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

	// Per-element package: elements/<name>/{BUILD.bazel, sources/...}.
	elemPkg := filepath.Join(outDir, "elements", elem.Name)
	if err := os.MkdirAll(elemPkg, 0o755); err != nil {
		return err
	}

	// Partition source paths into RealPaths (staged as files) and
	// ZeroPaths (handed to the zero_files starlark rule).
	if err := partitionSources(elem); err != nil {
		return fmt.Errorf("partition sources: %w", err)
	}

	// Real sources are staged as files in project A's source tree;
	// the glob in the per-element BUILD picks them up. Zero stubs are
	// NOT staged on disk — they're produced at action time by the
	// zero_files starlark rule and merged into $SRC_ROOT inside the
	// genrule's cmd. That way, the rendered project-A tree only
	// contains the files the user can actually inspect; the empty
	// stubs are an action-graph detail Bazel handles.
	srcStage := filepath.Join(elemPkg, "sources")
	if err := os.RemoveAll(srcStage); err != nil {
		return err
	}
	for _, rel := range elem.RealPaths {
		if err := copyFile(filepath.Join(elem.AbsSourceDir, rel), filepath.Join(srcStage, rel)); err != nil {
			return fmt.Errorf("stage real source %s: %w", rel, err)
		}
	}
	if err := writeFile(filepath.Join(elemPkg, "BUILD.bazel"), elementBuild(elem)); err != nil {
		return err
	}

	return nil
}

// writeProjectB renders the consumer workspace project B reads against
// project A's outputs. Layout:
//
//	<outDir>/
//	  MODULE.bazel             ← bazel_dep(rules_cc)
//	  BUILD.bazel              ← top-level placeholder
//	  elements/<name>/
//	    BUILD.bazel            ← placeholder; the driver script
//	                             overwrites this with project A's
//	                             bazel-bin/elements/<name>/BUILD.bazel.out
//	                             after the bazel-A pass.
//	    <element source tree>  ← full set of the user's sources (no
//	                             narrowing — project B compiles the
//	                             converted cc_library, so it needs the
//	                             real files.)
//
// Project B doesn't run convert-element; it consumes A's converted
// BUILD.bazel.out, which references rules_cc (load("@rules_cc//cc:defs.bzl",
// "cc_library")). The MODULE.bazel here pulls rules_cc from the
// registry — first-time bzlmod runs need network access to bcr (or a
// mirror via META_BAZEL_*_ARGS, see scripts/meta-hello.sh comment).
func writeProjectB(elem *element, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	if err := writeFile(filepath.Join(outDir, "MODULE.bazel"), moduleBazelB(elem)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "BUILD.bazel"),
		"# project B root; per-element packages live under elements/<name>/.\n",
	); err != nil {
		return err
	}

	elemPkg := filepath.Join(outDir, "elements", elem.Name)
	if err := os.MkdirAll(elemPkg, 0o755); err != nil {
		return err
	}

	// Stage the FULL source tree (no narrowing). Project B's
	// cc_library needs the real source bytes to compile, so this is
	// the user's tree verbatim. Idempotent: blow away any prior
	// staging first so re-runs reflect the current source state.
	if err := os.RemoveAll(elemPkg); err != nil {
		return err
	}
	if err := os.MkdirAll(elemPkg, 0o755); err != nil {
		return err
	}
	if err := copyTree(elem.AbsSourceDir, elemPkg); err != nil {
		return fmt.Errorf("stage element sources for project B: %w", err)
	}

	// Placeholder BUILD; the driver script overwrites this after the
	// bazel-A pass produces the converter's BUILD.bazel.out. Without
	// the placeholder, Bazel would try to load() rules_cc against an
	// empty package and fail with a confusing error before the stage
	// step ran; the placeholder makes the staging-not-yet-run state
	// explicit.
	placeholder := fmt.Sprintf(`# Placeholder for cmd/write-a-rendered project B.
# The driver script overwrites this file with project A's
# bazel-bin/elements/%s/BUILD.bazel.out (the converter's output)
# after the project-A bazel build succeeds. If this file is still
# the placeholder when project B's bazel build runs, the staging
# step was skipped.
filegroup(name = "BUILD_NOT_YET_STAGED", srcs = [])
`, elem.Name)
	if err := writeFile(filepath.Join(elemPkg, "BUILD.bazel"), placeholder); err != nil {
		return err
	}
	return nil
}

// moduleBazelB is project B's MODULE.bazel. Declares rules_cc so
// project A's converted BUILD.bazel.out (which loads cc_library from
// @rules_cc//cc:defs.bzl) resolves cleanly.
func moduleBazelB(elem *element) string {
	return fmt.Sprintf(`module(name = "meta_project_b_%s", version = "0.0.0")

# rules_cc is what the cmake-converter emits load() lines against
# (load("@rules_cc//cc:defs.bzl", "cc_library")). Pin a recent stable
# release; this is downloaded from bcr.bazel.build the first time
# project B's bazel build runs.
bazel_dep(name = "rules_cc", version = "0.0.17")
`, sanitizeModuleName(elem.Name))
}

// partitionSources walks the element's source tree and decides which
// paths flow as real files vs zero stubs into project A.
//
//   - With no read-set feedback (HasFeedback==false), every file is
//     real. First-run / no-narrowing shape.
//   - With feedback, the real set = the feedback set, plus all
//     CMakeLists.txt files in the source tree (cmake parses the entry
//     CMakeLists before any trace event fires; auto-including them
//     keeps cmake configure correct after narrowing).
func partitionSources(elem *element) error {
	universe := []string{}
	err := filepath.Walk(elem.AbsSourceDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(elem.AbsSourceDir, p)
		if err != nil {
			return err
		}
		universe = append(universe, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(universe)

	if !elem.HasFeedback {
		elem.RealPaths = universe
		elem.ZeroPaths = nil
		return nil
	}

	real := map[string]struct{}{}
	for _, p := range elem.ReadSet {
		real[p] = struct{}{}
	}
	for _, p := range universe {
		if filepath.Base(p) == "CMakeLists.txt" {
			real[p] = struct{}{}
		}
	}

	for _, p := range universe {
		if _, ok := real[p]; ok {
			elem.RealPaths = append(elem.RealPaths, p)
		} else {
			elem.ZeroPaths = append(elem.ZeroPaths, p)
		}
	}
	return nil
}

func moduleBazel(elem *element) string {
	return fmt.Sprintf(`module(name = "meta_project_a_%s", version = "0.0.0")

# Project A only runs genrules (one per element invoking the
# per-kind translator). It declares no bazel_dep — bazel pulls in
# its standard implicit modules (platforms / rules_license /
# rules_java / etc.) for toolchain bookkeeping; nothing else is
# needed.
`, sanitizeModuleName(elem.Name))
}

// sanitizeModuleName returns a bzlmod-module-name-safe string. Bzlmod
// permits [A-Za-z0-9._-] but the sanitizer collapses to underscores
// to keep the name's shape obvious in error messages.
func sanitizeModuleName(s string) string {
	return strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(s)
}

func elementBuild(elem *element) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Generated by cmd/write-a. Do not edit by hand.

package(default_visibility = ["//visibility:public"])
`)

	// Render the zero_files load + target only when feedback narrowed
	// the source set. First-run / no-feedback elements have an empty
	// ZeroPaths and don't need the rule at all — keeps the BUILD
	// minimal in that common case.
	if len(elem.ZeroPaths) > 0 {
		fmt.Fprintf(&b, `
load("//rules:zero_files.bzl", "zero_files")

# Files cmake's directory walks see but don't read. Materialized
# at action time as zero-length stubs whose merkle is the empty
# SHA — the action input remains content-stable across edits to
# any of these paths in the user's source tree.
zero_files(
    name = "%[1]s_zero_stubs",
    paths = [
`, elem.Name)
		for _, p := range elem.ZeroPaths {
			fmt.Fprintf(&b, "        %q,\n", "sources/"+p)
		}
		fmt.Fprintf(&b, `    ],
)
`)
	}

	// Real sources flow through a glob; CMakeLists.txt is included
	// like any other entry — the cmd's shadow merge handles every
	// source uniformly via $(SRCS).
	fmt.Fprintf(&b, `
filegroup(
    name = "%[1]s_real",
    srcs = glob(["sources/**"]),
)
`, elem.Name)

	// Compose the genrule's srcs: real-files filegroup + (when
	// narrowed) the zero_stubs target. The shadow-merge cmd handles
	// both sets uniformly: each entry contains "sources/" in its
	// path; ${path##*sources/} strips down to the source-relative
	// suffix used inside cmake's source root.
	srcsList := fmt.Sprintf(`":%s_real"`, elem.Name)
	if len(elem.ZeroPaths) > 0 {
		srcsList += fmt.Sprintf(`, ":%s_zero_stubs"`, elem.Name)
	}

	fmt.Fprintf(&b, `
genrule(
    name = "%[1]s_converted",
    srcs = [%[2]s],
    outs = [
        "BUILD.bazel.out",
        "read_paths.json",
        "cmake-config-bundle.tar",
    ],
    cmd = """
        # Build a unified source-root by merging real srcs (workspace
        # paths under elements/<name>/sources/) and zero stubs (under
        # bazel-bin/.../sources/) into a fresh shadow dir. Both share
        # a "sources/" segment in their path; strip up to the last one
        # to recover the source-relative suffix.
        SHADOW="$$(mktemp -d)"
        for src in $(SRCS); do
            rel="$${src##*sources/}"
            mkdir -p "$$SHADOW/$$(dirname "$$rel")"
            cp -L "$$src" "$$SHADOW/$$rel"
        done
        BUNDLE_DIR="$$(mktemp -d)"
        $(location //tools:convert-element) \\
            --source-root="$$SHADOW" \\
            --out-build="$(location BUILD.bazel.out)" \\
            --out-bundle-dir="$$BUNDLE_DIR" \\
            --out-read-paths="$(location read_paths.json)"
        tar -cf "$(location cmake-config-bundle.tar)" -C "$$BUNDLE_DIR" .
    """,
    tools = ["//tools:convert-element"],
)

# Typed exports project B consumes. Phase 1 emits the converter's
# raw outputs; later phases expand cmake-config-bundle.tar into
# the typed slices (cmake_config / pkg_config / headers / libs).
filegroup(
    name = "build_bazel",
    srcs = [":%[1]s_converted"],
    output_group = "BUILD.bazel.out",
)
`, elem.Name, srcsList)
	return b.String()
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
