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

func TestWriter_RejectsNonLocalSource(t *testing.T) {
	tmp := t.TempDir()
	bstPath := filepath.Join(tmp, "x.bst")
	if err := os.WriteFile(bstPath, []byte("kind: cmake\nsources:\n- kind: tar\n  url: foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGraph([]string{bstPath}); err == nil {
		t.Errorf("expected error for non-local source, got nil")
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
		// %{install-root} / %{prefix} substituted to shell vars.
		`$$INSTALL_ROOT$$PREFIX/share/greeting.txt`,
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
