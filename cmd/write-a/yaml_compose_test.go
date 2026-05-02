package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompose_SingleInclude covers the basic case: a YAML file
// declaring `(@): - other.yml` deep-merges other.yml into the
// surrounding map.
func TestCompose_SingleInclude(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "other.yml"),
		[]byte("included-key: included-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(tmp, "main.yml")
	if err := os.WriteFile(main, []byte(`(@):
- other.yml
own-key: own-value
`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadAndComposeYAML(main, tmp, map[string]bool{})
	if err != nil {
		t.Fatalf("loadAndComposeYAML: %v", err)
	}
	var got map[string]string
	if err := doc.Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]string{
		"included-key": "included-value",
		"own-key":      "own-value",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
}

// TestCompose_ParentWinsOnConflict covers the precedence rule:
// when both the parent and the include declare the same key, the
// parent's value wins (BuildStream's "your local definitions
// override the included content").
func TestCompose_ParentWinsOnConflict(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "inc.yml"),
		[]byte("conflict: from-include\nonly-in-include: yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(tmp, "main.yml")
	if err := os.WriteFile(main,
		[]byte("(@):\n- inc.yml\nconflict: from-parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadAndComposeYAML(main, tmp, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := doc.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["conflict"] != "from-parent" {
		t.Errorf("conflict: got %q, want %q", got["conflict"], "from-parent")
	}
	if got["only-in-include"] != "yes" {
		t.Errorf("only-in-include: got %q, want yes", got["only-in-include"])
	}
}

// TestCompose_NestedMappingsRecurse covers deep-merging at depth:
// both parent and include have `variables: { ... }`; the maps
// merge recursively rather than the parent's variables wholly
// replacing the include's.
func TestCompose_NestedMappingsRecurse(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "inc.yml"), []byte(`variables:
  prefix: /usr
  bindir: /usr/bin
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(tmp, "main.yml")
	if err := os.WriteFile(main, []byte(`(@):
- inc.yml
variables:
  bindir: /custom/bin
`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadAndComposeYAML(main, tmp, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Variables map[string]string `yaml:"variables"`
	}
	if err := doc.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Variables["prefix"] != "/usr" {
		t.Errorf("prefix from include should survive deep-merge; got %q", got.Variables["prefix"])
	}
	if got.Variables["bindir"] != "/custom/bin" {
		t.Errorf("bindir from parent should override include; got %q", got.Variables["bindir"])
	}
}

// TestCompose_ProjectRootRelativePaths covers BuildStream's
// project-root-relative include resolution: an include nested
// inside a subdirectory still resolves its (@): paths against the
// project root, not its own directory.
func TestCompose_ProjectRootRelativePaths(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Top-level include: include/runtime.yml itself includes
	// include/flags.yml (sibling). Real FDSDK does this; the
	// path "include/flags.yml" resolves against the project root,
	// NOT against include/'s directory.
	if err := os.WriteFile(filepath.Join(tmp, "include/runtime.yml"),
		[]byte("(@):\n- include/flags.yml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "include/flags.yml"),
		[]byte("nested-key: nested-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(tmp, "main.yml")
	if err := os.WriteFile(main,
		[]byte("(@):\n- include/runtime.yml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadAndComposeYAML(main, tmp, map[string]bool{})
	if err != nil {
		t.Fatalf("loadAndComposeYAML: %v", err)
	}
	var got map[string]string
	if err := doc.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["nested-key"] != "nested-value" {
		t.Errorf("nested include path didn't resolve project-root-relative; got %v", got)
	}
}

// TestCompose_ConditionalDirectivePreserved covers the composer's
// hand-off contract for (?): blocks: composer leaves them in the
// tree (the variables-level extractor in conditional.go pulls them
// out before the struct-decode step). The (>): / (<): / (=):
// list-merge directives are still stripped — those aren't yet
// observed in curated probes and stripping them keeps decode
// robust.
func TestCompose_ConditionalDirectivePreserved(t *testing.T) {
	tmp := t.TempDir()
	main := filepath.Join(tmp, "main.yml")
	if err := os.WriteFile(main, []byte(`variables:
  prefix: /usr
  (?):
  - target_arch == "x86_64":
      arch_var: x86_64
  - target_arch == "aarch64":
      arch_var: aarch64
`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadAndComposeYAML(main, tmp, map[string]bool{})
	if err != nil {
		t.Fatalf("loadAndComposeYAML: %v", err)
	}
	// Composer-level: (?): survives — extractor in conditional.go
	// is the v1 consumer.
	branches, err := extractConditionalsFromVariables(doc)
	if err != nil {
		t.Fatalf("extractConditionalsFromVariables: %v", err)
	}
	if len(branches) != 2 {
		t.Errorf("expected 2 branches, got %d", len(branches))
	}
}

// TestCompose_CycleDetected covers the include-cycle case: A
// includes B which includes A. The composer detects the loop via
// its visited set and surfaces a clear error.
func TestCompose_CycleDetected(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "a.yml"),
		[]byte("(@):\n- b.yml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "b.yml"),
		[]byte("(@):\n- a.yml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadAndComposeYAML(filepath.Join(tmp, "a.yml"), tmp, map[string]bool{})
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle; got: %v", err)
	}
}

// TestCompose_MissingIncludeReportsPath covers the typo case: the
// (@): directive references a file that doesn't exist; the error
// names the missing path.
func TestCompose_MissingIncludeReportsPath(t *testing.T) {
	tmp := t.TempDir()
	main := filepath.Join(tmp, "main.yml")
	if err := os.WriteFile(main,
		[]byte("(@):\n- nonexistent.yml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadAndComposeYAML(main, tmp, map[string]bool{})
	if err == nil {
		t.Fatal("expected error for missing include, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent.yml") {
		t.Errorf("error should name the missing file; got: %v", err)
	}
}

// TestCompose_ScalarIncludeAccepted covers the single-string form
// (@) takes (BuildStream accepts both `(@): "file.yml"` and
// `(@): - file.yml`).
func TestCompose_ScalarIncludeAccepted(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "inc.yml"),
		[]byte("k: v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(tmp, "main.yml")
	if err := os.WriteFile(main, []byte(`(@): inc.yml`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := loadAndComposeYAML(main, tmp, map[string]bool{})
	if err != nil {
		t.Fatalf("loadAndComposeYAML: %v", err)
	}
	var got map[string]string
	if err := doc.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["k"] != "v" {
		t.Errorf("scalar (@): didn't merge include; got %v", got)
	}
}
