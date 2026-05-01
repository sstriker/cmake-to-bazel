package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func init() { registerHandler(cmakeHandler{}) }

// cmakeHandler renders a kind:cmake element. The project-A side is a
// genrule invoking convert-element under Bazel's action graph; the
// project-B side is a placeholder the driver script overwrites with
// project A's converted BUILD.bazel.out plus the user's full source
// tree (project B's cc_library compiles against the real source bytes).
type cmakeHandler struct{}

func (cmakeHandler) Kind() string           { return "cmake" }
func (cmakeHandler) NeedsSources() bool     { return true }
func (cmakeHandler) HasProjectABuild() bool { return true }

// DefaultReadPathsPatterns returns the cmake-converter default
// shadow-tree narrowing rules. Per-element <element>.read-paths.txt
// rules layer on top.
//
// Today: empty (no defaults). The patterns mechanism is in place
// but the cmake defaults aren't tuned yet — empirical narrowing
// data from the FDSDK reality-check probe will inform what's
// safe to default-include. Until that lands, every cmake element
// without an explicit read-paths.txt stages everything as real
// (the conservative pre-narrowing behaviour); per-element files
// remain the only narrowing path.
//
// Pinning the converter version pins these defaults, so cache-
// key stability follows the converter release contract.
func (cmakeHandler) DefaultReadPathsPatterns() *readPathsPatterns {
	return nil
}

func (cmakeHandler) RenderA(elem *element, elemPkg string) error {
	// FUSE-sources mode (--use-fuse-sources): skip on-disk staging
	// entirely; the per-element BUILD references @src_<key>//:tree
	// directly. The repo rule (rules/sources.bzl) ctx.symlinks the
	// file tree from the cas-fuse mount, so the genrule's $(SRCS)
	// resolves to bazel-bin paths that the kernel serves through
	// FUSE. Only viable for single-source-no-directory cmake elements
	// today; multi-source / directory-suffix elements fall back to
	// the staging path for now (additional repo composition needed).
	if useFuseSourcesGlobal && !cmakeMultiSource(elem) {
		k := sourceKey(elem.Sources[0])
		if k != "" {
			// Run the same partitionSources walk as the staging
			// path: the source-cache local tree gives us the
			// universe; converter defaults + per-element patterns
			// partition it into RealPaths / ZeroPaths. Real entries
			// flow as enumerated @src_<k>//:tree_dir/<path> labels;
			// zero entries flow through the same zero_files starlark
			// rule the staging path uses. cmake walks SHADOW inside
			// the genrule action, which matches: real bytes for real
			// files (streamed from CAS via @src_<k>//), empty bytes
			// for zero stubs. Narrowing applies; bytes flow only
			// when the action reads them.
			if err := partitionSources(elem); err != nil {
				return fmt.Errorf("partition sources (fuse mode): %w", err)
			}
			return writeFile(filepath.Join(elemPkg, "BUILD.bazel"),
				cmakeElementBuildFuse(elem, k))
		}
		// Fall through: kind:local sources have no source-key, so
		// they can't be served via @src_<key>//. They still take
		// the staging path below.
	}

	srcStage := filepath.Join(elemPkg, "sources")
	if err := os.RemoveAll(srcStage); err != nil {
		return err
	}

	// Read-set narrowing only applies to single-source-no-directory
	// elements (the v1 fixture shape). Multi-source elements or any
	// source with a Directory subpath fall back to "stage everything
	// as real" — narrowing across multiple source roots needs
	// additional bookkeeping that lands when an FDSDK fixture forces
	// it.
	if cmakeMultiSource(elem) {
		elem.RealPaths = nil
		elem.ZeroPaths = nil
		if err := stageAllSources(elem, srcStage); err != nil {
			return err
		}
		return writeFile(filepath.Join(elemPkg, "BUILD.bazel"), cmakeElementBuild(elem))
	}

	if err := partitionSources(elem); err != nil {
		return fmt.Errorf("partition sources: %w", err)
	}
	// Real sources are staged as files in project A's source tree;
	// the glob in the per-element BUILD picks them up. Zero stubs are
	// NOT staged on disk — they're produced at action time by the
	// zero_files starlark rule and merged into $SRC_ROOT inside the
	// genrule's cmd. The rendered project-A tree only contains the
	// files the user can actually inspect; the empty stubs are an
	// action-graph detail Bazel handles.
	src := elem.Sources[0].AbsPath
	for _, rel := range elem.RealPaths {
		if err := copyFile(filepath.Join(src, rel), filepath.Join(srcStage, rel)); err != nil {
			return fmt.Errorf("stage real source %s: %w", rel, err)
		}
	}
	return writeFile(filepath.Join(elemPkg, "BUILD.bazel"), cmakeElementBuild(elem))
}

// cmakeMultiSource reports whether this cmake element's sources
// are in any shape that prevents the single-source-tree narrowing
// path: >1 source declared, the lone source has a non-empty
// Directory subpath, or the source has no on-disk tree to walk
// (kind:git_repo / kind:tar / etc. with no --source-cache hit —
// AbsPath is empty). All these shapes flow through stageAllSources
// without path-narrowing.
func cmakeMultiSource(elem *element) bool {
	if len(elem.Sources) != 1 {
		return true
	}
	if elem.Sources[0].Directory != "" {
		return true
	}
	return elem.Sources[0].AbsPath == ""
}

func (cmakeHandler) RenderB(elem *element, elemPkg string) error {
	// Stage the FULL source tree (no narrowing). Project B's
	// cc_library needs the real source bytes to compile, so this is
	// the user's tree verbatim. (writeProjectB already cleared and
	// re-created elemPkg before calling us.) Multi-source elements
	// honor each source's Directory subpath via stageAllSources.
	if err := stageAllSources(elem, elemPkg); err != nil {
		return err
	}
	// Placeholder BUILD; the driver script overwrites this after
	// project-A's bazel build produces the converter's
	// BUILD.bazel.out. Without the placeholder, Bazel would try to
	// load() rules_cc against an empty package and fail with a
	// confusing error before the stage step ran; the placeholder
	// makes the staging-not-yet-run state explicit.
	placeholder := fmt.Sprintf(`# Placeholder for cmd/write-a-rendered project B.
# The driver script overwrites this file with project A's
# bazel-bin/elements/%s/BUILD.bazel.out (the converter's output)
# after the project-A bazel build succeeds. If this file is still
# the placeholder when project B's bazel build runs, the staging
# step was skipped.
filegroup(name = "BUILD_NOT_YET_STAGED", srcs = [])
`, elem.Name)
	return writeFile(filepath.Join(elemPkg, "BUILD.bazel"), placeholder)
}

// partitionSources walks the element's source tree and decides which
// paths flow as real files vs zero stubs into project A.
//
//   - With no <element>.read-paths.txt patterns file
//     (elem.Patterns == nil), every file is real. The conservative
//     "no narrowing" default; matches pre-#61 behaviour without
//     opt-in.
//   - With patterns present, applyReadPathsPatterns partitions the
//     source-relative path universe per the include / exclude
//     rules. CMakeLists.txt files always stay real (cmake parses
//     the entry CMakeLists before any narrowing has a chance to
//     matter; auto-including them keeps cmake configure correct).
//
// Caller (cmakeHandler.RenderA) gates this on the single-source-no-
// directory case (cmakeMultiSource(elem) == false), so reading
// elem.Sources[0].AbsPath here is unconditional.
func partitionSources(elem *element) error {
	root := elem.Sources[0].AbsPath
	universe := []string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
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

	// Compose: converter defaults first, per-element override
	// rules concatenated after. applyReadPathsPatterns evaluates
	// rules left-to-right so defaults set the conservative
	// baseline and per-element entries refine.
	patterns := composeReadPathsPatterns(cmakeHandler{}.DefaultReadPathsPatterns(), elem.Patterns)
	elem.RealPaths, elem.ZeroPaths = applyReadPathsPatterns(patterns, universe)
	return nil
}

// composeReadPathsPatterns layers a per-element override file on
// top of converter defaults. nil + nil → nil (default-no-narrow);
// nil + b → b; a + nil → a; a + b → concatenated rules with
// defaults first.
func composeReadPathsPatterns(defaults, overrides *readPathsPatterns) *readPathsPatterns {
	if defaults == nil && overrides == nil {
		return nil
	}
	if defaults == nil {
		return overrides
	}
	if overrides == nil {
		return defaults
	}
	out := &readPathsPatterns{}
	out.Rules = append(out.Rules, defaults.Rules...)
	out.Rules = append(out.Rules, overrides.Rules...)
	return out
}

func cmakeElementBuild(elem *element) string {
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

# Typed exports project B consumes. Phase 1/2 emit the converter's
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

// cmakeElementBuildFuse renders the FUSE-sources variant of the
// per-element BUILD.bazel: srcs come from @src_<key>//:tree
// (which the sources extension's repo rule symlinks into the
// cas-fuse mount), and the genrule's cmd strips up to and
// including "tree_dir/" — matching the symlink target name
// the rule (rules/sources.bzl) creates.
//
// v1 doesn't emit zero_files in this mode: read-paths narrowing
// across a CAS-served tree needs additional plumbing
// (the universe is the FileNodes in the Directory proto, not a
// glob over an on-disk staging dir). All sources flow as real;
// the action-cache stability story for FUSE mode is "the
// Directory digest changes only when the source bytes change",
// which is already strictly stronger than today's glob().
func cmakeElementBuildFuse(elem *element, sourceKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Generated by cmd/write-a (--use-fuse-sources). Do not edit by hand.

package(default_visibility = ["//visibility:public"])
`)
	// zero_files target: paths cmake's directory walk sees but
	// doesn't read. Materialised at action time as zero-length
	// stubs whose merkle is the empty SHA — the action input
	// stays content-stable across edits to non-real source files.
	if len(elem.ZeroPaths) > 0 {
		fmt.Fprintf(&b, `
load("//rules:zero_files.bzl", "zero_files")

zero_files(
    name = "%[1]s_zero_stubs",
    paths = [
`, elem.Name)
		for _, p := range elem.ZeroPaths {
			fmt.Fprintf(&b, "        %q,\n", "tree_dir/"+p)
		}
		fmt.Fprintf(&b, `    ],
)
`)
	}

	// Real-files srcs: enumerate per-file labels into the
	// @src_<key>// repo (each file reachable as a digest-stable
	// Bazel label, exports_files'd by the repo rule). When no
	// patterns narrow the universe, partitionSources puts every
	// path in RealPaths; when patterns are active, only the
	// narrowed-real subset lands here and the rest flows through
	// the zero_stubs target above.
	srcsList := ""
	if len(elem.RealPaths) > 0 {
		// Single-line srcs list with @src_<k>//:tree_dir/<path>
		// labels. Sorted for determinism (RealPaths is already
		// sorted by partitionSources).
		var labels []string
		for _, p := range elem.RealPaths {
			labels = append(labels, fmt.Sprintf("%q", "@src_"+sourceKey+"//:tree_dir/"+p))
		}
		srcsList = strings.Join(labels, ", ")
	}
	if len(elem.ZeroPaths) > 0 {
		zeroRef := fmt.Sprintf("%q", ":"+elem.Name+"_zero_stubs")
		if srcsList == "" {
			srcsList = zeroRef
		} else {
			srcsList += ", " + zeroRef
		}
	}
	// Fallback: when the element has no patterns + no source-cache
	// hit (so partitionSources didn't run / produced nothing),
	// reach for the opaque :tree filegroup so we still feed
	// convert-element a non-empty input set. This matches the
	// pre-narrowing "everything real" default.
	if srcsList == "" {
		srcsList = fmt.Sprintf("%q", "@src_"+sourceKey+"//:tree")
	}

	fmt.Fprintf(&b, `
genrule(
    name = "%[1]s_converted",
    srcs = [%[3]s],
    outs = [
        "BUILD.bazel.out",
        "read_paths.json",
        "cmake-config-bundle.tar",
    ],
    cmd = """
        # Materialise the narrowed source root inside the action
        # sandbox: real srcs (CAS-served via @src_<key>//) and
        # zero stubs (rule-generated empties) both arrive under
        # path components ending in tree_dir/<rel>. Strip up to
        # and including the last tree_dir/ to recover the
        # source-relative suffix.
        SHADOW="$$(mktemp -d)"
        for src in $(SRCS); do
            rel="$${src##*tree_dir/}"
            mkdir -p "$$SHADOW/$$(dirname "$$rel")"
            cp -L "$$src" "$$SHADOW/$$rel"
        done
        BUNDLE_DIR="$$(mktemp -d)"
        $(location //tools:convert-element) \\
            --source-root="$$SHADOW" \\
            --source-key="%[2]s" \\
            --out-build="$(location BUILD.bazel.out)" \\
            --out-bundle-dir="$$BUNDLE_DIR" \\
            --out-read-paths="$(location read_paths.json)"
        tar -cf "$(location cmake-config-bundle.tar)" -C "$$BUNDLE_DIR" .
    """,
    tools = ["//tools:convert-element"],
)

filegroup(
    name = "build_bazel",
    srcs = [":%[1]s_converted"],
    output_group = "BUILD.bazel.out",
)
`, elem.Name, sourceKey, srcsList)
	return b.String()
}
