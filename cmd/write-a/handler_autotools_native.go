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

// autotoolsTraceExtension is the pipelineExtension that wires
// the build-tracer + convert-element-autotools steps into the
// rendered pipeline cmd. Outputs: install_tree.tar (existing)
// + BUILD.bazel.out (converter output) + make-db.txt
// (post-build dump of `make -np`, fed back to the converter as
// a structural hint). Tools: build-tracer + convert-element-
// autotools (both staged into project A's tools/ at write-a
// time). When hasImports is true, imports.json is added to the
// genrule's srcs and the converter step's `--imports-manifest`
// flag references it via $(location imports.json).
func autotoolsTraceExtension(hasImports bool) *pipelineExtension {
	ext := &pipelineExtension{
		WrapPipelineCmds: wrapAutotoolsPipelineCmds,
		AppendCmd:        autotoolsConverterStep(hasImports),
		ExtraOuts: []string{
			"BUILD.bazel.out",
			"make-db.txt",
			"install-mapping.json",
		},
		ExtraTools: []string{
			"//tools:build-tracer",
			"//tools:convert-element-autotools",
		},
	}
	if hasImports {
		ext.ExtraSrcs = []string{"imports.json"}
	}
	return ext
}

// wrapAutotoolsPipelineCmds rewrites the resolved
// configure/build/install commands block. Every command runs
// under build-tracer so a single trace.log captures the entire
// process tree (compile / archive / link / install execve
// calls). The trace lives in $$AUTOTOOLS_TRACE; the converter
// step (AppendCmd) reads it.
//
// We use one tracer invocation around the whole pipeline —
// configure + build + install — rather than per-phase, so the
// process-tree filtering is straightforward (one strace
// session, one trace file).
//
// Path note: the tool reference is anchored to $$EXEC_ROOT.
// Bazel resolves $(location //tools:build-tracer) to an
// exec-root-relative path; pipelineHandler's prelude already
// `cd "$$BUILD_ROOT"` by the time this runs, so the bare
// relative path wouldn't find the staged binary.
func wrapAutotoolsPipelineCmds(cmds string) string {
	return fmt.Sprintf(`        # Build-tracer wraps the entire configure/build/install
        # pipeline. The trace artifact captures every execve under
        # the build sandbox; convert-element-autotools (run by the
        # AppendCmd step) reads it to emit BUILD.bazel.out.
        export AUTOTOOLS_TRACE="$$(mktemp)"
        "$$EXEC_ROOT/$(location //tools:build-tracer)" --out="$$AUTOTOOLS_TRACE" -- sh -c '
%s
'`, cmds)
}

// autotoolsConverterStep is the shell snippet inserted between
// the (tracer-wrapped) pipeline cmds and the install-tree tar.
// Two sub-steps:
//
//  1. Dump `make -np` (dry-run + print-database) to capture
//     the post-build Makefile state — fully variable-resolved
//     after configure ran. Cwd is $$BUILD_ROOT here (the
//     pipeline's `cd "$$BUILD_ROOT"` is still in effect), so
//     make finds its Makefile.
//  2. Run convert-element-autotools against the trace + the
//     captured make database, emitting BUILD.bazel.out.
//
// `make -np` may exit non-zero on a healthy build (it skips
// the actual build but still attempts `nothing to do` — safe
// to ignore). The `|| true` keeps the genrule action
// successful even if make is unhappy with the dry run.
//
// When hasImports is true, --imports-manifest=$(location
// imports.json) threads through so cross-element `-l<name>`
// flags resolve to the right Bazel labels.
func autotoolsConverterStep(hasImports bool) string {
	importsFlag := ""
	if hasImports {
		importsFlag = ` \
            --imports-manifest="$(location imports.json)"`
	}
	return fmt.Sprintf(`        # Capture the post-build make database. Run from
        # $$BUILD_ROOT (pipeline cmds left us there); `+"`make -np`"+`
        # dumps every rule, variable, and prereq edge after
        # configure-time substitutions are baked in. Tolerate
        # non-zero exit (make's dry-run can grumble about
        # "nothing to do" or missing optional targets).
        make -np > "$$EXEC_ROOT/$(location make-db.txt)" 2>/dev/null || true

        # Trace + make-db -> native cc_library / cc_binary
        # BUILD.bazel.out. Output goes through bazel's normal
        # action cache (buildbarn in CI), which is what gives us
        # cross-node convergence — same trace + same converter
        # version => same BUILD.bazel.out everywhere.
        cd "$$EXEC_ROOT"
        $(location //tools:convert-element-autotools) \
            --trace="$$AUTOTOOLS_TRACE" \
            --make-db="$(location make-db.txt)" \
            --out-install-mapping="$(location install-mapping.json)" \
            --out-build="$(location BUILD.bazel.out)"%s`, importsFlag)
}
