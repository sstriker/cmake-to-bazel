// Package lower converts a parsed CMake File API reply into the IR consumed by
// emit/. It is the conversion brain; most semantic decisions (rule kind
// classification, header discovery, flag splitting) live here.
//
// M1 scope: single-config (Release), single-language compile groups, no
// add_custom_command (genrule recovery is M2). Anything outside this scope
// returns a Tier-1 failure via failure.Error so the caller can surface it
// without aborting the conversion run.
package lower

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sstriker/cmake-to-bazel/converter/internal/failure"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/ir"
	"github.com/sstriker/cmake-to-bazel/converter/internal/ninja"
	"github.com/sstriker/cmake-to-bazel/internal/manifest"
)

// Options controls behavior that the orchestrator (M3) overrides per-package.
// M1 callers can pass the zero value.
type Options struct {
	// HostSourceRoot is the on-disk path to the source tree, used for
	// filesystem walks (e.g. header discovery under each include directory).
	// It may differ from the source root recorded in the File API codemodel
	// when cmake ran inside a sandbox that bind-mounted the source tree at
	// a different path (e.g. /src). Defaults to the codemodel's source path.
	HostSourceRoot string

	// Imports resolves out-of-tree imported targets (find_package-style
	// names that aren't part of the current codebase) to Bazel labels.
	// Optional; nil disables manifest lookup, in which case unresolved
	// link deps trigger unresolved-link-dep.
	Imports *manifest.Resolver
}

// Header file extensions we treat as `hdrs` candidates when walking include
// directories. Lowercase comparison.
var headerExts = map[string]bool{
	".h":   true,
	".hh":  true,
	".hpp": true,
	".hxx": true,
	".inl": true,
}

// ToIR lowers a parsed reply into a Package. The optional ninja graph
// enables genrule recovery for targets with isGenerated sources; pass nil to
// disable (M1-style behavior — generated sources then trigger
// unsupported-custom-command).
func ToIR(r *fileapi.Reply, g *ninja.Graph, opts Options) (*ir.Package, error) {
	if got := len(r.Codemodel.Configurations); got != 1 {
		return nil, failure.New(failure.UnsupportedTargetType,
			"M1 supports exactly one configuration; got %d", got)
	}
	cfg := r.Codemodel.Configurations[0]

	cmakeSrc := r.Codemodel.Paths.Source
	cmakeBuild := r.Codemodel.Paths.Build
	hostSrc := opts.HostSourceRoot
	if hostSrc == "" {
		hostSrc = cmakeSrc
	}

	pkg := &ir.Package{
		Name:       projectName(r),
		SourceRoot: hostSrc,
	}

	cc := newCodegenContext()

	// Build the in-codebase id -> Bazel-rule-name map up front so dep
	// lowering can map t.Dependencies[].Id to a label without re-walking
	// configurations. UTILITY targets (add_custom_target nodes) are
	// excluded — they have no Bazel equivalent, so depending on them is a
	// no-op; the underlying add_custom_command's outputs are referenced
	// via srcs/hdrs instead. utilityIDs records them separately so dep
	// resolution can distinguish "skip utility" from "unresolved".
	idToName := map[string]string{}
	utilityIDs := map[string]bool{}
	for _, tref := range cfg.Targets {
		if t, ok := r.Targets[tref.Id]; ok && t.Type == "UTILITY" {
			utilityIDs[tref.Id] = true
			continue
		}
		idToName[tref.Id] = tref.Name
	}

	for _, tref := range cfg.Targets {
		t, ok := r.Targets[tref.Id]
		if !ok {
			return nil, failure.New(failure.FileAPIMalformed,
				"target id %q in codemodel but not loaded", tref.Id)
		}
		irt, err := lowerTarget(&t, cmakeSrc, cmakeBuild, hostSrc, g, cc, idToName, utilityIDs, opts.Imports)
		if err != nil {
			return nil, err
		}
		if irt == nil {
			// lowerTarget returned (nil, nil) to skip — UTILITY targets
			// (add_custom_target nodes) and similar that have no Bazel
			// equivalent.
			continue
		}
		pkg.Targets = append(pkg.Targets, *irt)
	}

	// Append recovered genrules in deterministic order (build-statement
	// declaration order is stable since cc.Genrules is appended on first
	// encounter while walking targets in cfg order).
	pkg.Targets = append(pkg.Targets, cc.Genrules...)
	return pkg, nil
}

func projectName(r *fileapi.Reply) string {
	if e := r.Cache.Get("CMAKE_PROJECT_NAME"); e != nil {
		return e.Value
	}
	return ""
}

func lowerTarget(t *fileapi.Target, cmakeSrc, cmakeBuild, hostSrc string, g *ninja.Graph, cc *codegenContext, idToName map[string]string, utilityIDs map[string]bool, imports *manifest.Resolver) (*ir.Target, error) {
	irt := &ir.Target{Name: t.Name}

	switch t.Type {
	case "STATIC_LIBRARY":
		irt.Kind = ir.KindCCLibrary
		irt.Linkstatic = true
	case "SHARED_LIBRARY", "MODULE_LIBRARY":
		irt.Kind = ir.KindCCLibrary
	case "EXECUTABLE":
		irt.Kind = ir.KindCCBinary
	case "INTERFACE_LIBRARY":
		irt.Kind = ir.KindCCInterface
	case "UTILITY":
		// add_custom_target / add_dependencies grouping. The underlying
		// add_custom_command is recovered separately via genrule lookup;
		// the utility node itself has no Bazel equivalent.
		return nil, nil
	default:
		return nil, failure.New(failure.UnsupportedTargetType,
			"target %q has unsupported type %q", t.Name, t.Type)
	}

	consumesCodegen := false
	for i, src := range t.Sources {
		// CMake's bookkeeping `<build>/version.h.rule` files are internal
		// re-run markers; skip them silently.
		if strings.HasSuffix(src.Path, ".rule") {
			continue
		}

		if src.IsGenerated {
			relOut, _, err := cc.recoverGenrule(src.Path, cmakeSrc, cmakeBuild, g)
			if err != nil {
				return nil, err
			}
			consumesCodegen = true
			ext := strings.ToLower(filepath.Ext(relOut))
			switch {
			case isInCompileGroup(t, i):
				irt.Srcs = append(irt.Srcs, relOut)
			case headerExts[ext]:
				irt.Hdrs = append(irt.Hdrs, relOut)
			default:
				// Non-header, not compiled: still belongs in srcs so the
				// genrule's output is included in the package's input set.
				irt.Srcs = append(irt.Srcs, relOut)
			}
			continue
		}

		if !isInCompileGroup(t, i) {
			// Not assigned to a compile group: probably a header in
			// target_sources(); we'll discover hdrs via include-dir
			// walking below. Skip here.
			continue
		}
		irt.Srcs = append(irt.Srcs, src.Path)
	}
	if consumesCodegen {
		irt.Tags = append(irt.Tags, "has-cmake-codegen")
	}

	if len(t.CompileGroups) > 0 {
		// M1 assumption: at most one language per target. Aggregate the
		// first compile group's flags/includes/defines.
		cg := t.CompileGroups[0]
		copts, defs := splitCompileFragments(cg.CompileCommandFragments)
		irt.Copts = copts

		for _, d := range cg.Defines {
			defs = append(defs, d.Define)
		}
		irt.Defines = defs

		for _, inc := range cg.Includes {
			rel, ok := relativeIfInside(cmakeSrc, inc.Path)
			if !ok {
				continue
			}
			irt.Includes = append(irt.Includes, rel)
		}
	}

	hdrs, err := discoverHeaders(hostSrc, irt.Includes)
	if err != nil {
		return nil, err
	}
	// Merge filesystem-discovered headers with any generated headers
	// already appended above; sort+dedupe so the emitter's stable.
	merged := append(irt.Hdrs, hdrs...)
	sort.Strings(merged)
	irt.Hdrs = dedupeStrings(merged)

	// Lower dependencies. In-codebase target ids look like `<name>::@<hash>`
	// where <name> is the CMake target name; out-of-tree find_package-
	// imported targets carry a namespaced name like `Pkg::tgt::@<hash>`.
	// Resolution order:
	//
	//   1. In-codebase non-UTILITY target -> ":<name>"
	//   2. In-codebase UTILITY target -> skip silently (no Bazel equivalent)
	//   3. CMake target name in imports manifest -> bazel_label
	//   4. Otherwise -> Tier-1 unresolved-link-dep.
	for _, dep := range t.Dependencies {
		if name, ok := idToName[dep.Id]; ok {
			irt.Deps = append(irt.Deps, ":"+name)
			continue
		}
		if utilityIDs[dep.Id] {
			continue
		}
		cmakeName := stripIDHash(dep.Id)
		if export := imports.LookupCMakeTarget(cmakeName); export != nil {
			irt.Deps = append(irt.Deps, export.BazelLabel)
			continue
		}
		return nil, failure.New(failure.UnresolvedLinkDep,
			"target %q depends on %q which is neither in-codebase nor in the imports manifest",
			t.Name, cmakeName)
	}

	// Out-of-tree link fragments. CMake records IMPORTED_LOCATION paths
	// in t.Link.CommandFragments[role="libraries"] as resolved absolute
	// paths under the synth-prefix tree. The orchestrator's imports
	// manifest carries each export's link paths so we can rewrite those
	// fragments to Bazel labels.
	if t.Link != nil {
		seen := map[string]bool{}
		for _, d := range irt.Deps {
			seen[d] = true
		}
		for _, frag := range t.Link.CommandFragments {
			if frag.Role != "libraries" {
				continue
			}
			path := strings.TrimSpace(frag.Fragment)
			if path == "" || !filepath.IsAbs(path) {
				continue
			}
			if export := imports.LookupLinkPath(path); export != nil {
				if !seen[export.BazelLabel] {
					seen[export.BazelLabel] = true
					irt.Deps = append(irt.Deps, export.BazelLabel)
				}
			}
		}
	}

	if t.Install != nil && len(t.Install.Destinations) > 0 {
		irt.Visibility = []string{"//visibility:public"}
		irt.InstallDest = t.Install.Destinations[0].Path
	}

	if len(t.Artifacts) > 0 {
		irt.ArtifactName = t.Artifacts[0].Path
	} else if t.NameOnDisk != "" {
		irt.ArtifactName = t.NameOnDisk
	}

	switch {
	case t.Link != nil && t.Link.Language != "":
		irt.LinkLanguage = t.Link.Language
	case len(t.CompileGroups) > 0:
		irt.LinkLanguage = t.CompileGroups[0].Language
	}

	return irt, nil
}

// stripIDHash returns the CMake target name from a File-API target id of the
// form `<name>::@<hash>`. If the id has no hash suffix it is returned
// unchanged; namespaced names (Foo::bar::@<hash>) collapse to "Foo::bar".
func stripIDHash(id string) string {
	if i := strings.Index(id, "::@"); i >= 0 {
		return id[:i]
	}
	return id
}

// dedupeStrings returns a copy of in with consecutive duplicates removed. The
// caller is expected to have sorted in.
func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	out = append(out, in[0])
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// isInCompileGroup reports whether target source index i is referenced by any
// of the target's compileGroups. The CompileGroupIndex field on TargetSource
// can't be trusted on its own — it's an int with default 0, indistinguishable
// from "in group 0" vs "absent".
func isInCompileGroup(t *fileapi.Target, i int) bool {
	for _, cg := range t.CompileGroups {
		for _, idx := range cg.SourceIndexes {
			if idx == i {
				return true
			}
		}
	}
	return false
}

// splitCompileFragments parses each whitespace-delimited fragment piece. -D
// pieces are returned as defines (with the leading -D stripped); everything
// else is a copt. -I and -isystem are dropped — those come through
// compileGroup.includes structurally.
func splitCompileFragments(frags []fileapi.CommandFragment) (copts, defines []string) {
	for _, f := range frags {
		if f.Role != "" {
			// Reserved for link fragments; ignore on the compile side.
			continue
		}
		for _, p := range strings.Fields(f.Fragment) {
			switch {
			case strings.HasPrefix(p, "-D"):
				defines = append(defines, strings.TrimPrefix(p, "-D"))
			case strings.HasPrefix(p, "-I"), strings.HasPrefix(p, "-isystem"):
				// dropped: see compileGroup.includes
			default:
				copts = append(copts, p)
			}
		}
	}
	return copts, defines
}

// relativeIfInside returns (rel-path, true) if abs is at or below root, else
// ("", false). Returned slash style: filepath.ToSlash for portability of
// emitted BUILD files (Bazel labels use forward slashes always).
func relativeIfInside(root, abs string) (string, bool) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "", true
	}
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return "", false
	}
	return rel, true
}

// discoverHeaders walks each include dir under sourceRoot and returns a sorted
// deduplicated list of header files (package-relative). M1 walks recursively;
// per-file granularity (excluding subdirs) can come later.
func discoverHeaders(sourceRoot string, includeDirs []string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, inc := range includeDirs {
		absDir := filepath.Join(sourceRoot, inc)
		walkErr := filepath.WalkDir(absDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				// An include dir that doesn't exist on disk is an error
				// worth surfacing; this is rare (CMake validates include
				// dirs on PUBLIC), but possible if the shadow tree is
				// out of sync.
				return err
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if !headerExts[ext] {
				return nil
			}
			rel, err := filepath.Rel(sourceRoot, p)
			if err != nil {
				return err
			}
			seen[filepath.ToSlash(rel)] = struct{}{}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk include dir %q: %w", absDir, walkErr)
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}
