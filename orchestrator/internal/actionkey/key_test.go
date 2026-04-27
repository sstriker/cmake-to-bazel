package actionkey_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/actionkey"
)

// stage builds a small directory with the given entries (path -> content)
// and returns its absolute path.
func stage(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range entries {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCompute_StableForIdenticalInputs(t *testing.T) {
	dir := stage(t, map[string]string{
		"a.txt":     "a",
		"sub/b.txt": "b",
	})
	conv := stage(t, map[string]string{"convert-element": "stub"})

	k1, err := actionkey.Compute(actionkey.Inputs{
		ShadowDir:    dir,
		ConverterBin: filepath.Join(conv, "convert-element"),
	})
	if err != nil {
		t.Fatal(err)
	}
	k2, err := actionkey.Compute(actionkey.Inputs{
		ShadowDir:    dir,
		ConverterBin: filepath.Join(conv, "convert-element"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Errorf("identical inputs -> different keys:\n%s\n%s", k1, k2)
	}
}

func TestCompute_ChangesOnFileContent(t *testing.T) {
	dir := stage(t, map[string]string{"src/foo.c": "alpha"})
	conv := stage(t, map[string]string{"convert-element": "stub"})
	k1, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: dir, ConverterBin: filepath.Join(conv, "convert-element")})

	if err := os.WriteFile(filepath.Join(dir, "src", "foo.c"), []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	k2, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: dir, ConverterBin: filepath.Join(conv, "convert-element")})

	if k1 == k2 {
		t.Errorf("file-content change didn't shift key")
	}
}

func TestCompute_StableUnderEmptyZeroByteFile(t *testing.T) {
	// Empty stub files (the shadow tree's bread and butter) shouldn't
	// somehow vary: Compute must produce the same key for two trees
	// staged identically.
	a := stage(t, map[string]string{"src/big.c": "", "src/big.h": ""})
	b := stage(t, map[string]string{"src/big.c": "", "src/big.h": ""})
	conv := stage(t, map[string]string{"c": "x"})

	ka, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: a, ConverterBin: filepath.Join(conv, "c")})
	kb, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: b, ConverterBin: filepath.Join(conv, "c")})
	if ka != kb {
		t.Errorf("identical empty-stub trees produced different keys:\n%s\n%s", ka, kb)
	}
}

func TestCompute_ImportsManifestContributes(t *testing.T) {
	shadow := stage(t, map[string]string{"x": "x"})
	conv := stage(t, map[string]string{"c": "x"})
	importsA := filepath.Join(t.TempDir(), "i.json")
	if err := os.WriteFile(importsA, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	importsB := filepath.Join(t.TempDir(), "i.json")
	if err := os.WriteFile(importsB, []byte(`{"version":1,"elements":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	kA, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: shadow, ImportsManifest: importsA, ConverterBin: filepath.Join(conv, "c")})
	kB, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: shadow, ImportsManifest: importsB, ConverterBin: filepath.Join(conv, "c")})

	if kA == kB {
		t.Errorf("imports content change didn't shift key")
	}
}

func TestCompute_ConverterBinContributes(t *testing.T) {
	shadow := stage(t, map[string]string{"x": "x"})
	convA := stage(t, map[string]string{"c": "version-1"})
	convB := stage(t, map[string]string{"c": "version-2"})

	kA, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: shadow, ConverterBin: filepath.Join(convA, "c")})
	kB, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: shadow, ConverterBin: filepath.Join(convB, "c")})
	if kA == kB {
		t.Errorf("converter binary content change didn't shift key")
	}
}

func TestCompute_PathReorderingDoesntMatter(t *testing.T) {
	// Whether the walker happens to visit b/ before a/, the entry-list
	// sort inside Compute must produce the same byte stream.
	a := stage(t, map[string]string{"a/x": "1", "b/y": "2"})
	b := stage(t, map[string]string{"b/y": "2", "a/x": "1"})
	conv := stage(t, map[string]string{"c": "x"})

	ka, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: a, ConverterBin: filepath.Join(conv, "c")})
	kb, _ := actionkey.Compute(actionkey.Inputs{ShadowDir: b, ConverterBin: filepath.Join(conv, "c")})
	if ka != kb {
		t.Errorf("walk order shifted key:\n%s\n%s", ka, kb)
	}
}
