package main

import (
	"strings"
	"testing"
)

func TestResolveVars_ProjectDefaults(t *testing.T) {
	// With no project.conf or per-element overrides, every variable
	// resolves to its BuildStream-stock value (prefix=/usr/local,
	// bindir derived through %{exec_prefix}).
	v, err := resolveVars(nil, nil, nil)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	cases := map[string]string{
		"prefix":     "/usr/local",
		"bindir":     "/usr/local/bin",
		"libdir":     "/usr/local/lib",
		"datadir":    "/usr/local/share",
		"sysconfdir": "/etc",
	}
	for name, want := range cases {
		if got := v[name]; got != want {
			t.Errorf("var %q: got %q, want %q", name, got, want)
		}
	}
}

func TestResolveVars_ProjectConfOverride(t *testing.T) {
	// FDSDK-shape: project.conf sets prefix=/usr, and every derived
	// path (bindir, datadir, libdir, ...) follows the override
	// because they reference %{prefix} / %{exec_prefix} via the
	// BuildStream-stock derivation chain.
	v, err := resolveVars(
		map[string]string{"prefix": "/usr"},
		nil, nil,
	)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	for name, want := range map[string]string{
		"prefix":  "/usr",
		"bindir":  "/usr/bin",
		"libdir":  "/usr/lib",
		"datadir": "/usr/share",
		"mandir":  "/usr/share/man",
	} {
		if got := v[name]; got != want {
			t.Errorf("var %q with project.conf prefix=/usr: got %q, want %q", name, got, want)
		}
	}
}

func TestResolveVars_LayerPrecedence(t *testing.T) {
	// All four layers contribute, highest wins: BuildStream stock
	// < project.conf < kind < element.
	v, err := resolveVars(
		map[string]string{"prefix": "/usr", "make-args": "-j2"},
		map[string]string{"make-args": "-j4", "make-install-args": "install"},
		map[string]string{"make-args": "-j8"},
	)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	// prefix: only project.conf provides; element doesn't touch.
	if got, want := v["prefix"], "/usr"; got != want {
		t.Errorf("prefix: got %q, want %q", got, want)
	}
	// make-args: element wins over kind wins over project.conf.
	if got, want := v["make-args"], "-j8"; got != want {
		t.Errorf("make-args: got %q, want %q", got, want)
	}
	// make-install-args: only kind provides.
	if got, want := v["make-install-args"], "install"; got != want {
		t.Errorf("make-install-args: got %q, want %q", got, want)
	}
}

func TestResolveVars_ElementOverridesPrefix(t *testing.T) {
	v, err := resolveVars(nil, nil, map[string]string{
		"prefix": "/opt/freedesktop-sdk",
	})
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	// Derived defaults follow the override.
	for name, want := range map[string]string{
		"prefix": "/opt/freedesktop-sdk",
		"bindir": "/opt/freedesktop-sdk/bin",
		"libdir": "/opt/freedesktop-sdk/lib",
	} {
		if got := v[name]; got != want {
			t.Errorf("var %q: got %q, want %q", name, got, want)
		}
	}
}

func TestResolveVars_KindDefaultsLayer(t *testing.T) {
	// kind:make-shape: kind defines its own variables; element doesn't override.
	v, err := resolveVars(
		nil,
		map[string]string{
			"make-args":         "",
			"make-install-args": `DESTDIR="%{install-root}" install`,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	if got, want := v["make-args"], ""; got != want {
		t.Errorf("make-args: got %q, want %q", got, want)
	}
	// install-root is a runtime sentinel — stays as %{install-root}.
	if got, want := v["make-install-args"], `DESTDIR="%{install-root}" install`; got != want {
		t.Errorf("make-install-args: got %q, want %q", got, want)
	}
}

func TestResolveVars_ElementOverridesKindDefault(t *testing.T) {
	v, err := resolveVars(
		nil,
		map[string]string{"make-args": "-j4"},
		map[string]string{"make-args": "-j8"},
	)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	if got, want := v["make-args"], "-j8"; got != want {
		t.Errorf("make-args (element override): got %q, want %q", got, want)
	}
}

func TestResolveVars_RecursiveExpansion(t *testing.T) {
	// %{a} -> %{b}, %{b} -> %{c}, %{c} -> "deep"
	v, err := resolveVars(nil, nil, map[string]string{
		"a": "%{b}/x",
		"b": "%{c}/y",
		"c": "deep",
	})
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	if got, want := v["a"], "deep/y/x"; got != want {
		t.Errorf("recursive a: got %q, want %q", got, want)
	}
}

func TestResolveVars_CycleDetected(t *testing.T) {
	_, err := resolveVars(nil, nil, map[string]string{
		"a": "%{b}",
		"b": "%{a}",
	})
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected error to mention cycle, got: %v", err)
	}
}

func TestResolveVars_UndefinedReferenceErrors(t *testing.T) {
	_, err := resolveVars(nil, nil, map[string]string{
		"a": "%{nonexistent}",
	})
	if err == nil {
		t.Fatal("expected undefined-reference error, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("expected error to mention missing var, got: %v", err)
	}
}

func TestResolveVars_RuntimeSentinelsPassThrough(t *testing.T) {
	v, err := resolveVars(nil, nil, nil)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	for _, name := range []string{"install-root", "build-root"} {
		if got, want := v[name], "%{"+name+"}"; got != want {
			t.Errorf("sentinel %q should pass through; got %q, want %q", name, got, want)
		}
	}
}

func TestSubstituteCmd_ExpandsAndMapsSentinels(t *testing.T) {
	v, err := resolveVars(
		nil,
		map[string]string{
			"make-args":         "",
			"make-install-args": `DESTDIR="%{install-root}" install`,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	got, err := substituteCmd(`make -j1 %{make-install-args}`, v)
	if err != nil {
		t.Fatalf("substituteCmd: %v", err)
	}
	want := `make -j1 DESTDIR="$$INSTALL_ROOT" install`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstituteCmd_PrefixDerivedPath(t *testing.T) {
	v, err := resolveVars(nil, nil, map[string]string{"prefix": "/opt/foo"})
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	got, err := substituteCmd(`install -D greet %{install-root}%{bindir}/greet`, v)
	if err != nil {
		t.Fatalf("substituteCmd: %v", err)
	}
	want := `install -D greet $$INSTALL_ROOT/opt/foo/bin/greet`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstituteCmd_UnknownVarErrors(t *testing.T) {
	v, err := resolveVars(nil, nil, nil)
	if err != nil {
		t.Fatalf("resolveVars: %v", err)
	}
	_, err = substituteCmd(`echo %{not-a-real-var}`, v)
	if err == nil {
		t.Fatal("expected error for unknown var, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-real-var") {
		t.Errorf("expected error to mention missing var, got: %v", err)
	}
}
