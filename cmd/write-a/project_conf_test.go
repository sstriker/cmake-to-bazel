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
	pc, err := loadProjectConf(conf)
	if err != nil {
		t.Fatalf("loadProjectConf: %v", err)
	}
	if got, want := pc.Variables["prefix"], "/usr"; got != want {
		t.Errorf("prefix: got %q, want %q", got, want)
	}
	if got, want := pc.Variables["custom"], "%{prefix}/custom"; got != want {
		t.Errorf("custom: got %q, want %q", got, want)
	}
	if got, want := pc.ElementPath, "elements"; got != want {
		t.Errorf("element-path: got %q, want %q", got, want)
	}
}

func TestResolveAliasURL(t *testing.T) {
	aliases := map[string]string{
		"github":     "https://github.com/",
		"sourceware": "https://sourceware.org/git/",
	}
	cases := map[string]string{
		// FDSDK-shape: <alias>:<path> resolves to alias-prefix +
		// path.
		"github:libexpat/libexpat.git": "https://github.com/libexpat/libexpat.git",
		"sourceware:bzip2.git":         "https://sourceware.org/git/bzip2.git",
		// Unknown alias prefix: leave URL unchanged (the URL is
		// either a bare http://, https://, or a not-yet-aliased
		// scheme; alias resolution shouldn't munge it).
		"https://example.org/foo.tar.gz": "https://example.org/foo.tar.gz",
		"http://nope/x.git":              "http://nope/x.git",
		// No colon: leave unchanged.
		"plain-string": "plain-string",
		// Empty input: empty output.
		"": "",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := resolveAliasURL(in, aliases); got != want {
				t.Errorf("resolveAliasURL(%q): got %q, want %q", in, got, want)
			}
		})
	}
	// Nil aliases map is safe — every URL passes through unchanged.
	if got := resolveAliasURL("github:foo.git", nil); got != "github:foo.git" {
		t.Errorf("nil aliases map should pass through; got %q", got)
	}
}

func TestLoadProjectConf_AliasesAndEnvironment(t *testing.T) {
	tmp := t.TempDir()
	conf := filepath.Join(tmp, "project.conf")
	body := `name: testproj
element-path: elements
aliases:
  github: https://github.com/
  sourceware: https://sourceware.org/git/
environment:
  LC_ALL: en_US.UTF-8
  SOURCE_DATE_EPOCH: '%{source-date-epoch}'
variables:
  prefix: /usr
`
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pc, err := loadProjectConf(conf)
	if err != nil {
		t.Fatalf("loadProjectConf: %v", err)
	}
	if got, want := pc.Aliases["github"], "https://github.com/"; got != want {
		t.Errorf("Aliases[github]: got %q, want %q", got, want)
	}
	if got, want := pc.Environment["LC_ALL"], "en_US.UTF-8"; got != want {
		t.Errorf("Environment[LC_ALL]: got %q, want %q", got, want)
	}
	if got, want := pc.Environment["SOURCE_DATE_EPOCH"], "%{source-date-epoch}"; got != want {
		t.Errorf("Environment values pre-substitution: got %q, want %q", got, want)
	}
}

func TestLoadProjectConf_NoVariablesBlockReturnsNil(t *testing.T) {
	tmp := t.TempDir()
	conf := filepath.Join(tmp, "project.conf")
	if err := os.WriteFile(conf, []byte("name: empty\nelement-path: elements\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pc, err := loadProjectConf(conf)
	if err != nil {
		t.Fatalf("loadProjectConf: %v", err)
	}
	if pc.Variables != nil {
		t.Errorf("expected nil for missing variables: block, got %v", pc.Variables)
	}
}
