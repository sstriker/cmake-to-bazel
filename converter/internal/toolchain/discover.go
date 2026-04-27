package toolchain

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// StandardBuildTypes are the four CMake-canonical build types.
// CMake hard-codes these in its bootstrap; custom build types
// declared via CMAKE_CONFIGURATION_TYPES extend the set
// project-side, but the canonical four are always available.
//
// Operators trim the variant matrix by passing a subset to
// VariantMatrix; they extend (or fully replace) by passing
// custom Variant entries directly.
var StandardBuildTypes = []string{"Debug", "Release", "RelWithDebInfo", "MinSizeRel"}

// DiscoverBuildTypes returns the canonical Variant entries for
// the standard CMake build types, plus a "baseline" variant with
// no CMAKE_BUILD_TYPE set so the empirical Observer has an
// uncontaminated reference point.
//
// Returned slice ordering: baseline first, then build types in
// alphabetical order for deterministic matrix iteration.
func DiscoverBuildTypes() []Variant {
	out := []Variant{{Name: "baseline"}}
	bts := append([]string(nil), StandardBuildTypes...)
	sort.Strings(bts)
	for _, bt := range bts {
		out = append(out, Variant{
			Name:      strings.ToLower(bt),
			CacheVars: map[string]string{"CMAKE_BUILD_TYPE": bt},
		})
	}
	return out
}

// DiscoverHostCompilers walks the PATH (or a caller-supplied
// directory list) for known C/C++ compiler driver names, returning
// one Variant per (C-compiler, C++-compiler) pair found.
//
// Naming: variants are named "<c-compiler-basename>" where the
// basename is the actual filename of the driver (e.g. "gcc-13",
// "clang", "clang-15"). Pairs are identified by a common
// suffix — we pair gcc with g++, clang with clang++, gcc-13 with
// g++-13.
//
// Returns an empty slice (not an error) when no compilers are
// found, so callers can union DiscoverBuildTypes() with
// DiscoverHostCompilers() without special-casing the empty case.
func DiscoverHostCompilers() []Variant {
	pathDirs := splitPath(os.Getenv("PATH"))
	return discoverCompilersIn(pathDirs)
}

// DiscoverHostCompilersIn is the testable variant: caller supplies
// the directory list. DiscoverHostCompilers wraps this with the
// process PATH.
func DiscoverHostCompilersIn(dirs []string) []Variant {
	return discoverCompilersIn(dirs)
}

func discoverCompilersIn(dirs []string) []Variant {
	// Collect every (name, fullPath) where name matches our
	// known C/C++ driver families. Then pair C/CXX by suffix.
	type found struct {
		family string // "gcc" / "g++" / "clang" / "clang++"
		suffix string // "" or "-13" etc.
		path   string
	}
	var entries []found
	seen := map[string]bool{}
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if seen[name] {
				continue
			}
			fam, suffix, ok := classifyCompilerName(name)
			if !ok {
				continue
			}
			seen[name] = true
			full := filepath.Join(dir, name)
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0o111 == 0 {
				// Not executable.
				continue
			}
			entries = append(entries, found{family: fam, suffix: suffix, path: full})
		}
	}

	// Pair C with CXX by (suffix). gcc + g++ go together; gcc-13 +
	// g++-13 likewise; clang + clang++; clang-15 + clang++-15.
	// Unpaired C compilers still produce a Variant (CXX falls
	// back to host autodetect).
	type pair struct {
		cPath, cxxPath string
		suffix         string
		family         string // "gcc" or "clang"
	}
	pairs := map[string]pair{} // key = family + suffix
	for _, e := range entries {
		key := variantFamilyKey(e.family) + e.suffix
		p := pairs[key]
		p.suffix = e.suffix
		p.family = variantFamilyKey(e.family)
		switch e.family {
		case "gcc", "clang":
			p.cPath = e.path
		case "g++", "clang++":
			p.cxxPath = e.path
		}
		pairs[key] = p
	}

	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []Variant
	for _, k := range keys {
		p := pairs[k]
		if p.cPath == "" && p.cxxPath == "" {
			continue
		}
		v := Variant{
			Name:      compilerVariantName(p.family, p.suffix),
			CacheVars: map[string]string{},
		}
		if p.cPath != "" {
			v.CacheVars["CMAKE_C_COMPILER"] = p.cPath
		}
		if p.cxxPath != "" {
			v.CacheVars["CMAKE_CXX_COMPILER"] = p.cxxPath
		}
		out = append(out, v)
	}
	return out
}

// classifyCompilerName extracts the family and suffix from a
// driver basename. Returns ok=false for non-compiler names.
//
//	gcc          -> "gcc", ""
//	gcc-13       -> "gcc", "-13"
//	g++          -> "g++", ""
//	g++-13       -> "g++", "-13"
//	clang        -> "clang", ""
//	clang-15     -> "clang", "-15"
//	clang++      -> "clang++", ""
//	clang++-15   -> "clang++", "-15"
//	x86_64-linux-gnu-gcc-13 -> "gcc", "-13" (suffix preserved;
//	                          target prefix dropped — we'd
//	                          re-detect target via cmake's File API)
func classifyCompilerName(name string) (family, suffix string, ok bool) {
	// Strip any "<arch>-<vendor>-<os>-" prefix gcc-style toolchains
	// use for cross-compile drivers. We keep the suffix (-N) because
	// it identifies the compiler version which is what differentiates
	// our variants.
	if i := strings.LastIndex(name, "-"); i >= 0 {
		// Try "<base>-<digits>" first (versioned suffix).
		base := name[:i]
		tail := name[i+1:]
		if isAllDigits(tail) {
			fam, ok := matchFamily(base)
			if ok {
				return fam, "-" + tail, true
			}
		}
	}
	if fam, ok := matchFamily(name); ok {
		return fam, "", true
	}
	// Cross-compile prefix: x86_64-linux-gnu-gcc, riscv64-linux-gnu-g++.
	parts := strings.Split(name, "-")
	if len(parts) >= 2 {
		// Try the last segment as a family name (or last + last-1
		// for "+++" trickery).
		if fam, ok := matchFamily(parts[len(parts)-1]); ok {
			return fam, "", true
		}
	}
	return "", "", false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func matchFamily(s string) (string, bool) {
	switch s {
	case "gcc", "g++", "clang", "clang++", "cc", "c++":
		return s, true
	}
	return "", false
}

// variantFamilyKey collapses a family name to its CMake-side
// language: "gcc", "g++", "cc" all key as "gcc"; "clang", "clang++"
// key as "clang"; "c++" keys as "gcc" (assumed gcc-style alias).
// The result is purely a pairing key.
func variantFamilyKey(family string) string {
	switch family {
	case "gcc", "g++", "cc", "c++":
		return "gcc"
	case "clang", "clang++":
		return "clang"
	default:
		return family
	}
}

// compilerVariantName produces a stable variant Name from a
// (family, suffix) pair: "gcc", "gcc-13", "clang-15", etc.
func compilerVariantName(family, suffix string) string {
	return family + suffix
}

// DeclareCrossToolchains scans dir for *.cmake files; each becomes
// a Variant whose CMAKE_TOOLCHAIN_FILE points at the file. Returns
// an empty slice when dir is empty or unreadable.
//
// File naming convention: the variant's Name is the basename minus
// the .cmake extension, lowercased and sanitized. Operators
// pre-stage their cross-toolchain files (e.g. arm64.cmake,
// riscv64.cmake) under one directory.
func DeclareCrossToolchains(dir string) ([]Variant, error) {
	if dir == "" {
		return nil, nil
	}
	var out []Variant
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(p) != ".cmake" {
			return nil
		}
		base := strings.TrimSuffix(filepath.Base(p), ".cmake")
		out = append(out, Variant{
			Name: sanitizeVariantName(base),
			CacheVars: map[string]string{
				"CMAKE_TOOLCHAIN_FILE": p,
			},
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("DeclareCrossToolchains: walk %s: %w", dir, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// VariantMatrix cross-products N axes of variants into a flat
// []Variant. Each output variant's Name is the dash-joined names
// of the inputs and CacheVars is the union of inputs (later axes
// win on key collisions).
//
// Axes are typically (build types, compilers, cross-toolchains).
// Empty axes are skipped — passing nil/[]Variant{} for any axis
// just means "don't multiply by that axis".
func VariantMatrix(axes ...[]Variant) []Variant {
	// Filter out empty axes.
	var nonempty [][]Variant
	for _, a := range axes {
		if len(a) > 0 {
			nonempty = append(nonempty, a)
		}
	}
	if len(nonempty) == 0 {
		return nil
	}
	// Recursive cross product.
	combos := []Variant{{}}
	for _, axis := range nonempty {
		next := make([]Variant, 0, len(combos)*len(axis))
		for _, c := range combos {
			for _, a := range axis {
				next = append(next, mergeVariants(c, a))
			}
		}
		combos = next
	}
	return combos
}

// mergeVariants returns a Variant whose Name is "a.Name-b.Name"
// (skipping empty parts) and whose CacheVars is the union of a
// and b's CacheVars. b's keys win on collisions.
func mergeVariants(a, b Variant) Variant {
	parts := []string{}
	if a.Name != "" {
		parts = append(parts, a.Name)
	}
	if b.Name != "" {
		parts = append(parts, b.Name)
	}
	out := Variant{
		Name:      strings.Join(parts, "-"),
		CacheVars: map[string]string{},
	}
	for k, v := range a.CacheVars {
		out.CacheVars[k] = v
	}
	for k, v := range b.CacheVars {
		out.CacheVars[k] = v
	}
	if len(out.CacheVars) == 0 {
		out.CacheVars = nil
	}
	return out
}

// CMakeReportsConfigurationTypes asks cmake what build types it
// knows about for the given source root, by running a one-off
// `cmake --system-information` parse. Useful when a project
// declares custom CMAKE_CONFIGURATION_TYPES; rare in practice for
// our target distros, so the function exists as an opt-in
// extension to DiscoverBuildTypes() rather than the default.
//
// Returns the union of CMake's standard four plus any custom
// types reported. Errors from cmake (binary missing, project
// reads failed) return the standard four with no error.
func CMakeReportsConfigurationTypes(cmakePath string) []string {
	if cmakePath == "" {
		cmakePath = "cmake"
	}
	if _, err := exec.LookPath(cmakePath); err != nil {
		return append([]string(nil), StandardBuildTypes...)
	}
	// cmake --system-information dumps a flat key=value list. We
	// scrape CMAKE_CONFIGURATION_TYPES if the project / install
	// declared one.
	cmd := exec.Command(cmakePath, "--system-information")
	body, err := cmd.Output()
	if err != nil {
		return append([]string(nil), StandardBuildTypes...)
	}
	custom := scrapeConfigurationTypes(body)
	out := append([]string(nil), StandardBuildTypes...)
	seen := map[string]bool{}
	for _, t := range out {
		seen[t] = true
	}
	for _, t := range custom {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func scrapeConfigurationTypes(body []byte) []string {
	// Look for a line like:
	//   CMAKE_CONFIGURATION_TYPES "Debug;Release;RelWithDebInfo;MinSizeRel"
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		const prefix = "CMAKE_CONFIGURATION_TYPES"
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimSpace(line[len(prefix):])
		// Format is typically: CMAKE_CONFIGURATION_TYPES "a;b;c"
		// or CMAKE_CONFIGURATION_TYPES "a;b;c" (with trailing
		// commentary). Strip the leading colon/equals/quote.
		rest = strings.TrimLeft(rest, ":= \t\"")
		rest = strings.TrimRight(rest, "\" \t")
		if rest == "" {
			continue
		}
		parts := strings.Split(rest, ";")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// splitPath splits a PATH-style env var by os.PathListSeparator.
// On Unix that's `:`. Empty returns nil.
func splitPath(env string) []string {
	if env == "" {
		return nil
	}
	return filepath.SplitList(env)
}
