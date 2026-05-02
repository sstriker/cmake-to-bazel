package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjectConf_WalksUp(t *testing.T) {
	tmp := t.TempDir()
	// Put project.conf two directories above the .bst.
	if err := os.WriteFile(filepath.Join(tmp, "project.conf"),
		[]byte("variables:\n  prefix: /opt/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deepDir := filepath.Join(tmp, "elements", "lib", "x")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findProjectConf(deepDir)
	if err != nil {
		t.Fatalf("findProjectConf: %v", err)
	}
	want := filepath.Join(tmp, "project.conf")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindProjectConf_NotFoundReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	got, err := findProjectConf(tmp)
	if err != nil {
		t.Fatalf("findProjectConf: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string when no project.conf exists, got %q", got)
	}
}

func TestFindProjectConf_NearestWins(t *testing.T) {
	// Two project.conf files on the path-up; the nearer one wins.
	tmp := t.TempDir()
	outerConf := filepath.Join(tmp, "project.conf")
	if err := os.WriteFile(outerConf,
		[]byte("variables:\n  prefix: /outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	innerDir := filepath.Join(tmp, "junction")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	innerConf := filepath.Join(innerDir, "project.conf")
	if err := os.WriteFile(innerConf,
		[]byte("variables:\n  prefix: /inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deepDir := filepath.Join(innerDir, "elements")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findProjectConf(deepDir)
	if err != nil {
		t.Fatalf("findProjectConf: %v", err)
	}
	if got != innerConf {
		t.Errorf("nearer project.conf should win; got %q, want %q", got, innerConf)
	}
}

func TestLoadProjectConf_ParsesVariables(t *testing.T) {
	tmp := t.TempDir()
	conf := filepath.Join(tmp, "project.conf")
	body := `name: testproj
element-path: elements
variables:
  prefix: /usr
  custom: "%{prefix}/custom"
plugins:
  - origin: core
    elements:
      - cmake
`
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	vars, err := loadProjectConf(conf)
	if err != nil {
		t.Fatalf("loadProjectConf: %v", err)
	}
	if got, want := vars["prefix"], "/usr"; got != want {
		t.Errorf("prefix: got %q, want %q", got, want)
	}
	if got, want := vars["custom"], "%{prefix}/custom"; got != want {
		t.Errorf("custom: got %q, want %q", got, want)
	}
}

func TestLoadProjectConf_NoVariablesBlockReturnsNil(t *testing.T) {
	tmp := t.TempDir()
	conf := filepath.Join(tmp, "project.conf")
	if err := os.WriteFile(conf, []byte("name: empty\nelement-path: elements\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vars, err := loadProjectConf(conf)
	if err != nil {
		t.Fatalf("loadProjectConf: %v", err)
	}
	if vars != nil {
		t.Errorf("expected nil for missing variables: block, got %v", vars)
	}
}

