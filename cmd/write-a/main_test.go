// Smoke tests for the cmd/write-a binary. These don't run Bazel —
// they verify the rendered project-A and project-B trees have the
// expected structure and key content. End-to-end Bazel-build
// validation through both projects lives in:
//
//   - make e2e-meta-hello (single-element kind:cmake fixture, Phase 1)
//   - make e2e-meta-stack (multi-element kind:cmake + kind:stack fixture, Phase 2)
//
// both gated on Bazel availability.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleCmakeBst = `kind: cmake

sources:
- kind: local
  path: src
`

// fakeConvertBin makes a marker file the writer can stat() + copy. The
// writer never executes it inside these tests; rendering doesn't run
// any actions.
func fakeConvertBin(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "convert-element")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// makeCmakeBst stages a tiny kind:local cmake source tree at
// dir/<name>/src/ and writes <name>.bst pointing at it. Returns the
// .bst path.
func makeCmakeBst(t *testing.T, dir, name string) string {
	t.Helper()
	srcDir := filepath.Join(dir, name+"-src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "CMakeLists.txt"),
		[]byte("cmake_minimum_required(VERSION 3.20)\nproject("+name+")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(dir, name+".bst")
	body := "kind: cmake\nsources:\n- kind: local\n  path: " + srcDir + "\n"
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return bst
}

func TestWriter_HelloWorldShape(t *testing.T) {
	tmp := t.TempDir()

	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "CMakeLists.txt"),
		[]byte("cmake_minimum_required(VERSION 3.20)\nproject(t)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bstPath := filepath.Join(tmp, "hello.bst")
	if err := os.WriteFile(bstPath, []byte(sampleCmakeBst), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := fakeConvertBin(t, tmp)

	g, err := loadGraph([]string{bstPath})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if len(g.Elements) != 1 || g.Elements[0].Name != "hello" {
		t.Fatalf("Elements = %+v, want [hello]", g.Elements)
	}
	if g.Elements[0].Bst.Kind != "cmake" {
		t.Errorf("Kind = %q, want cmake", g.Elements[0].Bst.Kind)
	}

	outA := filepath.Join(tmp, "project-A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	for _, want := range []string{
		"MODULE.bazel",
		"BUILD.bazel",
		"rules/zero_files.bzl",
		"rules/BUILD.bazel",
		"tools/convert-element",
		"tools/BUILD.bazel",
		"elements/hello/BUILD.bazel",
		"elements/hello/sources/CMakeLists.txt",
	} {
		if _, err := os.Stat(filepath.Join(outA, want)); err != nil {
			t.Errorf("missing rendered file %q in project A: %v", want, err)
		}
	}

	outB := filepath.Join(tmp, "project-B")
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	for _, want := range []string{
		"MODULE.bazel",
		"BUILD.bazel",
		"elements/hello/BUILD.bazel",
		"elements/hello/CMakeLists.txt",
	} {
		if _, err := os.Stat(filepath.Join(outB, want)); err != nil {
			t.Errorf("missing rendered file %q in project B: %v", want, err)
		}
	}
	bModule, err := os.ReadFile(filepath.Join(outB, "MODULE.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bModule), `bazel_dep(name = "rules_cc"`) {
		t.Errorf("project B MODULE.bazel missing rules_cc bazel_dep:\n%s", bModule)
	}
	bPlaceholder, err := os.ReadFile(filepath.Join(outB, "elements/hello/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bPlaceholder), "BUILD_NOT_YET_STAGED") {
		t.Errorf("project B element BUILD missing placeholder marker:\n%s", bPlaceholder)
	}

	// The element's BUILD references the staged convert-element via
	// tools = [//tools:convert-element], merges sources via the
	// shadow-build cmd, and produces the three expected outputs.
	body, err := os.ReadFile(filepath.Join(outA, "elements/hello/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, marker := range []string{
		`tools = ["//tools:convert-element"]`,
		`for src in $(SRCS)`,
		`rel="$${src##*sources/}"`,
		`"BUILD.bazel.out"`,
		`"read_paths.json"`,
		`"cmake-config-bundle.tar"`,
		`$(location //tools:convert-element)`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("rendered BUILD missing marker %q\n--body--\n%s", marker, got)
		}
	}
}

// TestWriter_AcceptsNonLocalSourceMetadata covers the source-kind
// dispatch story: non-kind:local sources (kind:tar, kind:git_repo,
// etc.) parse cleanly, their URL/Ref/Track metadata is recorded on
// the resolvedSource entry, and staging skips them gracefully.
// Real source-fetch integration with orchestrator/sourcecheckout
// is deferred — render-time succeeds against any source kind, but
// bazel-build of the resulting BUILD would fail without real bytes.
func TestWriter_AcceptsNonLocalSourceMetadata(t *testing.T) {
	tmp := t.TempDir()
	bstPath := filepath.Join(tmp, "x.bst")
	body := `kind: cmake
sources:
- kind: tar
  url: https://example.org/foo.tar.gz
  ref: a1b2c3
`
	if err := os.WriteFile(bstPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bstPath})
	if err != nil {
		t.Fatalf("loadGraph: %v (non-local sources should parse)", err)
	}
	if len(g.Elements[0].Sources) != 1 {
		t.Fatalf("Sources len = %d, want 1", len(g.Elements[0].Sources))
	}
	src := g.Elements[0].Sources[0]
	if src.Kind != "tar" {
		t.Errorf("Sources[0].Kind: got %q, want %q", src.Kind, "tar")
	}
	if src.URL != "https://example.org/foo.tar.gz" {
		t.Errorf("Sources[0].URL: got %q, want %q", src.URL, "https://example.org/foo.tar.gz")
	}
	if src.Ref != "a1b2c3" {
		t.Errorf("Sources[0].Ref: got %q, want %q", src.Ref, "a1b2c3")
	}
	if src.AbsPath != "" {
		t.Errorf("Sources[0].AbsPath should be empty for non-kind:local; got %q", src.AbsPath)
	}
}

func TestWriter_RejectsDuplicateElementName(t *testing.T) {
	tmp := t.TempDir()
	dir1 := filepath.Join(tmp, "d1")
	dir2 := filepath.Join(tmp, "d2")
	bst1 := makeCmakeBst(t, dir1, "shared")
	bst2 := makeCmakeBst(t, dir2, "shared")
	if _, err := loadGraph([]string{bst1, bst2}); err == nil {
		t.Errorf("expected error for duplicate element name, got nil")
	}
}

func TestWriter_GraphTopoSorted(t *testing.T) {
	// Build three cmake elements where leaf <- mid <- root; load them
	// in reverse order and check the graph comes out in dep order.
	tmp := t.TempDir()
	leafBst := makeCmakeBst(t, tmp, "leaf")
	midBst := makeCmakeBst(t, tmp, "mid")
	rootBst := makeCmakeBst(t, tmp, "root")
	// Inject depends: edges by appending to the .bst files.
	if err := appendDepends(midBst, []string{"leaf"}); err != nil {
		t.Fatal(err)
	}
	if err := appendDepends(rootBst, []string{"mid"}); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{rootBst, midBst, leafBst}) // reverse order
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	got := []string{}
	for _, e := range g.Elements {
		got = append(got, e.Name)
	}
	want := []string{"leaf", "mid", "root"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("topo order = %v, want %v", got, want)
	}
}

func TestWriter_RejectsCycle(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	b := makeCmakeBst(t, tmp, "b")
	// a depends on b, b depends on a → cycle.
	if err := appendDepends(a, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if err := appendDepends(b, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGraph([]string{a, b}); err == nil {
		t.Errorf("expected cycle error, got nil")
	}
}

func TestWriter_RejectsMissingDep(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	if err := appendDepends(a, []string{"nonexistent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGraph([]string{a}); err == nil {
		t.Errorf("expected unresolved-dep error, got nil")
	}
}

func TestWriter_StackElementShape(t *testing.T) {
	tmp := t.TempDir()
	libA := makeCmakeBst(t, tmp, "lib-a")
	libB := makeCmakeBst(t, tmp, "lib-b")
	stack := filepath.Join(tmp, "runtime.bst")
	if err := os.WriteFile(stack,
		[]byte("kind: stack\ndepends:\n- lib-a\n- lib-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := loadGraph([]string{libA, libB, stack})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	// Topo order: lib-a, lib-b, runtime.
	got := []string{}
	for _, e := range g.Elements {
		got = append(got, e.Name)
	}
	if strings.Join(got, ",") != "lib-a,lib-b,runtime" {
		t.Errorf("topo order = %v, want [lib-a,lib-b,runtime]", got)
	}

	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}

	// Project A: cmake elements get the genrule shape; stack gets a
	// no-op marker BUILD (no targets).
	for _, name := range []string{"lib-a", "lib-b", "runtime"} {
		if _, err := os.Stat(filepath.Join(outA, "elements", name, "BUILD.bazel")); err != nil {
			t.Errorf("project A: missing BUILD for %q: %v", name, err)
		}
	}
	stackBuild, err := os.ReadFile(filepath.Join(outA, "elements/runtime/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	// Stack's project-A package declares no actionable targets — only
	// comments. Anchor the check with `(` so the prose doesn't false-
	// positive ("filegroup that …" comment is fine; "filegroup(" call
	// is not).
	for _, banned := range []string{"genrule(", "filegroup(", "cc_library("} {
		if strings.Contains(string(stackBuild), banned) {
			t.Errorf("project A stack BUILD should declare no targets, got %q in:\n%s", banned, stackBuild)
		}
	}

	// Project B: the stack's filegroup references each dep's primary target.
	outB := filepath.Join(tmp, "B")
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	stackBBuild, err := os.ReadFile(filepath.Join(outB, "elements/runtime/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`name = "runtime"`,
		`"//elements/lib-a:lib-a"`,
		`"//elements/lib-b:lib-b"`,
	} {
		if !strings.Contains(string(stackBBuild), marker) {
			t.Errorf("project B runtime BUILD missing %q\n--body--\n%s", marker, stackBBuild)
		}
	}
}

// TestWriter_AutotoolsElementShape covers kind:autotools: the
// pipelineHandler defaults expand BuildStream's canonical %{autogen}
// / %{configure} / %{make} / %{make-install} chain. Without an
// element-level override the rendered cmd carries the canonical
// autoconf flag set substituted from the project-default (or
// project.conf-overridden) %{prefix} chain.
func TestWriter_AutotoolsElementShape(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "configure"),
		[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Makefile.in"),
		[]byte("all:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "auto.bst")
	bstBody := "kind: autotools\nsources:\n- kind: local\n  path: " + srcDir + "\n"
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if g.Elements[0].Bst.Kind != "autotools" {
		t.Fatalf("Kind = %q, want autotools", g.Elements[0].Bst.Kind)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outA, "elements/auto/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, marker := range []string{
		// Pipeline shape inherited from pipelineHandler.
		`name = "auto_install"`,
		`outs = ["install_tree.tar"]`,
		// All three phase headers render (autotools defaults supply
		// commands for configure / build / install).
		"# === configure ===",
		"# === build ===",
		"# === install ===",
		// Autogen branch detects ./configure and skips regeneration.
		"export NOCONFIGURE=1",
		"if [ -x ./configure ]; then",
		// Canonical autoconf flag set; %{prefix} is the BuildStream
		// stock /usr/local since this test doesn't ship a project.conf.
		"./configure --prefix=/usr/local",
		"--bindir=/usr/local/bin",
		"--libdir=/usr/local/lib",
		// Make + make-install with the runtime sentinel for
		// %{install-root}.
		`make -j1 DESTDIR="$$INSTALL_ROOT" install`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("autotools BUILD missing %q\n--body--\n%s", marker, got)
		}
	}
}

// TestWriter_AutotoolsElementHonorsConfLocal covers the per-element
// override path BuildStream documents: `variables: conf-local: ...`
// appends extra flags to ./configure without re-stating the
// surrounding %{conf-args} shape.
func TestWriter_AutotoolsElementHonorsConfLocal(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "configure"),
		[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Makefile.in"),
		[]byte("all:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "auto.bst")
	bstBody := `kind: autotools

sources:
- kind: local
  path: ` + srcDir + `

variables:
  conf-local: --enable-static --disable-shared
`
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outA, "elements/auto/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "--enable-static --disable-shared") {
		t.Errorf("conf-local override didn't reach rendered cmd:\n%s", body)
	}
}

// TestWriter_ImportElementShape covers kind:import: project-A
// no-target marker; project-B source tree staged verbatim plus a
// filegroup over glob("**/*", exclude=["BUILD.bazel"]).
func TestWriter_ImportElementShape(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "top.txt"), []byte("top\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "imp.bst")
	bstBody := "kind: import\nsources:\n- kind: local\n  path: " + srcDir + "\n"
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if g.Elements[0].Bst.Kind != "import" {
		t.Fatalf("Kind = %q, want import", g.Elements[0].Bst.Kind)
	}

	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	importA, err := os.ReadFile(filepath.Join(outA, "elements/imp/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"genrule(", "filegroup(", "cc_library("} {
		if strings.Contains(string(importA), banned) {
			t.Errorf("project A import BUILD should declare no targets, got %q in:\n%s", banned, importA)
		}
	}

	outB := filepath.Join(tmp, "B")
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	// Source tree staged verbatim into project B's element package.
	for _, rel := range []string{"top.txt", "sub/nested.txt"} {
		got, err := os.ReadFile(filepath.Join(outB, "elements/imp", rel))
		if err != nil {
			t.Errorf("staged file %q: %v", rel, err)
			continue
		}
		want, err := os.ReadFile(filepath.Join(srcDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("staged %q content differs from fixture", rel)
		}
	}
	importB, err := os.ReadFile(filepath.Join(outB, "elements/imp/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`name = "imp"`,
		`glob(["**/*"], exclude = ["BUILD.bazel"])`,
		`kind:import`,
	} {
		if !strings.Contains(string(importB), marker) {
			t.Errorf("project B import BUILD missing %q\n--body--\n%s", marker, importB)
		}
	}
}

// TestWriter_FilterElementShape covers kind:filter — single-dep
// validation, `config:` parsing of include / exclude / include-
// orphans recorded as comments in the rendered BUILD, and the
// pass-through filegroup-over-one-dep shape.
func TestWriter_FilterElementShape(t *testing.T) {
	tmp := t.TempDir()
	parent := makeCmakeBst(t, tmp, "lib")
	filter := filepath.Join(tmp, "lib-headers.bst")
	body := `kind: filter

depends:
- lib

config:
  include:
  - public-headers
  exclude:
  - runtime
  include-orphans: false
`
	if err := os.WriteFile(filter, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{parent, filter})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}

	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	filterA, err := os.ReadFile(filepath.Join(outA, "elements/lib-headers/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"genrule(", "filegroup(", "cc_library("} {
		if strings.Contains(string(filterA), banned) {
			t.Errorf("project A filter BUILD should declare no targets, got %q in:\n%s", banned, filterA)
		}
	}

	outB := filepath.Join(tmp, "B")
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	filterB, err := os.ReadFile(filepath.Join(outB, "elements/lib-headers/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`name = "lib-headers"`,
		`"//elements/lib:lib"`,
		`kind:filter`,
		`# include domains: [public-headers]`,
		`# exclude domains: [runtime]`,
		`# include-orphans: false`,
	} {
		if !strings.Contains(string(filterB), marker) {
			t.Errorf("project B filter BUILD missing %q\n--body--\n%s", marker, filterB)
		}
	}
}

// TestWriter_FilterRejectsMultipleDeps covers the single-dep
// invariant kind:filter enforces — filter is a slice of exactly one
// parent's install tree, so multi-dep filters surface as an error
// from the handler at render time.
func TestWriter_FilterRejectsMultipleDeps(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	b := makeCmakeBst(t, tmp, "b")
	bad := filepath.Join(tmp, "bad.bst")
	if err := os.WriteFile(bad,
		[]byte("kind: filter\ndepends:\n- a\n- b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{a, b, bad})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	err = writeProjectA(g, outA, binPath)
	if err == nil {
		t.Fatal("expected error for filter with 2 deps, got nil")
	}
	if !strings.Contains(err.Error(), "expected exactly 1 dep") {
		t.Errorf("error should name the single-dep invariant; got: %v", err)
	}
}

// TestWriter_ComposeElementShape covers kind:compose. Compose is
// rendering-shape-equivalent to kind:stack — the difference is the
// kind: marker and the BUILD comment, both validated below.
func TestWriter_ComposeElementShape(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	b := makeCmakeBst(t, tmp, "b")
	bundle := filepath.Join(tmp, "bundle.bst")
	if err := os.WriteFile(bundle,
		[]byte("kind: compose\ndepends:\n- a\n- b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{a, b, bundle})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if g.ByName["bundle"].Bst.Kind != "compose" {
		t.Fatalf("bundle Kind = %q, want compose", g.ByName["bundle"].Bst.Kind)
	}

	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	composeA, err := os.ReadFile(filepath.Join(outA, "elements/bundle/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	// Compose's project-A package declares no actionable targets.
	for _, banned := range []string{"genrule(", "filegroup(", "cc_library("} {
		if strings.Contains(string(composeA), banned) {
			t.Errorf("project A compose BUILD should declare no targets, got %q in:\n%s", banned, composeA)
		}
	}
	if !strings.Contains(string(composeA), "kind:compose") {
		t.Errorf("project A compose BUILD should carry kind:compose marker:\n%s", composeA)
	}

	outB := filepath.Join(tmp, "B")
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	composeB, err := os.ReadFile(filepath.Join(outB, "elements/bundle/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`name = "bundle"`,
		`"//elements/a:a"`,
		`"//elements/b:b"`,
		`kind:compose`,
	} {
		if !strings.Contains(string(composeB), marker) {
			t.Errorf("project B bundle BUILD missing %q\n--body--\n%s", marker, composeB)
		}
	}
}

func TestWriter_ManualElementShape(t *testing.T) {
	tmp := t.TempDir()

	// Trivial source tree the manual element references in its
	// install commands.
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "greeting.txt"),
		[]byte("Hello from kind:manual!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "greet.bst")
	bstBody := `kind: manual

sources:
- kind: local
  path: ` + srcDir + `

config:
  install-commands:
  - install -D greeting.txt %{install-root}%{prefix}/share/greeting.txt
`
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if len(g.Elements) != 1 || g.Elements[0].Bst.Kind != "manual" {
		t.Fatalf("Elements = %+v, want one kind:manual", g.Elements)
	}

	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outA, "elements/greet/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, marker := range []string{
		`name = "greet_install"`,
		`outs = ["install_tree.tar"]`,
		// %{install-root} stays as the runtime sentinel ($$INSTALL_ROOT);
		// %{prefix} expands to /usr/local at codegen time (BuildStream
		// stock default — this fixture has no project.conf to override
		// it the way the real meta-project fixtures do).
		`$$INSTALL_ROOT/usr/local/share/greeting.txt`,
		// Source-staging shadow merge same as cmake handler.
		`for src in $(SRCS)`,
		// install-commands phase header rendered.
		`# === install ===`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("manual element BUILD missing marker %q\n--body--\n%s", marker, got)
		}
	}
	// Source file copied into the project-A package.
	if _, err := os.Stat(filepath.Join(outA, "elements/greet/sources/greeting.txt")); err != nil {
		t.Errorf("sources/greeting.txt not staged: %v", err)
	}

	// Project B: placeholder until the driver post-processes the
	// install tarball into a real wrapper.
	outB := filepath.Join(tmp, "B")
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	bBuild, err := os.ReadFile(filepath.Join(outB, "elements/greet/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bBuild), "BUILD_NOT_YET_STAGED") {
		t.Errorf("project B kind:manual BUILD missing placeholder marker:\n%s", bBuild)
	}
}

func TestWriter_MakeElementShape(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Makefile"), []byte("all:\n\t@echo build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "build-it.bst")
	bstBody := `kind: make

sources:
- kind: local
  path: ` + srcDir + `
`
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if g.Elements[0].Bst.Kind != "make" {
		t.Fatalf("Kind = %q, want make", g.Elements[0].Bst.Kind)
	}

	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outA, "elements/build-it/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, marker := range []string{
		// Pipeline shape.
		`name = "build-it_install"`,
		`outs = ["install_tree.tar"]`,
		`for src in $(SRCS)`,
		// kind:make defaults render verbatim — no per-element
		// build/install commands in the .bst, so the handler's
		// pipelineDefaults filled them in.
		"# === build ===",
		"        make",
		"# === install ===",
		`make -j1 DESTDIR="$$INSTALL_ROOT" install`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("kind:make BUILD missing marker %q\n--body--\n%s", marker, got)
		}
	}
	// configure-commands and strip-commands have no defaults and no
	// .bst override → no headers for those phases.
	if strings.Contains(got, "# === configure ===") {
		t.Errorf("kind:make BUILD has unexpected configure phase header:\n%s", got)
	}
	if strings.Contains(got, "# === strip ===") {
		t.Errorf("kind:make BUILD has unexpected strip phase header:\n%s", got)
	}
}

func TestWriter_MakeElementOverridesDefaults(t *testing.T) {
	// .bst-supplied build-commands should replace kind:make's
	// default `make`. Verify the override path.
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Makefile"), []byte("all:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "make-override.bst")
	bstBody := `kind: make

sources:
- kind: local
  path: ` + srcDir + `

config:
  build-commands:
  - make custom-target
`
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outA, "elements/make-override/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "make custom-target") {
		t.Errorf("override build-commands not honored:\n%s", got)
	}
	if strings.Contains(got, "        make\n") {
		t.Errorf("override build-commands didn't replace default `make`:\n%s", got)
	}
	// install-commands has no .bst override → kind:make's default
	// install line still renders.
	if !strings.Contains(got, `make -j1 DESTDIR="$$INSTALL_ROOT" install`) {
		t.Errorf("install default missing despite no .bst override:\n%s", got)
	}
}

// TestWriter_ElementVariablesOverrideProjectDefaults checks the
// per-element variables: layer flows all the way through
// pipelineHandler.RenderA: a .bst that sets prefix=/opt/foo causes
// %{prefix}/share/... in install-commands to render with /opt/foo
// (rather than the default /usr from projectVars).
func TestWriter_ElementVariablesOverrideProjectDefaults(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "vary.bst")
	bstBody := `kind: manual

sources:
- kind: local
  path: ` + srcDir + `

variables:
  prefix: /opt/foo

config:
  install-commands:
  - install -D x.txt %{install-root}%{datadir}/x.txt
`
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outA, "elements/vary/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	// %{datadir} = %{prefix}/share, %{prefix} overridden to /opt/foo,
	// so the resolved path is /opt/foo/share. %{install-root} is the
	// runtime sentinel and stays as $$INSTALL_ROOT.
	want := `install -D x.txt $$INSTALL_ROOT/opt/foo/share/x.txt`
	if !strings.Contains(got, want) {
		t.Errorf("variable override not applied; want substring %q in:\n%s", want, got)
	}
	// And the unsubstituted %{prefix} / %{datadir} must not leak.
	for _, leak := range []string{`%{prefix}`, `%{datadir}`} {
		if strings.Contains(got, leak) {
			t.Errorf("unsubstituted reference %q leaked into output:\n%s", leak, got)
		}
	}
}

// TestWriter_UnknownVariableErrors covers the typo path: a .bst
// references %{not-a-real-var} in a phase command, the resolver
// reports the missing variable, and writeProjectA surfaces it.
func TestWriter_UnknownVariableErrors(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "typo.bst")
	bstBody := `kind: manual

sources:
- kind: local
  path: ` + srcDir + `

config:
  install-commands:
  - install -D x.txt %{install-root}%{nonexistent-prefix}/x.txt
`
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	err = writeProjectA(g, outA, binPath)
	if err == nil {
		t.Fatal("expected error for unresolved variable, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent-prefix") {
		t.Errorf("error should name the missing variable; got: %v", err)
	}
}

// TestWriter_ProjectConfVarsFlowThroughLoadGraph is the end-to-end
// project.conf integration: a .bst with no element variables, but a
// project.conf alongside that overrides prefix. loadGraph attaches
// the project.conf's variables: to every element via
// element.ProjectConfVars, and pipelineHandler.RenderA layers it
// into the resolver — so the rendered cmd reflects the override.
func TestWriter_ProjectConfVarsFlowThroughLoadGraph(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "project.conf"),
		[]byte("variables:\n  prefix: /opt/projwide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "x.bst")
	bstBody := `kind: manual

sources:
- kind: local
  path: ` + srcDir + `

config:
  install-commands:
  - install -D x.txt %{install-root}%{datadir}/x.txt
`
	if err := os.WriteFile(bst, []byte(bstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if got, want := g.Elements[0].ProjectConfVars["prefix"], "/opt/projwide"; got != want {
		t.Errorf("ProjectConfVars[prefix]: got %q, want %q", got, want)
	}

	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(outA, "elements/x/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	want := `install -D x.txt $$INSTALL_ROOT/opt/projwide/share/x.txt`
	if !strings.Contains(got, want) {
		t.Errorf("project.conf prefix override didn't reach rendered cmd; want substring %q in:\n%s", want, got)
	}
}

// TestWriter_MultiSourceImport covers kind:import with a 2-source
// element. write-a stages each source's tree into project B's
// element package; with no Directory set, the trees merge at the
// element-package root.
func TestWriter_MultiSourceImport(t *testing.T) {
	tmp := t.TempDir()
	srcA := filepath.Join(tmp, "src-a")
	srcB := filepath.Join(tmp, "src-b")
	for _, dir := range []string{srcA, srcB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcA, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcB, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "imp.bst")
	body := "kind: import\nsources:\n- kind: local\n  path: " + srcA + "\n- kind: local\n  path: " + srcB + "\n"
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if got := len(g.Elements[0].Sources); got != 2 {
		t.Fatalf("Sources len = %d, want 2", got)
	}
	binPath := fakeConvertBin(t, tmp)
	outB := filepath.Join(tmp, "B")
	if err := os.MkdirAll(outB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectA(g, filepath.Join(tmp, "A"), binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	for _, rel := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(outB, "elements/imp", rel)); err != nil {
			t.Errorf("multi-source: %s not staged in project B: %v", rel, err)
		}
	}
}

// TestWriter_SourceDirectoryMountsUnderSubpath covers the source-
// level `directory:` flag: a kind:local source with directory:
// extras stages its content under elemPkg/extras/ rather than at
// the package root.
func TestWriter_SourceDirectoryMountsUnderSubpath(t *testing.T) {
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "src-root")
	srcExtras := filepath.Join(tmp, "src-extras")
	for _, dir := range []string{srcRoot, srcExtras} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcExtras, "extra.txt"), []byte("extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "imp.bst")
	body := "kind: import\nsources:\n- kind: local\n  path: " + srcRoot + "\n- kind: local\n  path: " + srcExtras + "\n  directory: extras\n"
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if got := g.Elements[0].Sources[1].Directory; got != "extras" {
		t.Errorf("Sources[1].Directory: got %q, want %q", got, "extras")
	}
	binPath := fakeConvertBin(t, tmp)
	outB := filepath.Join(tmp, "B")
	if err := os.MkdirAll(outB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectA(g, filepath.Join(tmp, "A"), binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outB, "elements/imp/main.txt")); err != nil {
		t.Errorf("primary source not at element root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outB, "elements/imp/extras/extra.txt")); err != nil {
		t.Errorf("source with directory:extras not staged under extras/: %v", err)
	}
}

// TestWriter_MultiSourcePipeline covers kind:manual with two
// kind:local sources — one mounted at the source root, one under a
// `directory:` subpath. The genrule's source-stage block sees both
// in elemPkg/sources/, with the second under sources/extras/.
func TestWriter_MultiSourcePipeline(t *testing.T) {
	tmp := t.TempDir()
	primary := filepath.Join(tmp, "primary")
	patches := filepath.Join(tmp, "patches")
	for _, dir := range []string{primary, patches} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(primary, "main.c"), []byte("// main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(patches, "0001.patch"), []byte("--- patch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "elem.bst")
	body := `kind: manual

sources:
- kind: local
  path: ` + primary + `
- kind: local
  path: ` + patches + `
  directory: patches

config:
  install-commands:
  - echo done
`
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outA, "elements/elem/sources/main.c")); err != nil {
		t.Errorf("primary source not staged at sources/main.c: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outA, "elements/elem/sources/patches/0001.patch")); err != nil {
		t.Errorf("directory:patches source not staged at sources/patches/0001.patch: %v", err)
	}
}

// TestWriter_PublicBlockTolerated covers the public: data block
// real FDSDK elements declare. write-a doesn't act on it yet but
// must accept it without parse errors.
func TestWriter_PublicBlockTolerated(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "imp.bst")
	body := `kind: import

sources:
- kind: local
  path: ` + srcDir + `

public:
  bst:
    split-rules:
      runtime:
        - "/usr/lib/lib*.so*"
      devel:
        - "/usr/lib/lib*.so"
        - "/usr/include/**"
`
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v (public: block should be tolerated)", err)
	}
	if g.Elements[0].Bst.Public.IsZero() {
		t.Errorf("public: block should round-trip onto bstFile.Public; got zero node")
	}
}

// TestWriter_NonLocalSourceSkippedInStaging covers
// stageAllSources's skip-non-local behavior: an element with one
// kind:local + one kind:git_repo source stages the kind:local
// content into project B but leaves nothing on disk for the
// kind:git_repo entry. Render-time succeeds; bazel-build would
// require the source-fetch integration that's deferred.
func TestWriter_NonLocalSourceSkippedInStaging(t *testing.T) {
	tmp := t.TempDir()
	srcLocal := filepath.Join(tmp, "src-local")
	if err := os.MkdirAll(srcLocal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcLocal, "data.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bst := filepath.Join(tmp, "imp.bst")
	body := `kind: import

sources:
- kind: local
  path: ` + srcLocal + `
- kind: git_repo
  url: somealias:repo.git
  ref: deadbeef
  track: master
`
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if len(g.Elements[0].Sources) != 2 {
		t.Fatalf("Sources len = %d, want 2", len(g.Elements[0].Sources))
	}
	binPath := fakeConvertBin(t, tmp)
	outB := filepath.Join(tmp, "B")
	if err := os.MkdirAll(outB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectA(g, filepath.Join(tmp, "A"), binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}
	if err := writeProjectB(g, outB); err != nil {
		t.Fatalf("writeProjectB: %v", err)
	}
	// kind:local content was staged.
	if _, err := os.Stat(filepath.Join(outB, "elements/imp/data.txt")); err != nil {
		t.Errorf("kind:local source should be staged: %v", err)
	}
	// kind:git_repo metadata is on the resolvedSource entry; nothing
	// to assert in the staged tree (no bytes available without real
	// fetch).
	gitSrc := g.Elements[0].Sources[1]
	if gitSrc.Kind != "git_repo" {
		t.Errorf("Sources[1].Kind: got %q, want git_repo", gitSrc.Kind)
	}
	if gitSrc.URL != "somealias:repo.git" {
		t.Errorf("Sources[1].URL: got %q, want somealias:repo.git", gitSrc.URL)
	}
	if gitSrc.Ref != "deadbeef" || gitSrc.Track != "master" {
		t.Errorf("Sources[1] ref/track not recorded: %+v", gitSrc)
	}
}

// TestWriter_AllNonLocalSourcesRendersBuild covers the all-non-local
// case: an element whose every source is kind:git_repo / kind:patch
// / etc. still renders a BUILD (the genrule's source set will be
// empty, but write-a's render layer succeeds). Useful for the
// reality check, where most FDSDK elements declare kind:git_repo.
func TestWriter_AllNonLocalSourcesRendersBuild(t *testing.T) {
	tmp := t.TempDir()
	bst := filepath.Join(tmp, "elem.bst")
	body := `kind: manual

sources:
- kind: git_repo
  url: somealias:repo.git
  ref: aabbccdd
- kind: patch
  path: patches/0001.patch

config:
  install-commands:
  - echo done
`
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bst})
	if err != nil {
		t.Fatalf("loadGraph: %v (all-non-local sources should parse)", err)
	}
	binPath := fakeConvertBin(t, tmp)
	outA := filepath.Join(tmp, "A")
	if err := writeProjectA(g, outA, binPath); err != nil {
		t.Fatalf("writeProjectA: %v (all-non-local sources should render)", err)
	}
	if _, err := os.Stat(filepath.Join(outA, "elements/elem/BUILD.bazel")); err != nil {
		t.Errorf("BUILD.bazel should render even when no sources stage: %v", err)
	}
}

// TestWriter_PathQualifiedDeps covers the FDSDK-shape: element
// names key into the graph by their path relative to the project's
// element-root, so a depends-list reference like
// "components/foo.bst" resolves regardless of which subdir the
// dependent element lives in.
func TestWriter_PathQualifiedDeps(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "project.conf"),
		[]byte("variables:\n  prefix: /usr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// elements/components/foo.bst depends on elements/subdir/bar.bst
	// using the path-qualified form.
	for _, sub := range []string{"components", "subdir"} {
		if err := os.MkdirAll(filepath.Join(tmp, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srcDir := filepath.Join(tmp, "subdir-src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "CMakeLists.txt"),
		[]byte("cmake_minimum_required(VERSION 3.20)\nproject(bar)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "subdir/bar.bst"),
		[]byte("kind: cmake\nsources:\n- kind: local\n  path: "+srcDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "components/foo.bst"),
		[]byte("kind: stack\ndepends:\n- subdir/bar.bst\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{
		filepath.Join(tmp, "components/foo.bst"),
		filepath.Join(tmp, "subdir/bar.bst"),
	})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	// Names key by project-relative path (element-path defaults to "."
	// since project.conf doesn't set it).
	want := map[string]bool{"components/foo": true, "subdir/bar": true}
	for name := range g.ByName {
		if !want[name] {
			t.Errorf("unexpected element name %q in graph", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("missing element name %q in graph", name)
	}
	// foo's dep resolves to bar.
	foo := g.ByName["components/foo"]
	if foo == nil {
		t.Fatal("components/foo not in graph")
	}
	if len(foo.Deps) != 1 || foo.Deps[0].Name != "subdir/bar" {
		t.Errorf("path-qualified dep not resolved; got Deps=%v", foo.Deps)
	}
}

// TestWriter_PathQualifiedDeps_ElementPathSubdir covers the FDSDK
// case more precisely: project.conf sets element-path: elements,
// so .bst files at <project>/elements/foo/bar.bst key as "foo/bar"
// rather than "elements/foo/bar".
func TestWriter_PathQualifiedDeps_ElementPathSubdir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "project.conf"),
		[]byte("variables:\n  prefix: /usr\nelement-path: elements\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"elements/components", "elements/bootstrap"} {
		if err := os.MkdirAll(filepath.Join(tmp, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "CMakeLists.txt"),
		[]byte("cmake_minimum_required(VERSION 3.20)\nproject(b)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "elements/bootstrap/bar.bst"),
		[]byte("kind: cmake\nsources:\n- kind: local\n  path: "+srcDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "elements/components/foo.bst"),
		[]byte("kind: stack\nbuild-depends:\n- bootstrap/bar.bst\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{
		filepath.Join(tmp, "elements/components/foo.bst"),
		filepath.Join(tmp, "elements/bootstrap/bar.bst"),
	})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	// element-path: elements strips the "elements/" prefix, so names
	// are "components/foo" and "bootstrap/bar" (matching FDSDK's
	// dep-reference convention).
	if g.ByName["components/foo"] == nil {
		t.Fatalf("components/foo not in graph; have: %v", keysOf(g.ByName))
	}
	if g.ByName["bootstrap/bar"] == nil {
		t.Fatalf("bootstrap/bar not in graph; have: %v", keysOf(g.ByName))
	}
	foo := g.ByName["components/foo"]
	if len(foo.Deps) != 1 || foo.Deps[0].Name != "bootstrap/bar" {
		t.Errorf("path-qualified build-depends not resolved; got Deps=%v", foo.Deps)
	}
}

// TestWriter_PathQualifiedDeps_SameBasenameDifferentSubdirs covers
// the FDSDK case that broke basename keying: two elements with the
// same basename in different subdirs — like
// elements/components/bzip2.bst and elements/bootstrap/bzip2.bst —
// should be distinguishable by their path-qualified name.
func TestWriter_PathQualifiedDeps_SameBasenameDifferentSubdirs(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "project.conf"),
		[]byte("element-path: elements\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"elements/components", "elements/bootstrap"} {
		if err := os.MkdirAll(filepath.Join(tmp, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "elements/components/dup.bst"),
		[]byte("kind: stack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "elements/bootstrap/dup.bst"),
		[]byte("kind: stack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{
		filepath.Join(tmp, "elements/components/dup.bst"),
		filepath.Join(tmp, "elements/bootstrap/dup.bst"),
	})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	if g.ByName["components/dup"] == nil || g.ByName["bootstrap/dup"] == nil {
		t.Errorf("same-basename elements should both key by path; got %v", keysOf(g.ByName))
	}
}

// TestWriter_NoProjectConf_BasenameKeyingFallback covers the
// pre-project.conf code path: no project.conf found means name keying
// stays at basename-only (the existing testdata/meta-project/two-libs/
// fixture relies on this).
func TestWriter_NoProjectConf_BasenameKeyingFallback(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "lib-a")
	b := makeCmakeBst(t, tmp, "lib-b")
	g, err := loadGraph([]string{a, b})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	for _, want := range []string{"lib-a", "lib-b"} {
		if g.ByName[want] == nil {
			t.Errorf("expected basename keying %q without project.conf; got %v", want, keysOf(g.ByName))
		}
	}
}

// keysOf returns a sorted slice of the map keys (for stable error
// messages in the path-qualified-resolution tests above).
func keysOf(m map[string]*element) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestWriter_BuildDependsResolvedIntoDepsGraph covers the
// build-depends key (separate from `depends`) flowing into
// element.Deps. Without explicit handling, yaml.v3 silently drops
// the build-depends list since bstFile didn't have the field;
// adding bstFile.BuildDepends + the loadGraph merge reaches it.
func TestWriter_BuildDependsResolvedIntoDepsGraph(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	b := filepath.Join(tmp, "b.bst")
	if err := os.WriteFile(b,
		[]byte("kind: stack\nbuild-depends:\n- a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{a, b})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	bElem := g.ByName["b"]
	if bElem == nil {
		t.Fatal("element b not in graph")
	}
	if len(bElem.Deps) != 1 || bElem.Deps[0].Name != "a" {
		t.Errorf("build-depends not resolved into Deps; got Deps=%v", bElem.Deps)
	}
}

// TestWriter_RuntimeDependsResolvedIntoDepsGraph covers the
// runtime-depends key — same shape as build-depends.
func TestWriter_RuntimeDependsResolvedIntoDepsGraph(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	b := filepath.Join(tmp, "b.bst")
	if err := os.WriteFile(b,
		[]byte("kind: stack\nruntime-depends:\n- a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{a, b})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	bElem := g.ByName["b"]
	if len(bElem.Deps) != 1 || bElem.Deps[0].Name != "a" {
		t.Errorf("runtime-depends not resolved into Deps; got Deps=%v", bElem.Deps)
	}
}

// TestWriter_MergedDependsDedupesByElement covers the duplicate
// case: an element listed in both `depends:` and `build-depends:`
// still produces a single edge in element.Deps (topo sort and
// downstream rendering don't care about edge multiplicity, but
// keeping Deps unique avoids surprising the BUILD renderers).
func TestWriter_MergedDependsDedupesByElement(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	b := filepath.Join(tmp, "b.bst")
	body := `kind: stack

depends:
- a

build-depends:
- a
`
	if err := os.WriteFile(b, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{a, b})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	bElem := g.ByName["b"]
	if len(bElem.Deps) != 1 {
		t.Errorf("duplicate dep across depends + build-depends should dedupe; got %d edges", len(bElem.Deps))
	}
}

// TestWriter_DepMapFormParsed covers the junction-targeted dep
// shape: "- filename: foo.bst, junction: jx.bst, config: {...}".
// For v1 we resolve by Filename; junction + config are parsed but
// inert.
func TestWriter_DepMapFormParsed(t *testing.T) {
	tmp := t.TempDir()
	a := makeCmakeBst(t, tmp, "a")
	b := filepath.Join(tmp, "b.bst")
	body := `kind: stack

build-depends:
- filename: a.bst
  junction: somejunction.bst
  config:
    location: "%{sysroot}"
`
	if err := os.WriteFile(b, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{a, b})
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	bElem := g.ByName["b"]
	if len(bElem.Deps) != 1 || bElem.Deps[0].Name != "a" {
		t.Errorf("map-form dep not resolved by Filename; got Deps=%v", bElem.Deps)
	}
	// The Junction + Config fields are recorded on the bstDep entry
	// but inert — verify they round-tripped through the unmarshal.
	if got := bElem.Bst.BuildDepends[0].Junction; got != "somejunction.bst" {
		t.Errorf("junction not recorded on bstDep; got %q", got)
	}
	if bElem.Bst.BuildDepends[0].Config.IsZero() {
		t.Errorf("dep config not recorded on bstDep")
	}
}

// TestWriter_DepMapFormRequiresFilename covers the malformed map
// shape: a map-form dep without a filename: key surfaces as a parse
// error (without this, the silent default of empty filename would
// flow into graph resolution as a confusing "depends on \"\"").
func TestWriter_DepMapFormRequiresFilename(t *testing.T) {
	tmp := t.TempDir()
	bst := filepath.Join(tmp, "bad.bst")
	body := `kind: stack

build-depends:
- junction: somejunction.bst
`
	if err := os.WriteFile(bst, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGraph([]string{bst}); err == nil {
		t.Fatal("expected error for map-form dep without filename, got nil")
	}
}

// appendDepends adds a depends: list to an existing .bst file.
func appendDepends(bstPath string, deps []string) error {
	body, err := os.ReadFile(bstPath)
	if err != nil {
		return err
	}
	body = append(body, "depends:\n"...)
	for _, d := range deps {
		body = append(body, "- "+d+"\n"...)
	}
	return os.WriteFile(bstPath, body, 0o644)
}
