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
//   - all paths in srcs are real (no zero_files yet — that's the
//     follow-up commit)
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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
}

func main() {
	log.SetFlags(0)
	bstPath := flag.String("bst", "", "path to the .bst file")
	outDir := flag.String("out", "", "output directory for project A")
	convertBin := flag.String("convert-element", "", "path to the convert-element binary (will be referenced from project-A's tools/)")
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

	if err := writeProjectA(elem, *outDir, convertAbs); err != nil {
		log.Fatalf("write project A: %v", err)
	}
	fmt.Printf("wrote project A for element %q at %s\n", elem.Name, *outDir)
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
	abs, err := filepath.Abs(filepath.Join(bstDir, src.Path))
	if err != nil {
		return nil, err
	}
	return &element{Name: name, Bst: &f, AbsSourceDir: abs}, nil
}

func writeProjectA(elem *element, outDir, convertBin string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	// Top-level files. The spike uses WORKSPACE.bazel rather than
	// bzlmod's MODULE.bazel because the meta workspace has no
	// external deps (only genrules) and WORKSPACE keeps the spike
	// compatible with older bazel versions in dev environments.
	// Production wiring switches to MODULE.bazel + a real module
	// extension that pulls per-kind translator binaries from a
	// proper module.
	if err := writeFile(filepath.Join(outDir, "WORKSPACE.bazel"), workspaceBazel(elem)); err != nil {
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
	srcStage := filepath.Join(elemPkg, "sources")
	if err := os.RemoveAll(srcStage); err != nil {
		return err
	}
	if err := copyTree(elem.AbsSourceDir, srcStage); err != nil {
		return fmt.Errorf("stage element sources: %w", err)
	}
	if err := writeFile(filepath.Join(elemPkg, "BUILD.bazel"), elementBuild(elem)); err != nil {
		return err
	}

	return nil
}

func workspaceBazel(elem *element) string {
	return fmt.Sprintf(`workspace(name = "meta_project_spike_%s")

# Project A only runs genrules (one per element invoking the
# per-kind translator). It needs no rules_cc — that lives in
# project B, which builds the converted output.
`, sanitizeForWorkspace(elem.Name))
}

// sanitizeForWorkspace returns a bazel-workspace-name-safe string:
// only [A-Za-z0-9_], with hyphens replaced by underscores. Bazel
// rejects workspace names containing '-'.
func sanitizeForWorkspace(s string) string {
	return strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(s)
}

func elementBuild(elem *element) string {
	return fmt.Sprintf(`# Generated by write-a-spike. Do not edit by hand.

package(default_visibility = ["//visibility:public"])

# All real sources, declared as srcs to convert-element. The spike
# stages every file as real (no zero_files yet); the follow-up
# commit narrows this via read_paths.json feedback.
# CMakeLists.txt is split out so the genrule can $(location) it for
# the source-root anchor; the rest of the tree flows through the
# filegroup. Bazel rejects $(location) on a filegroup-internal path,
# so the CMakeLists has to be a top-level src.
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
