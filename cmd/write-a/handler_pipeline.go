package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// pipelineDefaults is the per-kind default phase command set. Each
// "coarse-grained install pipeline" kind (manual, make, autotools,
// pyproject, …) is a pipelineHandler instance with its own defaults;
// .bst-supplied commands override per phase.
//
// nil vs empty-list semantics: a kind that supplies a default for a
// phase but the .bst doesn't override gets the default; a .bst that
// explicitly sets `phase-commands: []` gets nothing for that phase.
// pipelineCfg uses pointer-to-slice fields to distinguish.
type pipelineDefaults struct {
	Configure []string
	Build     []string
	Install   []string
	Strip     []string
}

// pipelineHandler is the generic coarse-grained "install pipeline"
// handler implementation. Its identity (kindName), default phase
// commands, and default per-kind variables come from the registered
// instance; everything else — source staging, BUILD rendering,
// project-B placeholder — is shared.
//
// Concretely, a single source file per kind looks like:
//
//	func init() {
//	    registerHandler(pipelineHandler{
//	        kindName: "make",
//	        defaultVars: map[string]string{
//	            "make-args":         "",
//	            "make-install-args": `DESTDIR="%{install-root}" install`,
//	        },
//	        defaults: pipelineDefaults{
//	            Build:   []string{"make %{make-args}"},
//	            Install: []string{"make -j1 %{make-install-args}"},
//	        },
//	    })
//	}
//
// The element's `variables:` block overrides defaultVars per
// element; project-level defaults sit one layer below (see
// variables.go).
type pipelineHandler struct {
	kindName    string
	defaultVars map[string]string
	defaults    pipelineDefaults
}

func (h pipelineHandler) Kind() string           { return h.kindName }
func (h pipelineHandler) NeedsSources() bool     { return true }
func (h pipelineHandler) HasProjectABuild() bool { return true }

// pipelineCfg is the .bst `config:` block shape every pipeline-kind
// element shares. Pointer-to-slice so the renderer can distinguish
// "not set in .bst, fall back to handler defaults" (nil) from
// "explicitly cleared in .bst" (non-nil empty slice).
type pipelineCfg struct {
	ConfigureCommands *[]string `yaml:"configure-commands"`
	BuildCommands     *[]string `yaml:"build-commands"`
	InstallCommands   *[]string `yaml:"install-commands"`
	StripCommands     *[]string `yaml:"strip-commands"`
}

func (h pipelineHandler) RenderA(elem *element, elemPkg string) error {
	var cfg pipelineCfg
	// Decode the .bst's config: only when it's actually present;
	// otherwise leave cfg zero (all phases nil → use defaults).
	if !elem.Bst.Config.IsZero() {
		if err := elem.Bst.Config.Decode(&cfg); err != nil {
			return fmt.Errorf("element %q (kind:%s): parse config: block: %w", elem.Name, h.kindName, err)
		}
	}
	// Per-phase fallback: nil pointer → handler default; non-nil
	// pointer (even empty slice) → use what the .bst declared.
	configure := mergeWithDefault(cfg.ConfigureCommands, h.defaults.Configure)
	build := mergeWithDefault(cfg.BuildCommands, h.defaults.Build)
	install := mergeWithDefault(cfg.InstallCommands, h.defaults.Install)
	strip := mergeWithDefault(cfg.StripCommands, h.defaults.Strip)

	// Compose the variable scope (project defaults < kind defaults
	// < per-element overrides), expand recursively, and apply to
	// each phase command. References to undefined variables (typo
	// in a .bst) error out here rather than silently emitting a
	// literal %{misspelled} into the genrule cmd.
	vars, err := resolveVars(h.defaultVars, elem.Bst.Variables)
	if err != nil {
		return fmt.Errorf("element %q (kind:%s): resolve variables: %w", elem.Name, h.kindName, err)
	}
	configure, err = substituteCmds(configure, vars, elem.Name, h.kindName, "configure-commands")
	if err != nil {
		return err
	}
	build, err = substituteCmds(build, vars, elem.Name, h.kindName, "build-commands")
	if err != nil {
		return err
	}
	install, err = substituteCmds(install, vars, elem.Name, h.kindName, "install-commands")
	if err != nil {
		return err
	}
	strip, err = substituteCmds(strip, vars, elem.Name, h.kindName, "strip-commands")
	if err != nil {
		return err
	}

	if err := stagePipelineSources(elem, elemPkg); err != nil {
		return err
	}
	return writeFile(filepath.Join(elemPkg, "BUILD.bazel"),
		renderPipelineBuild(elem, configure, build, install, strip))
}

// substituteCmds applies the resolved variable map to every command
// in a phase. The phase / kind / element labels feed the error
// message so a stray %{typo} surfaces with enough context to
// locate it in the .bst.
func substituteCmds(cmds []string, vars map[string]string, elemName, kindName, phase string) ([]string, error) {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		s, err := substituteCmd(c, vars)
		if err != nil {
			return nil, fmt.Errorf("element %q (kind:%s) %s[%d]: %w", elemName, kindName, phase, i, err)
		}
		out[i] = s
	}
	return out, nil
}

func (h pipelineHandler) RenderB(elem *element, elemPkg string) error {
	// All pipeline kinds expose their primary output as
	// install_tree.tar in project A; project B's wrapper for
	// downstream consumers is a follow-up. For now, a placeholder
	// (same shape as kind:cmake's BUILD_NOT_YET_STAGED) marks the
	// "driver hasn't post-processed yet" state.
	body := fmt.Sprintf(`# Generated by cmd/write-a. Do not edit by hand.
# kind:%[2]s — install tree produced by project A's genrule.
# The driver script overwrites this file with the typed-filegroup
# wrapper once that lands; until then, downstream consumers fetch
# install_tree.tar from project A directly.
filegroup(name = "BUILD_NOT_YET_STAGED", srcs = [])
`, elem.Name, h.kindName)
	return writeFile(filepath.Join(elemPkg, "BUILD.bazel"), body)
}

// mergeWithDefault returns the user-supplied slice when non-nil,
// otherwise the default. The empty-slice case (user explicitly set
// `phase-commands: []`) is preserved as-is via the pointer check.
func mergeWithDefault(user *[]string, def []string) []string {
	if user == nil {
		return def
	}
	return *user
}

// stagePipelineSources copies the .bst's kind:local source tree into
// the project-A package so the genrule's `srcs = glob(["sources/**"])`
// picks them up. No narrowing: pipeline kinds' commands can read any
// path arbitrarily, so feedback-driven zero stubs don't apply.
func stagePipelineSources(elem *element, elemPkg string) error {
	srcStage := filepath.Join(elemPkg, "sources")
	if err := copyTree(elem.AbsSourceDir, srcStage); err != nil {
		return fmt.Errorf("stage pipeline sources for %q: %w", elem.Name, err)
	}
	return nil
}

// renderPipelineBuild renders the per-element BUILD for a coarse-
// grained pipeline kind: a glob over staged sources + a genrule
// whose cmd stages the sources into a fresh work dir, runs each
// phase's commands in order, then tars %{install-root} as the
// element's primary output (install_tree.tar).
//
// Phase commands arrive here already variable-expanded (RenderA
// runs each through substituteCmd before getting here), so the
// only thing the genrule cmd binds at action time is the runtime
// sentinels: $$INSTALL_ROOT (the per-action mktemp dir tarred as
// install_tree.tar) and $$BUILD_ROOT (the staged source dir, also
// the cwd where phase commands run).
func renderPipelineBuild(elem *element, configure, build, install, strip []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Generated by cmd/write-a. Do not edit by hand.

package(default_visibility = ["//visibility:public"])

filegroup(
    name = "%[1]s_sources",
    srcs = glob(["sources/**"]),
)

genrule(
    name = "%[1]s_install",
    srcs = [":%[1]s_sources"],
    outs = ["install_tree.tar"],
    cmd = """
        # Snapshot the exec root before any cd. Bazel resolves
        # location expressions to exec-root-relative paths, and the
        # user-provided commands below cd into the staged work dir,
        # so we restore PWD before tarring the install tree.
        EXEC_ROOT="$$PWD"
        # Stage sources into a fresh work dir; honor the original
        # source-relative layout via the same shadow-merge pattern
        # the cmake handler uses (strip the leading "sources/" of
        # each $(SRCS) entry to recover the source-relative path).
        BUILD_ROOT="$$(mktemp -d)"
        for src in $(SRCS); do
            rel="$${src##*sources/}"
            mkdir -p "$$BUILD_ROOT/$$(dirname "$$rel")"
            cp -L "$$src" "$$BUILD_ROOT/$$rel"
        done
        cd "$$BUILD_ROOT"

        # Runtime variable bindings (every other %%{...} reference is
        # already expanded at codegen time by handler_pipeline's
        # substituteCmd):
        #   $$INSTALL_ROOT — DESTDIR-style staging dir; tarred as
        #                    the element's output below.
        #   $$BUILD_ROOT   — the staged source dir (set above).
        export INSTALL_ROOT="$$(mktemp -d)"
        export PATH=/usr/local/bin:/usr/bin:/bin

%[2]s
        # Tar the install tree as the element's primary output.
        cd "$$EXEC_ROOT"
        tar -cf "$(location install_tree.tar)" -C "$$INSTALL_ROOT" .
    """,
)
`, elem.Name, renderPipelineCommands(configure, build, install, strip))
	return b.String()
}

// renderPipelineCommands flattens the four phase command lists into
// the genrule's cmd, in canonical order. The commands arrive here
// already variable-expanded (RenderA → substituteCmd), so the only
// shaping this layer does is per-phase header comments and the
// "no commands at all" fallthrough. Each command runs under `set -e`
// (the genrule cmd block is a single shell script); failures at any
// step abort the action.
func renderPipelineCommands(configure, build, install, strip []string) string {
	var lines []string
	for _, phase := range []struct {
		label    string
		commands []string
	}{
		{"configure", configure},
		{"build", build},
		{"install", install},
		{"strip", strip},
	} {
		if len(phase.commands) == 0 {
			continue
		}
		lines = append(lines, "        # === "+phase.label+" ===")
		for _, c := range phase.commands {
			lines = append(lines, "        "+c)
		}
	}
	if len(lines) == 0 {
		// No commands at all (e.g., a kind:manual element with
		// empty config:). The genrule produces an empty install
		// tree — useful only as a degenerate fixture, but a real
		// element will always declare at least install-commands
		// or pull defaults from the kind handler.
		lines = append(lines, "        # (no pipeline commands declared; install tree will be empty)")
	}
	return strings.Join(lines, "\n")
}
