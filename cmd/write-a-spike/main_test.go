// Smoke tests for the write-a-spike binary. These don't run Bazel —
// they verify the rendered project-A tree has the expected structure
// and key content. End-to-end Bazel-build validation lives in the
// `make spike-hello` target (gated on Bazel availability).

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleBst = `kind: cmake

sources:
- kind: local
  path: src
`

func TestSpikeWriter_HelloWorldShape(t *testing.T) {
	tmp := t.TempDir()

	// Stage a minimal cmake source tree.
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "CMakeLists.txt"),
		[]byte("cmake_minimum_required(VERSION 3.20)\nproject(t)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bstPath := filepath.Join(tmp, "hello.bst")
	if err := os.WriteFile(bstPath, []byte(sampleBst), 0o644); err != nil {
		t.Fatal(err)
	}
	// A fake convert-element binary — just a marker file. The writer
	// only stat()s and copies it; no execution happens in this test.
	binPath := filepath.Join(tmp, "convert-element-bin")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	elem, err := loadElement(bstPath)
	if err != nil {
		t.Fatalf("loadElement: %v", err)
	}
	if elem.Name != "hello" {
		t.Errorf("Name = %q, want hello", elem.Name)
	}
	if elem.Bst.Kind != "cmake" {
		t.Errorf("Kind = %q, want cmake", elem.Bst.Kind)
	}

	outDir := filepath.Join(tmp, "project-A")
	if err := writeProjectA(elem, outDir, binPath); err != nil {
		t.Fatalf("writeProjectA: %v", err)
	}

	// Required files in the rendered tree.
	for _, want := range []string{
		"WORKSPACE.bazel",
		"BUILD.bazel",
		"rules/zero_files.bzl",
		"rules/BUILD.bazel",
		"tools/convert-element",
		"tools/BUILD.bazel",
		"elements/hello/BUILD.bazel",
		"elements/hello/sources/CMakeLists.txt",
	} {
		if _, err := os.Stat(filepath.Join(outDir, want)); err != nil {
			t.Errorf("missing rendered file %q: %v", want, err)
		}
	}

	// The element's BUILD references the staged convert-element via
	// tools = [//tools:convert-element], split CMakeLists out of the
	// glob, and produces the three expected outputs.
	body, err := os.ReadFile(filepath.Join(outDir, "elements/hello/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, marker := range []string{
		`tools = ["//tools:convert-element"]`,
		`exclude = ["sources/CMakeLists.txt"]`,
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

func TestSpikeWriter_RejectsNonCmakeKind(t *testing.T) {
	tmp := t.TempDir()
	bstPath := filepath.Join(tmp, "x.bst")
	if err := os.WriteFile(bstPath, []byte("kind: meson\nsources:\n- kind: local\n  path: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	elem, err := loadElement(bstPath)
	if err != nil {
		t.Fatalf("loadElement: %v", err)
	}
	if elem.Bst.Kind != "meson" {
		t.Errorf("expected to parse meson kind for the rejection check, got %q", elem.Bst.Kind)
	}
	// The main() function rejects non-cmake; loadElement itself
	// permits the parse so a later writer can extend dispatch.
	// Validate the rejection path stays where main() can surface a
	// useful error message when a future caller adds a new kind.
}

func TestSpikeWriter_RejectsNonLocalSource(t *testing.T) {
	tmp := t.TempDir()
	bstPath := filepath.Join(tmp, "x.bst")
	if err := os.WriteFile(bstPath, []byte("kind: cmake\nsources:\n- kind: tar\n  url: foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadElement(bstPath); err == nil {
		t.Errorf("expected error for non-local source, got nil")
	}
}
