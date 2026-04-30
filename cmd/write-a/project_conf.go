package main

// project.conf loader.
//
// BuildStream meta-projects keep their project-wide configuration in
// a YAML file at the project root. The full schema is rich (element-
// path, plugins, aliases, options, …); for v1 of write-a we consume
// exactly one key — `variables:` — which supplies the project-level
// override layer of the variable resolver (see variables.go).
//
// Discovery walks up from the .bst file's directory looking for the
// nearest `project.conf`, stopping at the filesystem root. That
// matches BuildStream's "first project.conf wins" semantics. If no
// project.conf is found, the element renders against BuildStream's
// stock variable defaults plus any per-kind / per-element overrides;
// the project.conf layer is empty.

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// projectConf is the slice of the project.conf surface write-a
// currently consumes. Other keys (name, element-path, plugins,
// aliases, options, …) are ignored at unmarshal time so we don't
// have to track BuildStream's full schema.
type projectConf struct {
	Variables map[string]string `yaml:"variables"`
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

// loadProjectConf parses the project.conf at path and returns its
// `variables:` block. An empty / missing variables: block returns
// nil (callers treat nil as "no overrides at the project layer").
func loadProjectConf(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pc projectConf
	if err := yaml.Unmarshal(body, &pc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return pc.Variables, nil
}

// loadProjectConfFromBst is the convenience entry point loadGraph
// uses: walk up from the .bst's directory to find project.conf, and
// load its variables: block. Returns (nil, nil) when no project.conf
// is found anywhere on the upward path.
func loadProjectConfFromBst(bstPath string) (map[string]string, error) {
	startDir := filepath.Dir(bstPath)
	path, err := findProjectConf(startDir)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return loadProjectConf(path)
}
