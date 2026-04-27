package toolchain

// ResolvedToolchain is the after-Diff product: the Model that
// represents shared state across the variant matrix, plus per-
// build-type flag deltas isolated from the baseline.
//
// The emitter consumes this directly: BaseFlags / BaseLinkFlags
// land in compile_flags / link_flags (always-on); each entry in
// PerBuildType maps to one of Bazel's compilation_mode-keyed slots
// (dbg / opt) via standardCompilationMode.
type ResolvedToolchain struct {
	// Base mirrors the baseline variant's Model — host/target
	// platform, languages with their compiler info and BaseFlags
	// from CMAKE_<LANG>_FLAGS, tools. PerBuildType strips the
	// build-type contribution so this layer is pure.
	Base *Model

	// PerBuildType keyed by uppercase CMake build type
	// ("DEBUG", "RELEASE", "RELWITHDEBINFO", "MINSIZEREL"). Each
	// entry holds only the additional flags that build type
	// contributes — i.e. CMAKE_<LANG>_FLAGS_<BUILD_TYPE> tokens
	// minus anything already in Base.BaseFlags.
	PerBuildType map[string]BuildTypeDelta
}

// BuildTypeDelta is the per-language flag set that a single
// CMAKE_BUILD_TYPE adds on top of the baseline.
type BuildTypeDelta struct {
	// LanguageFlags keyed by CMake language ("C", "CXX"). Each
	// value is the per-build-type contribution (e.g. -O3 -DNDEBUG
	// for Release, -O0 -g for Debug).
	LanguageFlags map[string][]string

	// LinkFlags is the merged exe + shared linker delta from
	// CMAKE_EXE_LINKER_FLAGS_<BUILD_TYPE> + the corresponding
	// CMAKE_SHARED_LINKER_FLAGS_<BUILD_TYPE>.
	LinkFlags []string
}

// Diff folds a probe matrix into a ResolvedToolchain. The first
// variant with empty BuildType wins as the baseline; if no such
// variant exists, the first probed variant becomes the baseline
// and contributes no per-build-type delta.
//
// Returns nil when results is empty.
func Diff(results []ProbeResult) *ResolvedToolchain {
	if len(results) == 0 {
		return nil
	}
	baseIdx := pickBaselineIndex(results)
	rt := &ResolvedToolchain{
		Base:         cloneBaseline(results[baseIdx].Model),
		PerBuildType: map[string]BuildTypeDelta{},
	}

	baseFlags := baseLanguageFlags(rt.Base)

	for i, r := range results {
		if i == baseIdx || r.Variant.BuildType == "" {
			continue
		}
		key := upper(r.Variant.BuildType)
		delta := BuildTypeDelta{LanguageFlags: map[string][]string{}}
		for lang, l := range r.Model.Languages {
			// CMAKE_<LANG>_FLAGS_<BUILD_TYPE> already lives on
			// Language.BuildTypeFlags after FromReply. Strip
			// anything that's already in BaseFlags so we don't
			// double-count flags shared between baseline and the
			// variant.
			contribution := stripDuplicates(l.BuildTypeFlags, baseFlags[lang])
			if len(contribution) > 0 {
				delta.LanguageFlags[lang] = contribution
			}
			// LinkBuildTypeFlags is already exe+shared merged
			// inside FromReply; treat as the link delta.
			if len(l.LinkBuildTypeFlags) > 0 && lang == primaryLangKey(rt.Base) {
				delta.LinkFlags = append(delta.LinkFlags, l.LinkBuildTypeFlags...)
			}
		}
		rt.PerBuildType[key] = delta
	}
	return rt
}

// pickBaselineIndex returns the index of the variant we treat as
// the baseline. Preference order:
//
//  1. Empty BuildType (the explicit "no per-build-type" probe).
//  2. The first variant in the slice.
//
// Most probe matrices include the empty-BuildType row by default
// (DefaultVariants does); operators that skip it accept the
// drift caused by deriving the baseline from a build-typed run.
func pickBaselineIndex(results []ProbeResult) int {
	for i, r := range results {
		if r.Variant.BuildType == "" {
			return i
		}
	}
	return 0
}

// cloneBaseline copies the chosen baseline Model, zeroing out
// BuildType and per-build-type flag slots so the Base field
// represents purely the always-on layer.
func cloneBaseline(src *Model) *Model {
	if src == nil {
		return nil
	}
	out := &Model{
		HostPlatform:   src.HostPlatform,
		TargetPlatform: src.TargetPlatform,
		BuildType:      "",
		Tools:          src.Tools,
		Languages:      map[string]Language{},
	}
	for k, l := range src.Languages {
		copy := l
		copy.BuildTypeFlags = nil
		copy.LinkBuildTypeFlags = nil
		copy.BuiltinIncludeDirs = append([]string(nil), l.BuiltinIncludeDirs...)
		copy.BuiltinLinkDirs = append([]string(nil), l.BuiltinLinkDirs...)
		copy.BaseFlags = append([]string(nil), l.BaseFlags...)
		copy.LinkFlags = append([]string(nil), l.LinkFlags...)
		copy.SourceFileExtensions = append([]string(nil), l.SourceFileExtensions...)
		out.Languages[k] = copy
	}
	return out
}

// baseLanguageFlags returns a map keyed by language with the
// always-on flag set. Used to subtract from per-build-type flag
// sets so the deltas are pure.
func baseLanguageFlags(base *Model) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(base.Languages))
	for lang, l := range base.Languages {
		set := make(map[string]bool, len(l.BaseFlags))
		for _, f := range l.BaseFlags {
			set[f] = true
		}
		out[lang] = set
	}
	return out
}

func stripDuplicates(items []string, exclude map[string]bool) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, x := range items {
		if !exclude[x] {
			out = append(out, x)
		}
	}
	return out
}

// primaryLangKey returns the language key the emitter treats as
// the primary one ("C" if present, else any). Mirrors
// emit/bazeltoolchain's primaryLanguage selection.
func primaryLangKey(m *Model) string {
	if _, ok := m.Languages["C"]; ok {
		return "C"
	}
	for k := range m.Languages {
		return k
	}
	return ""
}

// CompilationMode maps a CMake build type (uppercase) to the
// corresponding Bazel cc_toolchain_config flag-set slot. Bazel's
// unix_cc_toolchain_config exposes only `dbg_compile_flags` and
// `opt_compile_flags`; the more nuanced CMake quartet folds:
//
//	DEBUG          -> dbg
//	RELEASE        -> opt
//	MINSIZEREL     -> opt   (close enough; no separate Bazel slot)
//	RELWITHDEBINFO -> opt   (same)
//
// Operators who need finer control (e.g. distinct minsize variant)
// can post-process the emitted .bzl by hand. Document the lossy
// mapping in toolchain-derivation-plan.md.
func CompilationMode(buildType string) string {
	switch upper(buildType) {
	case "DEBUG":
		return "dbg"
	case "RELEASE", "MINSIZEREL", "RELWITHDEBINFO":
		return "opt"
	default:
		return ""
	}
}

func upper(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c = c - 'a' + 'A'
		}
		out[i] = c
	}
	return string(out)
}
