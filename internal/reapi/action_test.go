package reapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sstriker/cmake-to-bazel/internal/cas"
)

// fixture writes the standard four-input layout under a tmpdir and
// returns Inputs ready for Build.
func fixture(t *testing.T, withImports, withPrefix bool) Inputs {
	t.Helper()
	root := t.TempDir()

	shadow := filepath.Join(root, "shadow")
	mustMkdirP(t, shadow, "src")
	mustWrite(t, filepath.Join(shadow, "CMakeLists.txt"), "project(x)\n")
	mustWrite(t, filepath.Join(shadow, "src", "x.c"), "")

	conv := filepath.Join(root, "convert-element")
	mustWrite(t, conv, "#!/bin/sh\necho fake\n")
	if err := os.Chmod(conv, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	in := Inputs{
		ShadowDir:    shadow,
		ConverterBin: conv,
		Platform: []PlatformProperty{
			{Name: "OSFamily", Value: "linux"},
			{Name: "Arch", Value: "x86_64"},
			{Name: "cmake-version", Value: "3.28.3"},
		},
	}
	if withImports {
		path := filepath.Join(root, "imports.json")
		mustWrite(t, path, `{"version":1,"imports":[]}`)
		in.ImportsManifest = path
	}
	if withPrefix {
		prefix := filepath.Join(root, "prefix")
		mustMkdirP(t, prefix, "lib/cmake/x")
		mustWrite(t, filepath.Join(prefix, "lib/cmake/x/xConfig.cmake"), "")
		in.PrefixDir = prefix
	}
	return in
}

func TestBuild_Deterministic(t *testing.T) {
	in := fixture(t, true, true)
	a, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := Build(in)
	if err != nil {
		t.Fatalf("Build (rerun): %v", err)
	}
	if !cas.DigestEqual(a.ActionDigest, b.ActionDigest) {
		t.Fatalf("Action digest unstable: %s vs %s",
			cas.DigestString(a.ActionDigest), cas.DigestString(b.ActionDigest))
	}
	if !cas.DigestEqual(a.CommandDigest, b.CommandDigest) {
		t.Errorf("Command digest unstable: %s vs %s",
			cas.DigestString(a.CommandDigest), cas.DigestString(b.CommandDigest))
	}
	if !cas.DigestEqual(a.InputRoot.RootDigest, b.InputRoot.RootDigest) {
		t.Errorf("InputRoot digest unstable: %s vs %s",
			cas.DigestString(a.InputRoot.RootDigest), cas.DigestString(b.InputRoot.RootDigest))
	}
}

func TestBuild_DifferentPathsSameContent_SameDigest(t *testing.T) {
	in1 := fixture(t, false, false)
	in2 := fixture(t, false, false)
	a, _ := Build(in1)
	b, _ := Build(in2)
	if !cas.DigestEqual(a.ActionDigest, b.ActionDigest) {
		t.Fatalf("two host paths with same content should produce same Action digest, got %s vs %s",
			cas.DigestString(a.ActionDigest), cas.DigestString(b.ActionDigest))
	}
}

func TestBuild_ContentEditChangesActionDigest(t *testing.T) {
	in := fixture(t, false, false)
	before, _ := Build(in)

	// Mutate the converter binary.
	mustWrite(t, in.ConverterBin, "#!/bin/sh\necho different\n")

	after, _ := Build(in)
	if cas.DigestEqual(before.ActionDigest, after.ActionDigest) {
		t.Errorf("converter binary edit should change Action digest, got %s == %s",
			cas.DigestString(before.ActionDigest), cas.DigestString(after.ActionDigest))
	}
}

func TestBuild_PlatformDigestsDiffer(t *testing.T) {
	in1 := fixture(t, false, false)
	in1.Platform = []PlatformProperty{
		{Name: "cmake-version", Value: "3.28.3"},
	}
	in2 := in1
	in2.Platform = []PlatformProperty{
		{Name: "cmake-version", Value: "3.30.0"},
	}
	a1, _ := Build(in1)
	a2, _ := Build(in2)
	if cas.DigestEqual(a1.ActionDigest, a2.ActionDigest) {
		t.Errorf("different cmake-version should produce different Action digests")
	}
}

func TestBuild_ArgvHasCanonicalPaths(t *testing.T) {
	in := fixture(t, true, true)
	a, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	args := a.Command.Arguments
	want := []string{
		"bin/convert-element",
		"--source-root", "source",
		"--out-build", "BUILD.bazel",
		"--out-bundle-dir", "cmake-config",
		"--out-failure", "failure.json",
		"--out-read-paths", "read_paths.json",
		"--imports-manifest", "imports.json",
		"--prefix-dir", "prefix",
	}
	if len(args) != len(want) {
		t.Fatalf("argv length: got %d want %d (%v)", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("argv[%d]: got %q want %q", i, args[i], want[i])
		}
	}
}

func TestBuild_OutputPaths(t *testing.T) {
	in := fixture(t, false, false)
	a, _ := Build(in)
	want := []string{"BUILD.bazel", "cmake-config", "failure.json", "read_paths.json"}
	if len(a.OutputPaths) != len(want) {
		t.Fatalf("output_paths len: got %d want %d", len(a.OutputPaths), len(want))
	}
	for i := range want {
		if a.OutputPaths[i] != want[i] {
			t.Errorf("output_paths[%d]: got %q want %q", i, a.OutputPaths[i], want[i])
		}
	}
}

func TestBuild_InputRootContainsAllBlobs(t *testing.T) {
	in := fixture(t, true, true)
	a, _ := Build(in)
	// Every file referenced from any Directory must have its blob
	// known via InputRoot.Files keyed by digest hash.
	for _, dir := range a.InputRoot.Directories {
		for _, f := range dir.Files {
			if _, ok := a.InputRoot.Files[f.Digest.Hash]; !ok {
				t.Errorf("file %s digest %s not in InputRoot.Files", f.Name, cas.DigestString(f.Digest))
			}
		}
	}
}

func mustMkdirP(t *testing.T, parts ...string) {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
