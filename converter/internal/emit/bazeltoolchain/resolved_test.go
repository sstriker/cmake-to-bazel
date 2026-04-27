package bazeltoolchain

import (
	"strings"
	"testing"

	"github.com/sstriker/cmake-to-bazel/converter/internal/toolchain"
)

// TestEmitResolved_RoutesPerBuildTypeFlagsIntoBazelSlots: a
// hand-built ResolvedToolchain with -O3 on Release and -O0 on
// Debug must land -O3 in opt_compile_flags, -O0 in
// dbg_compile_flags. Bazel's compilation_mode toggles drive the
// right flag set without us re-running cmake.
func TestEmitResolved_RoutesPerBuildTypeFlagsIntoBazelSlots(t *testing.T) {
	rt := &toolchain.ResolvedToolchain{
		Base: &toolchain.Model{
			HostPlatform:   toolchain.Platform{OS: "Linux", CPU: "x86_64"},
			TargetPlatform: toolchain.Platform{OS: "Linux", CPU: "x86_64"},
			Languages: map[string]toolchain.Language{
				"C": {
					CompilerID:           "GNU",
					CompilerPath:         "/usr/bin/gcc",
					BuiltinIncludeDirs:   []string{"/usr/include"},
					BaseFlags:            []string{"-Wall"},
					SourceFileExtensions: []string{"c"},
				},
			},
		},
		PerBuildType: map[string]toolchain.BuildTypeDelta{
			"RELEASE": {LanguageFlags: map[string][]string{"C": {"-O3"}}},
			"DEBUG":   {LanguageFlags: map[string][]string{"C": {"-O0", "-g"}}},
		},
	}
	b, err := EmitResolved(rt, Config{})
	if err != nil {
		t.Fatalf("EmitResolved: %v", err)
	}
	cfg := string(b.Files["cc_toolchain_config.bzl"])
	for _, want := range []string{
		`compile_flags = [
            "-Wall",`,
		`opt_compile_flags = [
            "-O3",`,
		`dbg_compile_flags = [
            "-O0",
            "-g",`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("cc_toolchain_config.bzl missing %q\n%s", want, cfg)
		}
	}
}

// TestEmitResolved_OptModeMergesAcrossCMakeBuildTypes: Release
// adds -O3, MinSizeRel adds -Os; both fold to Bazel's "opt" mode.
// The merged opt_compile_flags must dedup-preserve order.
func TestEmitResolved_OptModeMergesAcrossCMakeBuildTypes(t *testing.T) {
	rt := &toolchain.ResolvedToolchain{
		Base: &toolchain.Model{
			Languages: map[string]toolchain.Language{
				"C": {CompilerPath: "/usr/bin/gcc"},
			},
		},
		PerBuildType: map[string]toolchain.BuildTypeDelta{
			"RELEASE":    {LanguageFlags: map[string][]string{"C": {"-O3", "-DNDEBUG"}}},
			"MINSIZEREL": {LanguageFlags: map[string][]string{"C": {"-Os", "-DNDEBUG"}}},
		},
	}
	b, _ := EmitResolved(rt, Config{})
	cfg := string(b.Files["cc_toolchain_config.bzl"])
	for _, want := range []string{`-O3`, `-Os`, `-DNDEBUG`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("opt slot missing %q\n%s", want, cfg)
		}
	}
	// -DNDEBUG appears in both; dedup means it's emitted once.
	count := strings.Count(cfg, `"-DNDEBUG"`)
	if count != 1 {
		t.Errorf("-DNDEBUG appears %d times, want 1 (dedup)", count)
	}
}

func TestEmitResolved_NilRejected(t *testing.T) {
	if _, err := EmitResolved(nil, Config{}); err == nil {
		t.Error("EmitResolved(nil) should error")
	}
	if _, err := EmitResolved(&toolchain.ResolvedToolchain{}, Config{}); err == nil {
		t.Error("EmitResolved with nil Base should error")
	}
}
