package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// init registers kind:autotools. The handler always falls back
// to the coarse install-pipeline shape; when --convert-element-
// autotools is supplied, it additionally wraps the build cmd in
// build-tracer + runs convert-element-autotools to emit a native
// BUILD.bazel.out alongside the install_tree.tar.
//
// One genrule with two outputs (install_tree.tar +
// BUILD.bazel.out). Bazel's action cache (buildbarn in CI)
// handles convergence — same source + same toolchain + same
// converter version → same action result, shared across nodes
// via the existing remote-cache plumbing. No separate registry
// needed; the "B → A feedback" lives entirely inside the
// Bazel-action graph.
func init() {
	registerHandler(autotoolsHandler{})
}

// autotoolsConfig holds the render-time settings for the
// trace-driven autotools converter. Populated from main()'s
// flags before the per-element render loop runs. Empty
// convertBin disables the trace+convert wrap entirely
// (rendered output is the unmodified pipeline shape).
//
// Package-level state keeps the kindHandler interface small
// (RenderA / RenderB don't take a config arg) while letting
// the autotools handler decide per-element whether to install
// the extension hooks.
var autotoolsConfig struct {
	convertBin string // absolute path to convert-element-autotools
	tracerBin  string // absolute path to build-tracer
}

// autotoolsHandler picks the right pipelineHandler shape based
// on the global autotoolsConfig. Without a converter binary,
// the coarse install_tree.tar pipeline is the rendered shape;
// with it, the pipelineExtension wraps the cmd in build-tracer
// and runs convert-element-autotools after the install phase.
type autotoolsHandler struct{}

func (autotoolsHandler) Kind() string                                 { return "autotools" }
func (autotoolsHandler) NeedsSources() bool                           { return true }
func (autotoolsHandler) HasProjectABuild() bool                       { return true }
func (autotoolsHandler) DefaultReadPathsPatterns() *readPathsPatterns { return nil }

func (autotoolsHandler) RenderA(elem *element, elemPkg string) error {
	h, err := autotoolsPipelineHandlerForElement(elem, elemPkg)
	if err != nil {
		return err
	}
	return h.RenderA(elem, elemPkg)
}

func (autotoolsHandler) RenderB(elem *element, elemPkg string) error {
	return autotoolsBasePipelineHandler().RenderB(elem, elemPkg)
}

// autotoolsPipelineHandlerForElement builds the per-element
// pipelineHandler. Side effect: when the trace-driven native
// path is enabled AND the element has cross-element deps, an
// imports.json is rendered next to the BUILD that maps each
// dep's link library to its Bazel label (the convention bind
// from the kind:cmake handler — see writeAutotoolsImportsManifest).
// The extension's ExtraSrcs lists imports.json so the genrule
// stages it; AppendCmd's --imports-manifest flag references it
// via $(location imports.json).
//
// Without --convert-element-autotools / --build-tracer-bin, the
// returned handler has no extension — the unmodified coarse
// install_tree.tar pipeline renders.
func autotoolsPipelineHandlerForElement(elem *element, elemPkg string) (pipelineHandler, error) {
	h := autotoolsBasePipelineHandler()
	if autotoolsConfig.convertBin == "" {
		return h, nil
	}
	hasImports, err := writeAutotoolsImportsManifest(elem, elemPkg)
	if err != nil {
		return pipelineHandler{}, err
	}
	h.extension = autotoolsTraceExtension(hasImports)
	return h, nil
}

// writeAutotoolsImportsManifest renders an imports.json next
// to the element's BUILD when there are cross-element deps to
// resolve. Convention bind: each dep "<name>" maps to
// link_libraries=["<name>"] → "//elements/<name>:<name>".
// Mirrors writeCmakeImportsManifest's shape, except the
// resolution key is the link-library name (matched against
// `-l<name>` flags by convert-element-autotools'
// LookupLinkLibrary) rather than the cmake target name.
//
// Returns (true, nil) when imports.json was written;
// (false, nil) when the element has no deps that need
// cross-element resolution (no file written).
func writeAutotoolsImportsManifest(elem *element, elemPkg string) (bool, error) {
	if len(elem.Deps) == 0 {
		return false, nil
	}
	var b strings.Builder
	b.WriteString(`{
  "version": 1,
  "elements": [
`)
	first := true
	for _, dep := range elem.Deps {
		if dep == nil {
			continue
		}
		if !first {
			b.WriteString(",\n")
		}
		first = false
		fmt.Fprintf(&b, `    {
      "name": %q,
      "exports": [
        {
          "cmake_target": %q,
          "bazel_label": "//elements/%s:%s",
          "link_libraries": [%q]
        }
      ]
    }`, dep.Name, dep.Name+"::"+dep.Name, dep.Name, dep.Name, dep.Name)
	}
	b.WriteString(`
  ]
}
`)
	if first {
		// All deps were nil — shouldn't happen but tolerated.
		return false, nil
	}
	if err := writeFile(filepath.Join(elemPkg, "imports.json"), b.String()); err != nil {
		return false, err
	}
	return true, nil
}

// autotoolsTraceExtension wires build-tracer into the install
// genrule + adds a sibling `<elem>_converted` genrule that
// runs convert-element-autotools against the trace. Two
// Bazel actions, two cache keys:
//
//   - `<elem>_install`: full source tree as input. Wraps
//     `./configure && make && make install` under
//     build-tracer; outputs install_tree.tar + trace.log +
//     make-db.txt. Source content edits invalidate this
//     action's cache key (the build re-runs).
//   - `<elem>_converted`: trace.log + make-db.txt (+
//     optional imports.json) as input. Runs
//     convert-element-autotools; outputs BUILD.bazel.out +
//     install-mapping.json. **Cache key narrows to the
//     trace + make-db**, so a comment-only source edit
//     re-runs _install but its trace + make-db come out
//     byte-identical → _converted cache hits → project B's
//     staged BUILD doesn't churn.
//
// Mirrors kind:cmake's narrow-cache story: cmake's convert
// action depends only on configure-time read paths via
// zero_files; everything else changes don't invalidate the
// converter. For autotools the build IS the introspection,
// so we can't avoid re-running the build — but we can cache
// the conversion narrowly.
func autotoolsTraceExtension(hasImports bool) *pipelineExtension {
	ext := &pipelineExtension{
		WrapPipelineCmds: wrapAutotoolsPipelineCmds,
		AppendCmd:        autotoolsMakeDBStep(),
		ExtraOuts: []string{
			"trace.log",
			"make-db.txt",
		},
		ExtraTools: []string{
			"//tools:build-tracer",
		},
		SiblingRules: func(elemName string) string {
			return autotoolsConvertedSiblingRule(elemName, hasImports)
		},
	}
	if hasImports {
		// imports.json is consumed by `<elem>_converted`,
		// not `<elem>_install` — but write-a generates it
		// at render time and stages it in the element pkg.
		// The sibling rule's srcs reference it via the
		// package-relative path, so _install doesn't need
		// it as a srcs entry.
	}
	return ext
}

// wrapAutotoolsPipelineCmds rewrites the resolved
// configure/build/install commands block. Every command runs
// under build-tracer so a single trace.log captures the entire
// process tree (compile / archive / link / install execve
// calls). The trace artifact is a real Bazel output of the
// install genrule — the sibling `<elem>_converted` rule
// consumes it.
//
// Path note: the tool reference is anchored to $$EXEC_ROOT.
// Bazel resolves $(location //tools:build-tracer) and
// $(location trace.log) to exec-root-relative paths;
// pipelineHandler's prelude already cd'd to $$BUILD_ROOT, so
// the bare relative paths wouldn't find them.
func wrapAutotoolsPipelineCmds(cmds string) string {
	return fmt.Sprintf(`        # Build-tracer wraps the entire configure/build/install
        # pipeline. The trace artifact captures every execve under
        # the build sandbox; convert-element-autotools (in the
        # sibling _converted rule) reads it to emit BUILD.bazel.out.
        "$$EXEC_ROOT/$(location //tools:build-tracer)" --out="$$EXEC_ROOT/$(location trace.log)" -- sh -c '
%s
'`, cmds)
}

// autotoolsMakeDBStep dumps the post-build make database to
// make-db.txt. Runs in $$BUILD_ROOT (pipeline's cd is still
// in effect) so make finds its Makefile. `make -np` may exit
// non-zero on a healthy build (e.g. "nothing to do" when
// targets are up-to-date), so `|| true` keeps the genrule
// successful.
//
// The convert step itself moved out of the install genrule
// into the sibling `<elem>_converted` rule — see
// autotoolsConvertedSiblingRule.
func autotoolsMakeDBStep() string {
	return `        # Capture the post-build make database. Run from
        # $$BUILD_ROOT (pipeline cmds left us there); ` + "`make -np`" + `
        # dumps every rule, variable, and prereq edge after
        # configure-time substitutions are baked in. Tolerate
        # non-zero exit (make's dry-run can grumble about
        # "nothing to do" or missing optional targets).
        make -np > "$$EXEC_ROOT/$(location make-db.txt)" 2>/dev/null || true`
}

// autotoolsConvertedSiblingRule renders the
// `<elem>_converted` genrule. Consumes the install rule's
// trace.log + make-db.txt outputs; runs convert-element-
// autotools to produce BUILD.bazel.out + install-mapping.json.
//
// Cache-key narrowing: this action's input set is just the
// trace + make-db (+ optional imports.json). A comment-only
// edit in a source file invalidates `<elem>_install` (which
// re-runs the build) but leaves trace.log + make-db.txt
// byte-identical → `<elem>_converted`'s cache key is
// unchanged → cache hit → BUILD.bazel.out reused → project
// B's staged BUILD doesn't churn → consumers don't rebuild.
//
// `<elem>_install`'s outputs are referenced via the local
// labels `:trace.log` and `:make-db.txt` (Bazel exposes each
// genrule output as a sibling target with the output's
// filename).
func autotoolsConvertedSiblingRule(elemName string, hasImports bool) string {
	importsSrcs := ""
	importsFlag := ""
	if hasImports {
		importsSrcs = `, "imports.json"`
		importsFlag = ` \
            --imports-manifest="$(location imports.json)"`
	}
	return fmt.Sprintf(`# Convert step — narrow cache key. A comment-only edit in a
# source file re-runs `+"`%[1]s_install`"+` (the build), but its
# trace.log + make-db.txt outputs come out byte-identical, so
# this rule's action cache hits and BUILD.bazel.out is reused.
# Mirrors kind:cmake's read-paths-narrowed convert action.
genrule(
    name = "%[1]s_converted",
    srcs = [":trace.log", ":make-db.txt"%[2]s],
    outs = ["BUILD.bazel.out", "install-mapping.json"],
    cmd = """
        $(location //tools:convert-element-autotools) \
            --trace="$(location :trace.log)" \
            --make-db="$(location :make-db.txt)" \
            --out-build="$(location BUILD.bazel.out)" \
            --out-install-mapping="$(location install-mapping.json)"%[3]s
    """,
    tools = ["//tools:convert-element-autotools"],
)
`, elemName, importsSrcs, importsFlag)
}
