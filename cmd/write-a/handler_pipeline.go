package main

import (
	"fmt"
	"path/filepath"
	"sort"
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

func (h pipelineHandler) Kind() string                                 { return h.kindName }
func (h pipelineHandler) NeedsSources() bool                           { return true }
func (h pipelineHandler) HasProjectABuild() bool                       { return true }
func (h pipelineHandler) DefaultReadPathsPatterns() *readPathsPatterns { return nil }

// pipelineCfg is the .bst `config:` block shape every pipeline-kind
// element shares. Pointer-to-slice so the renderer can distinguish
// "not set in .bst, fall back to handler defaults" (nil) from
// "explicitly cleared in .bst" (non-nil empty slice).
//
// Commands is the kind:script shape: a single flat list of
// shell commands run in order. When set, it takes the place of
// install-commands (and the other phases stay empty); kind:script
// is the only kind that reads it. Mutually exclusive with the
// per-phase fields per BuildStream's contract.
type pipelineCfg struct {
	ConfigureCommands *[]string `yaml:"configure-commands"`
	BuildCommands     *[]string `yaml:"build-commands"`
	InstallCommands   *[]string `yaml:"install-commands"`
	StripCommands     *[]string `yaml:"strip-commands"`
	Commands          *[]string `yaml:"commands"`
}

// pipelinePhases is a set of resolved phase command lists ready for
// rendering. One per arch for conditional elements; one total
// otherwise. Env carries the per-action environment-variable
// bindings the cmd's prelude emits as `export K=V` lines —
// project.conf-level + element-level environments composed and
// variable-resolved (runtime sentinels mapped to their shell-var
// form so `GOPATH: %{build-root}` becomes `export
// GOPATH="$BUILD_ROOT"`, working under shell-time expansion the
// same way phase commands do).
type pipelinePhases struct {
	Configure, Build, Install, Strip []string
	Env                              [][2]string // ordered K, V pairs
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
	rawConfigure := mergeWithDefault(cfg.ConfigureCommands, h.defaults.Configure)
	rawBuild := mergeWithDefault(cfg.BuildCommands, h.defaults.Build)
	rawInstall := mergeWithDefault(cfg.InstallCommands, h.defaults.Install)
	rawStrip := mergeWithDefault(cfg.StripCommands, h.defaults.Strip)
	// kind:script's flat config:commands list — when present, it
	// takes the install-commands slot (other phases stay empty).
	// BuildStream's script plugin doesn't have configure / build /
	// strip phases.
	if cfg.Commands != nil {
		rawInstall = *cfg.Commands
	}

	dispatch, err := dispatchSpaceForElement(elem, elem.ProjectConfOptions)
	if err != nil {
		return err
	}

	// Resolution helper: variable-resolve + substitute every phase
	// command for a specific tuple (one entry per dispatch
	// variable). Empty tuple = unconditional resolution.
	resolveAt := func(tuple map[string]string) (pipelinePhases, error) {
		var vars map[string]string
		var err error
		if len(tuple) == 0 {
			vars, err = resolveVars(elem.ProjectConfVars, h.defaultVars, elem.Bst.Variables)
		} else {
			vars, err = resolveVarsForTuple(elem.ProjectConfVars, h.defaultVars, elem.Bst.Variables,
				tuple, elem.ProjectConfConditionals, elem.Bst.Conditionals)
		}
		if err != nil {
			return pipelinePhases{}, fmt.Errorf("element %q (kind:%s) resolve variables%s: %w",
				elem.Name, h.kindName, tupleSuffix(tuple), err)
		}
		var p pipelinePhases
		p.Configure, err = substituteCmds(rawConfigure, vars, elem.Name, h.kindName, "configure-commands")
		if err != nil {
			return pipelinePhases{}, err
		}
		p.Build, err = substituteCmds(rawBuild, vars, elem.Name, h.kindName, "build-commands")
		if err != nil {
			return pipelinePhases{}, err
		}
		p.Install, err = substituteCmds(rawInstall, vars, elem.Name, h.kindName, "install-commands")
		if err != nil {
			return pipelinePhases{}, err
		}
		p.Strip, err = substituteCmds(rawStrip, vars, elem.Name, h.kindName, "strip-commands")
		if err != nil {
			return pipelinePhases{}, err
		}
		// Compose env: project.conf-level (defaults) + element-level
		// (overrides). Substitute %{...} references against the
		// resolved variable map. Result is ordered K-V pairs so the
		// rendered `export K=V` lines are deterministic across runs.
		composedEnv := composeEnvironment(elem.ProjectConfEnvironment, elem.Bst.Environment)
		p.Env, err = substituteEnv(composedEnv, vars, elem.Name, h.kindName)
		if err != nil {
			return pipelinePhases{}, err
		}
		return p, nil
	}

	// FUSE-sources mode: skip on-disk staging when the element
	// has a single non-kind:local source with no Directory subpath
	// — the genrule will pull from @src_<key>//:tree (symlinked
	// into the cas-fuse mount by the rules/sources.bzl repo
	// rule). Multi-source / Directory / kind:local elements still
	// stage; same shape as cmakeHandler's gating.
	fuseKey := pipelineFuseEligible(elem)
	if fuseKey == "" {
		if err := stagePipelineSources(elem, elemPkg); err != nil {
			return err
		}
	}

	if len(dispatch) == 0 {
		// No (?): dispatch (or branches were folded into static
		// vars at graph-load time): single-string cmd.
		phases, err := resolveAt(nil)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(elemPkg, "BUILD.bazel"),
			renderPipelineBuild(elem, dispatch, []dispatchGroup{{Phases: phases}}, fuseKey))
	}

	// Cross-product of all dispatch variables' values. Each tuple
	// resolves to one phases set; group tuples by identical
	// resolution so the emitted select() doesn't duplicate identical
	// branches.
	type groupKey [4]string
	groupIdx := map[groupKey]int{}
	var groups []dispatchGroup
	for _, tuple := range cartesianTuples(dispatch) {
		phases, err := resolveAt(tuple)
		if err != nil {
			return err
		}
		key := groupKey{
			strings.Join(phases.Configure, "\x00"),
			strings.Join(phases.Build, "\x00"),
			strings.Join(phases.Install, "\x00"),
			strings.Join(phases.Strip, "\x00") + "\x01" + envKey(phases.Env),
		}
		if idx, ok := groupIdx[key]; ok {
			groups[idx].Tuples = append(groups[idx].Tuples, tuple)
		} else {
			groupIdx[key] = len(groups)
			groups = append(groups, dispatchGroup{
				Tuples: []map[string]string{tuple},
				Phases: phases,
			})
		}
	}
	// Dedup-collapse: if every dispatch tuple resolves identically,
	// the (?): block didn't actually affect the rendered cmd. Emit
	// the single-string form to keep the BUILD readable.
	if len(groups) == 1 {
		groups[0] = dispatchGroup{Phases: groups[0].Phases}
		return writeFile(filepath.Join(elemPkg, "BUILD.bazel"),
			renderPipelineBuild(elem, nil, groups, fuseKey))
	}
	return writeFile(filepath.Join(elemPkg, "BUILD.bazel"),
		renderPipelineBuild(elem, dispatch, groups, fuseKey))
}

// archSuffix shapes an arch identifier into a parenthetical for
// error messages: empty arch returns empty string, non-empty
// returns " (arch=<name>)".
func archSuffix(arch string) string {
	if arch == "" {
		return ""
	}
	return " (arch=" + arch + ")"
}

// tupleSuffix formats the dispatch tuple for error messages. Empty
// tuple → empty string; one entry → " (var=val)"; multiple entries
// → " (var1=val1, var2=val2, ...)" sorted by name.
func tupleSuffix(tuple map[string]string) string {
	if len(tuple) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tuple))
	for k := range tuple {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+tuple[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// composeEnvironment merges project.conf-level env (defaults) and
// element-level env (overrides), returning ordered K-V pairs sorted
// by key for stable rendering.
func composeEnvironment(projectEnv, elemEnv map[string]string) [][2]string {
	merged := map[string]string{}
	for k, v := range projectEnv {
		merged[k] = v
	}
	for k, v := range elemEnv {
		merged[k] = v
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, merged[k]})
	}
	return out
}

// substituteEnv runs each env value through substituteCmd against
// the resolved variable map. Errors carry the env-key context so a
// stray %{typo} surfaces with enough context to locate it.
func substituteEnv(env [][2]string, vars map[string]string, elemName, kindName string) ([][2]string, error) {
	out := make([][2]string, len(env))
	for i, kv := range env {
		v, err := substituteCmd(kv[1], vars)
		if err != nil {
			return nil, fmt.Errorf("element %q (kind:%s) environment[%q]: %w", elemName, kindName, kv[0], err)
		}
		out[i] = [2]string{kv[0], v}
	}
	return out, nil
}

// envKey is a stable string serialization of an env-pair list,
// used by the per-arch dedup hash so two arches with identical env
// + identical phases share a select() group.
func envKey(env [][2]string) string {
	var b strings.Builder
	for _, kv := range env {
		b.WriteString(kv[0])
		b.WriteByte('=')
		b.WriteString(kv[1])
		b.WriteByte('\x00')
	}
	return b.String()
}

// dispatchGroup is one branch of the select() the pipeline handler
// emits when an element's (?): block dispatches over one or more
// variables. A group with empty Tuples is the "single-string cmd"
// shape (no select); a group with a non-empty Tuples list becomes
// one entry per tuple in the select() dict (each tuple maps to the
// same Phases body — dedup-collapse groups identical resolutions).
//
// Each tuple is a complete assignment of values across all
// dispatch dimensions in the element's dispatch space. With one
// dispatch var, tuples are single-key maps; with multiple, tuples
// have one entry per dimension and the renderer emits combined
// config_settings (constraint_values + flag_values).
type dispatchGroup struct {
	Tuples []map[string]string
	Phases pipelinePhases
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

// stagePipelineSources copies the .bst's kind:local source trees
// into the project-A package so the genrule's
// `srcs = glob(["sources/**"])` picks them up. No narrowing:
// pipeline kinds' commands can read any path arbitrarily, so
// feedback-driven zero stubs don't apply. Multi-source elements
// honor each source's Directory subpath under sources/.
func stagePipelineSources(elem *element, elemPkg string) error {
	return stageAllSources(elem, filepath.Join(elemPkg, "sources"))
}

// pipelineFuseEligible reports whether a pipeline-shape element
// can take the FUSE-sources path: --use-fuse-sources flipped
// at startup, single source, no Directory subpath, and the
// source has a sourceKey (i.e. not kind:local). Returns the
// source key on success, "" otherwise.
//
// Same constraint envelope as cmakeHandler's gating; multi-
// source / Directory / kind:local cases fall back to staging.
// Repo-composition for multi-source elements is a follow-up.
func pipelineFuseEligible(elem *element) string {
	if !useFuseSourcesGlobal {
		return ""
	}
	if len(elem.Sources) != 1 {
		return ""
	}
	if elem.Sources[0].Directory != "" {
		return ""
	}
	return sourceKey(elem.Sources[0])
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
//
// groups carries one or more pre-resolved phase command sets:
//   - Single group with Arches==nil → renders cmd as a single
//     """...""" block (the no-conditional shape; covers every
//     v1 fixture and elements whose (?): blocks didn't actually
//     affect any rendered command).
//   - Multiple groups → renders cmd as `select({label: """...""",
//     ...})` over @platforms//cpu:* labels, one branch per arch
//     group. Lowering BuildStream's (?): per-arch overrides into
//     project-B Bazel-native multi-arch resolution rather than
//     baking write-a's host arch into the rendered cmd.
func renderPipelineBuild(elem *element, dispatch []dispatchVar, groups []dispatchGroup, fuseKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Generated by cmd/write-a. Do not edit by hand.

package(default_visibility = ["//visibility:public"])

`)
	if fuseKey == "" {
		fmt.Fprintf(&b, `filegroup(
    name = "%[1]s_sources",
    srcs = glob(["sources/**"]),
)

`, elem.Name)
	}

	// config_setting emission gates on the dispatch shape:
	//   - 0 dims (no dispatch) — none.
	//   - 1 dim, "platform" (target_arch only) — none; the cmd's
	//     select() arms reference @platforms//cpu:<v> directly.
	//   - 1 dim, "option" — one config_setting per option value.
	//   - 2+ dims (cross-product) — one config_setting per tuple,
	//     combining constraint_values (for platform dims) with
	//     flag_values (for option dims).
	if needsConfigSettings(dispatch) {
		b.WriteString(renderConfigSettings(dispatch, groups))
	}

	srcsAttr := fmt.Sprintf(`[":%s_sources"]`, elem.Name)
	if fuseKey != "" {
		srcsAttr = fmt.Sprintf(`["@src_%s//:tree"]`, fuseKey)
	}
	fmt.Fprintf(&b, `genrule(
    name = "%[1]s_install",
    srcs = %[3]s,
    outs = ["install_tree.tar"],
    cmd = %[2]s,
)
`, elem.Name, renderPipelineCmdAttr(dispatch, groups, fuseKey != ""), srcsAttr)
	return b.String()
}

// needsConfigSettings reports whether the dispatch shape requires
// emitting local config_setting rules. The single-dispatch-var
// "platform" case (target_arch only) doesn't — the cmd's select()
// arms can reference @platforms//cpu:<v> directly. Every other
// shape (option-typed, or any cross-product with 2+ dims)
// requires per-tuple config_settings.
func needsConfigSettings(dispatch []dispatchVar) bool {
	if len(dispatch) == 0 {
		return false
	}
	if len(dispatch) == 1 && dispatch[0].Kind == "platform" {
		return false
	}
	return true
}

// renderConfigSettings emits one `config_setting` per dispatch
// tuple. Each config_setting carries:
//   - constraint_values for "platform" dims (e.g. target_arch=x86_64
//     becomes constraint_values = ["@platforms//cpu:x86_64"]).
//   - flag_values for "option" dims (e.g. snap_grade=devel becomes
//     flag_values = {"//options:snap_grade": "devel"}).
//
// Names follow tupleConfigSettingName — a sorted-by-varname join
// of values with '_' so identical tuples produce identical names
// across runs.
func renderConfigSettings(dispatch []dispatchVar, groups []dispatchGroup) string {
	kinds := map[string]string{}
	for _, d := range dispatch {
		kinds[d.Name] = d.Kind
	}
	var b strings.Builder
	for _, g := range groups {
		for _, tuple := range g.Tuples {
			fmt.Fprintf(&b, "config_setting(\n")
			fmt.Fprintf(&b, "    name = %q,\n", tupleConfigSettingName(tuple))
			// Sort keys for deterministic rendering.
			keys := sortedKeys(tuple)
			var constraints, flagPairs []string
			for _, k := range keys {
				v := tuple[k]
				switch kinds[k] {
				case "platform":
					constraints = append(constraints, archConstraintLabel(v))
				case "option":
					flagPairs = append(flagPairs, fmt.Sprintf("%q: %q", "//options:"+k, v))
				}
			}
			if len(constraints) > 0 {
				fmt.Fprintf(&b, "    constraint_values = [\n")
				for _, c := range constraints {
					fmt.Fprintf(&b, "        %q,\n", c)
				}
				fmt.Fprintf(&b, "    ],\n")
			}
			if len(flagPairs) > 0 {
				fmt.Fprintf(&b, "    flag_values = {\n")
				for _, fp := range flagPairs {
					fmt.Fprintf(&b, "        %s,\n", fp)
				}
				fmt.Fprintf(&b, "    },\n")
			}
			fmt.Fprintf(&b, ")\n\n")
		}
	}
	return b.String()
}

// tupleConfigSettingName returns the local config_setting label
// name for a dispatch tuple. Sorts entries by varname and joins
// values with '_'; non-identifier characters in values become '_'.
func tupleConfigSettingName(tuple map[string]string) string {
	keys := sortedKeys(tuple)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, sanitizeIdent(tuple[k]))
	}
	return strings.Join(parts, "_")
}

// sanitizeIdent replaces non-identifier characters with '_' so a
// dispatch value like "1.2.3" or "my-option" produces a valid
// Bazel target name.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sortedKeys returns the keys of a map[string]string sorted
// alphabetically.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// renderPipelineCmdAttr emits the value of the genrule's cmd
// attribute. Empty dispatch + single group: a triple-quoted shell
// script string. Otherwise: select({...}) over per-tuple labels
// (either @platforms//cpu:<v> for the simple target_arch-only
// case, or local :<tuple-name> config_setting labels otherwise).
func renderPipelineCmdAttr(dispatch []dispatchVar, groups []dispatchGroup, fuseSources bool) string {
	if len(groups) == 1 && len(groups[0].Tuples) == 0 {
		return renderPipelineCmdBody(groups[0].Phases, fuseSources)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "select({\n")
	for _, g := range groups {
		body := renderPipelineCmdBody(g.Phases, fuseSources)
		for _, tuple := range g.Tuples {
			fmt.Fprintf(&b, "        %q: %s,\n", tupleSelectLabel(dispatch, tuple), body)
		}
	}
	fmt.Fprintf(&b, "    })")
	return b.String()
}

// tupleSelectLabel returns the Bazel select() key for a dispatch
// tuple. Single-platform-dim case (target_arch only) uses
// @platforms//cpu:<v> directly without a config_setting wrapper.
// Every other shape references the local :<tuple-name>
// config_setting renderConfigSettings emitted.
func tupleSelectLabel(dispatch []dispatchVar, tuple map[string]string) string {
	if len(dispatch) == 1 && dispatch[0].Kind == "platform" {
		return archConstraintLabel(tuple[dispatch[0].Name])
	}
	return ":" + tupleConfigSettingName(tuple)
}

// renderPipelineCmdBody emits the triple-quoted shell-script body
// the genrule's cmd attribute consumes (or one branch of the
// select() dict in the multi-arch case). fuseSources picks the
// strip-prefix the cmd uses to recover source-relative paths
// from $(SRCS) entries: "sources/" for the staged-on-disk shape
// (the default), "tree_dir/" for the FUSE-symlinked
// @src_<key>//:tree shape (matches the symlink target the
// rules/sources.bzl repo rule creates).
func renderPipelineCmdBody(p pipelinePhases, fuseSources bool) string {
	stripFrom := "sources/"
	if fuseSources {
		stripFrom = "tree_dir/"
	}
	return fmt.Sprintf(`"""
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
            rel="$${src##*%[3]s}"
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
%[1]s
        # Tar the install tree as the element's primary output.
        cd "$$EXEC_ROOT"
        tar -cf "$(location install_tree.tar)" -C "$$INSTALL_ROOT" .
    """`, renderPipelineCommands(p.Configure, p.Build, p.Install, p.Strip), renderEnvExports(p.Env), stripFrom)
}

// renderEnvExports emits one `export K=V` line per env entry,
// indented to match the surrounding cmd-body lines. The values are
// already variable-resolved (substituteCmd in resolveAt); we just
// shell-quote them. Empty env yields the empty string so the
// surrounding template doesn't get a stray blank line.
func renderEnvExports(env [][2]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("        # Project- + element-level environment, sourced from\n")
	b.WriteString("        # project.conf's `environment:` and the .bst's `environment:`\n")
	b.WriteString("        # blocks. Element-level entries override project-level on\n")
	b.WriteString("        # conflict; values are variable-resolved with runtime\n")
	b.WriteString("        # sentinels (%%{install-root} → $$INSTALL_ROOT etc.) mapped\n")
	b.WriteString("        # to their shell-var form so phase commands consume them\n")
	b.WriteString("        # consistently.\n")
	for _, kv := range env {
		fmt.Fprintf(&b, "        export %s=%s\n", kv[0], shellQuote(kv[1]))
	}
	return b.String()
}

// shellQuote wraps a value in double quotes, escaping any
// embedded $$ / " / \ so the resulting string is a valid
// double-quoted shell literal. Specifically: $$ stays as $$
// (Bazel's escape; the action runner sees $); a literal " becomes
// \"; a literal \ becomes \\.
func shellQuote(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
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
