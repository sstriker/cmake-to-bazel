// Command write-a-spike is the toy "writer-of-A" for the meta-project
// hello-world spike. It parses a single .bst file (kind:cmake only),
// resolves its sources, and renders a project-A workspace tree
// containing one per-element generated BUILD.bazel that invokes
// convert-element via a genrule.
//
// This is a SPIKE: the goal is to validate that the meta-project shape
// described in docs/whole-project-plan.md works end-to-end against a
// minimal fixture, NOT to ship a production writer-of-A. After the
// spike validates, this gets replaced by a proper writer-of-A under
// cmd/write-a/ that handles the full .bst surface (kind dispatch,
// dep resolution, module extension wiring, etc.) per Phase 1 of the
// plan.
//
// Scope intentionally narrow:
//   - kind:cmake elements only
//   - kind:local sources only
//   - no cross-element deps
//   - no toolchain handling (uses the host's cc_toolchain)
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

// zero_files.bzl is embedded into the spike binary so the writer
// doesn't depend on its caller's working directory. Production
// wiring would pull the rule from a real bazel module instead.
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
	outDir := flag.String("out", "", "output directory for project A")
	convertBin := flag.String("convert-element", "", "path to the convert-element binary (will be referenced from project-A's tools/)")
	readPathsFeedback := flag.String("read-paths-feedback", "", "optional: path to a prior run's read_paths.json. When set, narrows the source-tree staging to that set + CMakeLists.txt files; everything else becomes a zero_files stub.")
	flag.Parse()

	if *bstPath == "" || *outDir == "" || *convertBin == "" {
		flag.Usage()
		os.Exit(2)
	}

	elem, err := loadElement(*bstPath)
	if err != nil {
		log.Fatalf("load element: %v", err)
	}
	if elem.Bst.Kind != "cmake" {
		log.Fatalf("spike supports only kind:cmake; got %q", elem.Bst.Kind)
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

	if err := writeProjectA(elem, *outDir, convertAbs); err != nil {
		log.Fatalf("write project A: %v", err)
	}
	fmt.Printf("wrote project A for element %q at %s (real=%d, zero=%d)\n",
		elem.Name, *outDir, len(elem.RealPaths), len(elem.ZeroPaths))
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
		return nil, fmt.Errorf("spike requires exactly one source; got %d", len(f.Sources))
	}
	src := f.Sources[0]
	if src.Kind != "local" {
		return nil, fmt.Errorf("spike supports only kind:local sources; got %q", src.Kind)
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
	// into project A's rules/ dir. The spike's rule has no deps, so a
	// flat copy works; production wiring would expose it via a module.
	if err := writeFile(filepath.Join(outDir, "rules", "zero_files.bzl"), zeroFilesBzl); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "rules", "BUILD.bazel"), "# rules/ exposes per-spike starlark utilities.\n"); err != nil {
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

	srcStage := filepath.Join(elemPkg, "sources")
	if err := os.RemoveAll(srcStage); err != nil {
		return err
	}
	for _, rel := range elem.RealPaths {
		if err := copyFile(filepath.Join(elem.AbsSourceDir, rel), filepath.Join(srcStage, rel)); err != nil {
			return fmt.Errorf("stage real source %s: %w", rel, err)
		}
	}
	// Zero-length stubs are written directly into the staged source
	// tree as real (empty) files. cmake's add_library(...) requires
	// the source paths to exist on disk at configure time, but
	// doesn't read their content for cmake-element conversion. We
	// could host the stubs via the zero_files starlark rule — that
	// landed alongside this spike — but it'd put the stubs at
	// bazel-bin/.../sources/<path> while the real srcs sit at
	// <workspace>/.../sources/<path>; the genrule's cmd would have
	// to merge them into a unified $SRC_ROOT. Writing the stubs as
	// project-A source files instead lets Bazel stage everything
	// from one location and keeps the genrule's cmd identical to the
	// no-narrowing case. zero_files.bzl stays in the repo as the
	// documented mechanism — production wiring uses it once the
	// merging step is wired.
	for _, rel := range elem.ZeroPaths {
		if err := writeFile(filepath.Join(srcStage, rel), ""); err != nil {
			return fmt.Errorf("stage zero stub %s: %w", rel, err)
		}
	}
	if err := writeFile(filepath.Join(elemPkg, "BUILD.bazel"), elementBuild(elem)); err != nil {
		return err
	}

	return nil
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
	return fmt.Sprintf(`module(name = "meta_project_spike_%s", version = "0.0.0")

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
	return fmt.Sprintf(`# Generated by write-a-spike. Do not edit by hand.

package(default_visibility = ["//visibility:public"])

# CMakeLists.txt is split out of the source filegroup so the genrule
# can $(location) it for the source-root anchor — Bazel rejects
# $(location) on a filegroup-internal path. The rest of the source
# tree (real files copied from the user's tree + zero-length stubs
# materialized by write-a-spike when --read-paths-feedback is set)
# flows through the filegroup unchanged.
filegroup(
    name = "%[1]s_sources",
    srcs = glob(["sources/**"], exclude = ["sources/CMakeLists.txt"]),
)

genrule(
    name = "%[1]s_converted",
    srcs = [":%[1]s_sources", "sources/CMakeLists.txt"],
    outs = [
        "BUILD.bazel.out",
        "read_paths.json",
        "cmake-config-bundle.tar",
    ],
    cmd = """
        SRC_ROOT="$$(dirname $(location sources/CMakeLists.txt))"
        BUNDLE_DIR="$$(mktemp -d)"
        $(location //tools:convert-element) \\
            --source-root="$$SRC_ROOT" \\
            --out-build="$(location BUILD.bazel.out)" \\
            --out-bundle-dir="$$BUNDLE_DIR" \\
            --out-read-paths="$(location read_paths.json)"
        tar -cf "$(location cmake-config-bundle.tar)" -C "$$BUNDLE_DIR" .
    """,
    tools = ["//tools:convert-element"],
)

# Typed exports project B consumes. The spike emits the converter's
# raw outputs; production rules expand cmake-config-bundle.tar into
# the typed slices (cmake_config / pkg_config / headers / libs).
filegroup(
    name = "build_bazel",
    srcs = [":%[1]s_converted"],
    output_group = "BUILD.bazel.out",
)
`, elem.Name)
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
// targets (they're rare in source trees and the spike doesn't need to
// preserve them).
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
