// Package exports reads imported-target declarations out of an element's
// synthesized cmake-config bundle and turns them into manifest.Export
// entries the orchestrator can register in the dep-export registry.
//
// Source of truth is the bundle's <Pkg>Targets.cmake. Parsing it (rather
// than modeling the converter's IR a second time) keeps the contract on
// the cmake-side surface — the same surface a real find_package consumer
// would see.
package exports

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sstriker/cmake-to-bazel/internal/manifest"
)

// addLibraryRe matches lines like
//
//	add_library(hello::hello STATIC IMPORTED)
//	add_library(FMT::fmt SHARED IMPORTED)
//	add_library(absl::strings INTERFACE IMPORTED)
//
// Group 1 = namespace, group 2 = target name, group 3 = type keyword.
// Whitespace and trailing-arg variations within the parens are tolerated.
// CMake allows hyphens in both namespace and target ID. Convention is
// underscores-only for namespaces but real-world bundles (e.g. one whose
// project name itself contains a hyphen) emit hyphenated namespaces.
var addLibraryRe = regexp.MustCompile(`^add_library\(\s*([A-Za-z0-9_-]+)\s*::\s*([A-Za-z0-9_-]+)\s+(STATIC|SHARED|MODULE|INTERFACE)\s+IMPORTED`)

// FromBundle scans a cmake-config bundle directory for imported targets and
// returns one manifest.Export per declaration. Element name is NOT applied
// to BazelLabel here — that's the caller's job (the orchestrator stamps the
// `@elem_<name>//:<target>` label after picking up the raw exports).
func FromBundle(bundleDir string) ([]*manifest.Export, error) {
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return nil, fmt.Errorf("exports: read %s: %w", bundleDir, err)
	}
	// Find the *Targets.cmake (singular, no -release suffix).
	var targetsFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, "Targets.cmake") {
			targetsFiles = append(targetsFiles, filepath.Join(bundleDir, name))
		}
	}
	if len(targetsFiles) == 0 {
		return nil, nil // nothing to export; not an error
	}

	seen := map[string]bool{}
	var out []*manifest.Export
	for _, path := range targetsFiles {
		exps, err := parseFile(path)
		if err != nil {
			return nil, err
		}
		for _, e := range exps {
			if seen[e.CMakeTarget] {
				continue
			}
			seen[e.CMakeTarget] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// parseFile extracts imported-target declarations from one Targets.cmake.
//
// The orchestrator ignores per-config overlays (Targets-release.cmake) —
// those carry IMPORTED_LOCATION_RELEASE and similar but no new add_library
// calls.
func parseFile(path string) ([]*manifest.Export, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var exps []*manifest.Export
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		m := addLibraryRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ns, target := m[1], m[2]
		exps = append(exps, &manifest.Export{
			CMakeTarget: ns + "::" + target,
		})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return exps, nil
}

// AsElement stamps a per-element BazelLabel onto raw exports and wraps
// them as a manifest.Element. The label form
// `@elem_<element-name>//:<target>` matches what M3's MODULE.bazel
// declares for each converted element repository; the `<target>` half is
// the second segment of the cmake-target namespaced name (`Pkg::target`
// -> `target`).
func AsElement(elementName string, raw []*manifest.Export) *manifest.Element {
	bazelRepo := bazelRepoFor(elementName)
	out := make([]*manifest.Export, 0, len(raw))
	for _, ex := range raw {
		// CMakeTarget is "Pkg::target"; the second half becomes the label.
		idx := strings.Index(ex.CMakeTarget, "::")
		if idx < 0 || idx+2 >= len(ex.CMakeTarget) {
			continue
		}
		target := ex.CMakeTarget[idx+2:]
		out = append(out, &manifest.Export{
			CMakeTarget: ex.CMakeTarget,
			BazelLabel:  fmt.Sprintf("@%s//:%s", bazelRepo, target),
		})
	}
	return &manifest.Element{
		Name:    bazelRepo,
		Exports: out,
	}
}

// bazelRepoFor maps an element name (with directory components) to a Bazel
// external-repo identifier. Bazel repo names are restricted to
// [A-Za-z0-9_-]; we replace path separators and dots with underscores and
// prefix with `elem_` so the orchestrator's MODULE.bazel can declare them
// uniformly.
func bazelRepoFor(elementName string) string {
	var sb strings.Builder
	sb.WriteString("elem_")
	for i := 0; i < len(elementName); i++ {
		c := elementName[i]
		switch {
		case (c >= 'a' && c <= 'z'),
			(c >= 'A' && c <= 'Z'),
			(c >= '0' && c <= '9'),
			c == '_':
			sb.WriteByte(c)
		default:
			sb.WriteByte('_')
		}
	}
	return sb.String()
}
