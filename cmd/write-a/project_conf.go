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
)

// projectConf is the slice of the project.conf surface write-a
// currently consumes. Other keys (name, plugins, aliases, options,
// …) are ignored at unmarshal time so we don't have to track
// BuildStream's full schema.
type projectConf struct {
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
	return projectInfo{
		ProjectRoot:  root,
		ElementRoot:  elementRoot,
		Variables:    pc.Variables,
		Conditionals: pc.Conditionals,
	}, nil
}
