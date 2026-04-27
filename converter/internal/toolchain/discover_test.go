package toolchain

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestDiscoverBuildTypes_StandardFourPlusBaseline(t *testing.T) {
	got := DiscoverBuildTypes()
	if len(got) != 5 {
		t.Fatalf("DiscoverBuildTypes: %d entries, want 5 (baseline + 4)", len(got))
	}
	if got[0].Name != "baseline" {
		t.Errorf("first entry %q, want baseline", got[0].Name)
	}
	if len(got[0].CacheVars) != 0 {
		t.Errorf("baseline.CacheVars = %v, want empty", got[0].CacheVars)
	}
	want := []string{"baseline", "debug", "minsizerel", "relwithdebinfo", "release"}
	sort.Strings(want[1:])
	gotNames := make([]string, len(got))
	for i, v := range got {
		gotNames[i] = v.Name
	}
	gotSorted := append([]string{}, gotNames...)
	sort.Strings(gotSorted[1:])
	if !reflect.DeepEqual(gotSorted, want) {
		t.Errorf("names = %v, want %v", gotSorted, want)
	}
}

func TestClassifyCompilerName(t *testing.T) {
	cases := []struct {
		in     string
		family string
		suffix string
		ok     bool
	}{
		{"gcc", "gcc", "", true},
		{"gcc-13", "gcc", "-13", true},
		{"gcc-12", "gcc", "-12", true},
		{"g++", "g++", "", true},
		{"g++-13", "g++", "-13", true},
		{"clang", "clang", "", true},
		{"clang-15", "clang", "-15", true},
		{"clang++", "clang++", "", true},
		{"clang++-15", "clang++", "-15", true},
		{"cc", "cc", "", true},
		{"x86_64-linux-gnu-gcc", "gcc", "", true},
		{"some-random-binary", "", "", false},
		{"go", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		fam, suf, ok := classifyCompilerName(tc.in)
		if fam != tc.family || suf != tc.suffix || ok != tc.ok {
			t.Errorf("classifyCompilerName(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, fam, suf, ok, tc.family, tc.suffix, tc.ok)
		}
	}
}

// TestDiscoverHostCompilers_FromTmpdir: stage gcc + g++ + gcc-13 +
// g++-13 as executable empty files in a tmpdir, run discovery
// against that dir, assert two pairs come back.
func TestDiscoverHostCompilers_FromTmpdir(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"gcc", "g++", "gcc-13", "g++-13", "go", "ls"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte{}, 0o755); err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
	}

	got := DiscoverHostCompilersIn([]string{dir})
	if len(got) != 2 {
		t.Fatalf("DiscoverHostCompilersIn: %d variants, want 2 (gcc, gcc-13)\n%v", len(got), got)
	}
	names := make([]string, len(got))
	for i, v := range got {
		names[i] = v.Name
	}
	sort.Strings(names)
	want := []string{"gcc", "gcc-13"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}

	// gcc-13 variant should pair gcc-13 with g++-13.
	for _, v := range got {
		if v.Name == "gcc-13" {
			if got := v.CacheVars["CMAKE_C_COMPILER"]; got != filepath.Join(dir, "gcc-13") {
				t.Errorf("gcc-13.CMAKE_C_COMPILER = %q, want %q",
					got, filepath.Join(dir, "gcc-13"))
			}
			if got := v.CacheVars["CMAKE_CXX_COMPILER"]; got != filepath.Join(dir, "g++-13") {
				t.Errorf("gcc-13.CMAKE_CXX_COMPILER = %q, want %q",
					got, filepath.Join(dir, "g++-13"))
			}
		}
	}
}

func TestDeclareCrossToolchains(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"arm64.cmake", "riscv64.cmake", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0o644); err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
	}
	got, err := DeclareCrossToolchains(dir)
	if err != nil {
		t.Fatalf("DeclareCrossToolchains: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d variants, want 2 (arm64, riscv64)", len(got))
	}
	if got[0].Name != "arm64" || got[1].Name != "riscv64" {
		t.Errorf("names = [%s, %s], want [arm64, riscv64]", got[0].Name, got[1].Name)
	}
	if got[0].CacheVars["CMAKE_TOOLCHAIN_FILE"] != filepath.Join(dir, "arm64.cmake") {
		t.Errorf("arm64.CMAKE_TOOLCHAIN_FILE = %q", got[0].CacheVars["CMAKE_TOOLCHAIN_FILE"])
	}
}

func TestVariantMatrix_CrossProduct(t *testing.T) {
	bts := []Variant{
		{Name: "debug", CacheVars: map[string]string{"CMAKE_BUILD_TYPE": "Debug"}},
		{Name: "release", CacheVars: map[string]string{"CMAKE_BUILD_TYPE": "Release"}},
	}
	cs := []Variant{
		{Name: "gcc", CacheVars: map[string]string{"CMAKE_C_COMPILER": "/usr/bin/gcc"}},
		{Name: "clang", CacheVars: map[string]string{"CMAKE_C_COMPILER": "/usr/bin/clang"}},
	}
	got := VariantMatrix(bts, cs)
	if len(got) != 4 {
		t.Fatalf("got %d combinations, want 4 (2x2)", len(got))
	}
	names := make([]string, len(got))
	for i, v := range got {
		names[i] = v.Name
	}
	sort.Strings(names)
	want := []string{"debug-clang", "debug-gcc", "release-clang", "release-gcc"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	// Each combo must carry both axes' cache vars.
	for _, v := range got {
		if _, ok := v.CacheVars["CMAKE_BUILD_TYPE"]; !ok {
			t.Errorf("%q missing CMAKE_BUILD_TYPE", v.Name)
		}
		if _, ok := v.CacheVars["CMAKE_C_COMPILER"]; !ok {
			t.Errorf("%q missing CMAKE_C_COMPILER", v.Name)
		}
	}
}

func TestVariantMatrix_EmptyAxesSkipped(t *testing.T) {
	bts := []Variant{
		{Name: "debug"},
		{Name: "release"},
	}
	got := VariantMatrix(bts, nil, []Variant{})
	if len(got) != 2 {
		t.Errorf("got %d, want 2 (empty axes should be skipped)", len(got))
	}
}

func TestVariantMatrix_AllEmpty(t *testing.T) {
	if got := VariantMatrix(nil, []Variant{}); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestScrapeConfigurationTypes(t *testing.T) {
	body := []byte(`
some preamble
CMAKE_CONFIGURATION_TYPES "Debug;Release;Coverage"
more stuff
`)
	got := scrapeConfigurationTypes(body)
	want := []string{"Debug", "Release", "Coverage"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
