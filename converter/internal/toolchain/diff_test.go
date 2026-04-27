package toolchain

import (
	"reflect"
	"testing"
)

// TestDiff_SeparatesPerBuildTypeFromBaseline: hand-build a probe
// matrix where the baseline has BaseFlags=[-Wall] and Release
// adds -O3, Debug adds -O0+-g. After Diff:
//
//   - Base.Languages.C.BaseFlags == [-Wall]
//   - PerBuildType["RELEASE"].LanguageFlags.C == [-O3]
//   - PerBuildType["DEBUG"].LanguageFlags.C   == [-O0, -g]
//
// And -Wall is stripped from per-build-type entries even if a
// variant's CMAKE_C_FLAGS_<BUILD_TYPE> happens to include it.
func TestDiff_SeparatesPerBuildTypeFromBaseline(t *testing.T) {
	results := []ProbeResult{
		{
			Variant: Variant{BuildType: ""},
			Model: &Model{
				Languages: map[string]Language{
					"C": {BaseFlags: []string{"-Wall"}},
				},
			},
		},
		{
			Variant: Variant{BuildType: "Release"},
			Model: &Model{
				BuildType: "Release",
				Languages: map[string]Language{
					"C": {
						BaseFlags:      []string{"-Wall"},
						BuildTypeFlags: []string{"-O3"},
					},
				},
			},
		},
		{
			Variant: Variant{BuildType: "Debug"},
			Model: &Model{
				BuildType: "Debug",
				Languages: map[string]Language{
					"C": {
						BaseFlags: []string{"-Wall"},
						// -Wall here would double up; Diff strips it.
						BuildTypeFlags: []string{"-O0", "-g", "-Wall"},
					},
				},
			},
		},
	}

	rt := Diff(results)
	if rt == nil {
		t.Fatal("Diff returned nil")
	}
	if got := rt.Base.Languages["C"].BaseFlags; !reflect.DeepEqual(got, []string{"-Wall"}) {
		t.Errorf("Base.C.BaseFlags = %v, want [-Wall]", got)
	}
	if got := rt.Base.Languages["C"].BuildTypeFlags; got != nil {
		t.Errorf("Base.C.BuildTypeFlags should be nil after baseline-clone, got %v", got)
	}
	if got := rt.PerBuildType["RELEASE"].LanguageFlags["C"]; !reflect.DeepEqual(got, []string{"-O3"}) {
		t.Errorf("RELEASE.C = %v, want [-O3]", got)
	}
	if got := rt.PerBuildType["DEBUG"].LanguageFlags["C"]; !reflect.DeepEqual(got, []string{"-O0", "-g"}) {
		t.Errorf("DEBUG.C = %v, want [-O0, -g] (no -Wall — that's baseline)", got)
	}
}

// TestDiff_NoExplicitBaselineFallsBackToFirstVariant: when no
// variant has empty BuildType, Diff treats the first slot as the
// baseline. Per-build-type deltas of the chosen baseline are
// dropped; other variants compute deltas as usual.
func TestDiff_NoExplicitBaselineFallsBackToFirstVariant(t *testing.T) {
	results := []ProbeResult{
		{
			Variant: Variant{BuildType: "Release"},
			Model: &Model{
				BuildType: "Release",
				Languages: map[string]Language{
					"C": {
						BaseFlags:      []string{"-Wall"},
						BuildTypeFlags: []string{"-O3"},
					},
				},
			},
		},
		{
			Variant: Variant{BuildType: "Debug"},
			Model: &Model{
				BuildType: "Debug",
				Languages: map[string]Language{
					"C": {
						BaseFlags:      []string{"-Wall"},
						BuildTypeFlags: []string{"-O0", "-g"},
					},
				},
			},
		},
	}
	rt := Diff(results)
	// Baseline is Release; its per-build-type delta is dropped
	// (no PerBuildType[RELEASE]). Debug remains as a delta.
	if _, ok := rt.PerBuildType["RELEASE"]; ok {
		t.Errorf("baseline (Release) should not appear in PerBuildType")
	}
	if got := rt.PerBuildType["DEBUG"].LanguageFlags["C"]; !reflect.DeepEqual(got, []string{"-O0", "-g"}) {
		t.Errorf("DEBUG.C = %v, want [-O0, -g]", got)
	}
}

func TestDiff_EmptyResultsReturnsNil(t *testing.T) {
	if rt := Diff(nil); rt != nil {
		t.Errorf("Diff(nil) = %+v, want nil", rt)
	}
}

func TestCompilationMode(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"DEBUG":          "dbg",
		"Debug":          "dbg",
		"RELEASE":        "opt",
		"MinSizeRel":     "opt",
		"RelWithDebInfo": "opt",
		"NoSuchType":     "",
	}
	for in, want := range cases {
		if got := CompilationMode(in); got != want {
			t.Errorf("CompilationMode(%q) = %q, want %q", in, got, want)
		}
	}
}
