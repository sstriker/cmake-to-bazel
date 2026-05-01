package main

// project.conf loader.
//
// BuildStream meta-projects keep their project-wide configuration in
// a YAML file at the project root. The full schema is rich (plugins,
// aliases, options, …); for v1 of write-a we consume two keys:
//
//   - `variables:` — the project-level override layer of the variable
//     resolver (see variables.go).
//   - `element-path:` — the path (relative to the project.conf dir)
//     under which `.bst` files live. Drives path-qualified element
//     resolution: a `.bst` at <project>/<element-path>/foo/bar.bst
//     keys into the graph as "foo/bar" so a depends-list reference
//     `foo/bar.bst` resolves to the same element regardless of which
//     subdirectory the dep declaration itself lives in.
//
// Discovery walks up from the .bst file's directory looking for the
// nearest `project.conf`, stopping at the filesystem root. That
// matches BuildStream's "first project.conf wins" semantics. If no
// project.conf is found, the element renders against BuildStream's
// stock variable defaults plus any per-kind / per-element overrides
// (the project.conf layer is empty), and the graph keys elements by
// basename instead of project-relative path.

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// projectConf is the slice of the project.conf surface write-a
// currently consumes. Other keys (name, plugins, options,
// fatal-warnings, …) are ignored at unmarshal time so we don't
// have to track BuildStream's full schema.
type projectConf struct {
	// Name is the BuildStream project name (e.g. "freedesktop-sdk")
	// from project.conf's top-level `name:` field. BuildStream
	// exposes this as the `%{project-name}` built-in variable;
	// FDSDK's bootstrap/base-sdk/perl uses it in flags.yml's
	// arch-conditional overrides.
	Name        string            `yaml:"name"`
	Variables   map[string]string `yaml:"variables"`
	ElementPath string            `yaml:"element-path"`
	// Conditionals are the per-arch (?): branches extracted from
	// `variables:` before the YAML decode pass. Same shape as
	// bstFile.Conditionals; project-level conditionals layer below
	// element-level ones. Most FDSDK arch-specific defaults arrive
	// here (project.conf composes include/_private/arch.yml, which
	// declares (?): branches setting %{snap_arch} / %{go-arch}
	// / etc.).
	Conditionals []conditionalBranch `yaml:"-"`
	// Aliases maps URL-prefix aliases to their full URL. FDSDK's
	// project.conf composes include/_private/aliases.yml, which
	// declares 50+ entries like `github: https://github.com/`,
	// `sourceware: https://sourceware.org/git/`, etc. kind:git_repo
	// / kind:tar / kind:remote_asset URLs use the `<alias>:<path>`
	// syntax and the alias-resolver translates that to a full URL.
	// Consumed by the source-fetcher (deferred) — parsed and
	// recorded here so the data is ready when the fetcher lands.
	Aliases map[string]string `yaml:"aliases"`
	// Environment is the project-level environment-variable map
	// applied to every element's build / install / strip actions.
	// Element-level `environment:` blocks override per key.
	// Composes via (@): includes; FDSDK declares ~10 keys here
	// (LC_ALL, SOURCE_DATE_EPOCH, OMP_NUM_THREADS, ...). Pipeline
	// handlers emit `env = {...}` on the genrule attribute,
	// variable-resolved.
	Environment map[string]string `yaml:"environment"`
	// Options declares the user-configurable build settings the
	// project exposes. Each entry produces a Bazel `string_flag`
	// in project A; per-(option, value) `config_setting`s under
	// the same package back the eventual `select()` lowering of
	// (?): branches keyed on these variables. Per
	// docs/sources-design.md, target_arch keeps the
	// @platforms//cpu:* pathway; everything else option-typed
	// gets flag+select.
	Options map[string]bstOption `yaml:"options"`
	// Elements is BuildStream's per-kind project-conf override
	// block: `elements: <kind>: { variables: {...}, config: {...} }`
	// applies to every element of that kind in the project.
	// FDSDK uses this heavily — `elements: autotools:` pulls in
	// _private/autotools-conf.yml which defines `build-dir`,
	// `conf-cmd`, `conf-host`, `conf-build`, etc. Variables
	// declared here override the kind's plugin defaults; the
	// element's own variables: block still wins on a key conflict.
	Elements map[string]elementKindConf `yaml:"elements"`
}

// elementKindConf is the per-kind subblock of project.conf's
// elements: section. Mirrors BuildStream's element-level
// configuration shape; we currently consume Variables only.
// Conditionals / config: blocks at this layer land when fixtures
// surface them.
type elementKindConf struct {
	Variables map[string]string `yaml:"variables"`
}

// bstOption is one declaration in the project.conf options:
// block. Mirrors BuildStream's project options shape (see
// https://docs.buildstream.build/master/format_project.html#format-options).
//
// type: arch / enum / bool / flags / element / element-mask /
// element-list — write-a renders each as a Bazel string_flag.
// For `bool` and `flags` types we shape the values list to match
// (true/false for bool; the declared values for flags). The
// `default` field is parsed as an opaque yaml.Node since
// flags-typed defaults are lists while every other type is a
// scalar — the renderer converts to the canonical string form
// for string_flag.
type bstOption struct {
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
	// Variable is the BuildStream variable name the option's
	// chosen value sets. Defaults to the option's own name when
	// unset (the name : variable identity is BuildStream's
	// convention).
	Variable string    `yaml:"variable"`
	Values   []string  `yaml:"values"`
	Default  yaml.Node `yaml:"default"`
}

// projectInfo is the resolved view of the project.conf write-a
// uses to key the element graph and layer the variable scope.
// Empty / nil ProjectRoot means no project.conf was found —
// callers fall back to basename keying and an empty
// project-conf-vars layer.
type projectInfo struct {
	// ProjectRoot is the absolute directory that contains
	// project.conf. Empty when no project.conf was found.
	ProjectRoot string

	// ElementRoot is the absolute directory under which .bst files
	// live: ProjectRoot + ElementPath. Equal to ProjectRoot when
	// ElementPath is empty / "." (BuildStream default). Empty when
	// no project.conf was found.
	ElementRoot string

	// Variables is the project.conf variables: layer, fed to
	// resolveVars as the project-conf override layer.
	Variables map[string]string
	// Conditionals are the project-level (?): branches (e.g.
	// FDSDK's project.conf includes arch.yml which declares
	// per-arch variable overrides). Empty slice when no
	// project.conf was found.
	Conditionals []conditionalBranch
	// Aliases is the project-level URL-alias map (see projectConf.Aliases).
	Aliases map[string]string
	// Environment is the project-level env-var map (see projectConf.Environment).
	Environment map[string]string
	// Options is the project-level options-declaration map
	// (see projectConf.Options).
	Options map[string]bstOption
	// KindVars is the per-kind variable-override map
	// (`elements: <kind>: variables:` in project.conf). Each
	// entry overrides the kind's plugin defaults for every
	// element of that kind in the project. Element-level
	// variables: still wins on a key conflict (more specific).
	// FDSDK populates this via include/_private/autotools-conf.yml,
	// cmake-conf.yml, meson-conf.yml — defining `build-dir`,
	// `conf-cmd`, etc. that the .bst's commands reference.
	KindVars map[string]map[string]string
}

// findProjectConf walks up from startDir looking for a project.conf
// file. Returns the path when found, or the empty string when no
// project.conf exists between startDir and the filesystem root.
// Symlinks aren't followed at this layer (filepath.Walk-like
// surprises don't apply — we only ever check single paths).
func findProjectConf(startDir string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	dir := abs
	for {
		path := filepath.Join(dir, "project.conf")
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// loadProjectConf parses the project.conf at path and returns the
// resolved projectConf struct (variables: + element-path:). Runs
// the YAML composer first so (@): include directives resolve into
// project.conf's tree before the struct-decode step (FDSDK's
// project.conf composes variables from include/_private/arch.yml
// + include/repo_branches.yml + ...).
//
// Project-conf-relative includes resolve from the directory
// containing project.conf — that's the project root.
func loadProjectConf(path string) (*projectConf, error) {
	root := filepath.Dir(path)
	doc, err := loadAndComposeYAML(path, root, map[string]bool{})
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	conditionals, err := extractConditionalsFromVariables(doc)
	if err != nil {
		return nil, fmt.Errorf("extract conditionals from %s: %w", path, err)
	}
	var pc projectConf
	if err := doc.Decode(&pc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	pc.Conditionals = conditionals
	return &pc, nil
}

// loadProjectInfoFromBst is the entry point loadGraph uses: walk up
// from the .bst's directory to find project.conf, parse its
// variables: + element-path: keys, and resolve element-path against
// the project root. Returns a zero projectInfo (empty ProjectRoot)
// when no project.conf is found — callers treat that as the
// "no-project, basename-keyed" mode.
func loadProjectInfoFromBst(bstPath string) (projectInfo, error) {
	startDir := filepath.Dir(bstPath)
	path, err := findProjectConf(startDir)
	if err != nil {
		return projectInfo{}, err
	}
	if path == "" {
		return projectInfo{}, nil
	}
	pc, err := loadProjectConf(path)
	if err != nil {
		return projectInfo{}, err
	}
	root := filepath.Dir(path)
	elementPath := pc.ElementPath
	if elementPath == "" || elementPath == "." {
		// BuildStream default: element-path defaults to the
		// project root itself when unset.
		elementPath = "."
	}
	elementRoot := filepath.Join(root, elementPath)
	// Inject `%{project-name}` (the BuildStream built-in) as a
	// project-conf-level variable. Lower precedence than user
	// declarations in project.conf's variables: block, so a
	// user-set `project-name` would override (rare; this is
	// mostly there for elements that reference %{project-name}
	// in their command shapes — FDSDK does this in flags.yml).
	vars := pc.Variables
	if pc.Name != "" {
		merged := map[string]string{"project-name": pc.Name}
		for k, v := range pc.Variables {
			merged[k] = v
		}
		vars = merged
	}
	// Project-conf per-kind variable overrides. Flatten
	// pc.Elements to a kind→variables map so handlers can layer
	// the overrides on top of plugin defaults at render time.
	var kindVars map[string]map[string]string
	if len(pc.Elements) > 0 {
		kindVars = make(map[string]map[string]string, len(pc.Elements))
		for kind, conf := range pc.Elements {
			if len(conf.Variables) == 0 {
				continue
			}
			kv := make(map[string]string, len(conf.Variables))
			for k, v := range conf.Variables {
				kv[k] = v
			}
			kindVars[kind] = kv
		}
	}
	return projectInfo{
		ProjectRoot:  root,
		ElementRoot:  elementRoot,
		Variables:    vars,
		Conditionals: pc.Conditionals,
		Aliases:      pc.Aliases,
		Environment:  pc.Environment,
		Options:      pc.Options,
		KindVars:     kindVars,
	}, nil
}

// resolveAliasURL translates a BuildStream alias-prefixed URL
// (`<alias>:<path>`) to a full URL using the aliases map. The
// prefix matches the substring before the first colon; the
// remainder gets appended verbatim to the alias's URL. URLs
// without a registered alias prefix are returned unchanged
// (preserves http://, https://, file:// shapes etc.).
//
// Used by the source-fetcher (deferred) when materializing
// kind:git_repo / kind:tar / kind:remote sources whose URL
// declarations follow FDSDK's `github:org/repo.git` shorthand.
func resolveAliasURL(url string, aliases map[string]string) string {
	if aliases == nil {
		return url
	}
	colon := indexByte(url, ':')
	if colon <= 0 {
		return url
	}
	alias := url[:colon]
	prefix, ok := aliases[alias]
	if !ok {
		return url
	}
	return prefix + url[colon+1:]
}

// indexByte is a tiny inline helper to keep the alias resolver
// dependency-free at this layer (avoids pulling strings into a
// file that otherwise doesn't need it).
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
