package shadow

import (
	"path/filepath"
	"strings"
)

// Matcher classifies a package-relative source path as either content-needed
// (true) or path-only (false). Implementations must be pure.
type Matcher interface {
	Allowed(rel string) bool
}

// MatcherFunc adapts a plain function to Matcher.
type MatcherFunc func(string) bool

func (f MatcherFunc) Allowed(rel string) bool { return f(rel) }

// DefaultAllowlist returns the project-wide allowlist that covers ~95% of
// CMake packages without per-package augmentation. Membership tracks the
// "Content allowlist" enumeration in docs/m1-plan.md and the architecture
// design.
//
// The five rules in priority order:
//
//  1. Top-level CMake driver files: CMakeLists.txt, *.cmake, *.in,
//     CMakePresets.json, CMakeUserPresets.json.
//  2. *.cmake.in (CMakePackageConfigHelpers' template extension).
//  3. Anything under a cmake/ CMake/ cmake_modules/ directory at any depth.
//  4. Conventional bare-name files: VERSION, AUTHORS, COPYING, LICENSE.
//  5. Else: path-only (zero-byte stub in the shadow tree).
func DefaultAllowlist() Matcher {
	return MatcherFunc(func(rel string) bool {
		rel = filepath.ToSlash(rel)
		base := filepath.Base(rel)

		// Rule 4: bare-name conventionals.
		switch base {
		case "CMakeLists.txt",
			"CMakePresets.json",
			"CMakeUserPresets.json",
			"VERSION",
			"AUTHORS",
			"COPYING",
			"LICENSE":
			return true
		}

		// Rule 2: *.cmake.in (must check before .in fallthrough).
		if strings.HasSuffix(base, ".cmake.in") {
			return true
		}

		// Rule 1: extension-only matches.
		switch filepath.Ext(base) {
		case ".cmake", ".in":
			return true
		}

		// Rule 3: any path component is cmake/CMake/cmake_modules.
		for _, c := range strings.Split(rel, "/") {
			switch c {
			case "cmake", "CMake", "cmake_modules":
				return true
			}
		}
		return false
	})
}
