package lower

// configure_file detection.
//
// CMake's `configure_file(<input>.in <output>)` substitutes
// `@VAR@` and `${VAR}` references in the template against the
// current variable scope and writes the result into the build
// tree at configure time. The generated file then becomes a
// source-tree-equivalent header that .c files reference via
// `-I${CMAKE_CURRENT_BINARY_DIR}`.
//
// Codemodel doesn't surface configure_file as a target source
// (no entry in t.Sources, no compile group). It surfaces only
// as: a `.in`-suffixed entry in `cmakeFiles.inputs` with the
// path relative to the source tree. The generated output
// (resolved bytes) lives at `<build>/<input-rel-path-without-.in>`.
//
// Conversion strategy:
//   - Walk cmakeFiles.inputs for non-cmake, non-external,
//     non-generated `.in` files.
//   - For each, read the resolved generated file from the
//     build dir (only available when convert-element ran cmake
//     itself, OR when the test fixture's record-fileapi.sh
//     captured the build-dir output).
//   - Emit a Bazel genrule whose cmd writes the resolved
//     content verbatim — substitution already happened at
//     cmake-configure time, we just snapshot the result.
//
// Limitations / known gaps:
//   - Re-conversion captures whatever CMakeLists' set() values
//     resolved to at convert time. Same as cmake-fidelity:
//     editing the source `set()` and not re-running converter
//     keeps the prior value.
//   - Build-dir-only includes (target_include_directories(
//     <CMAKE_CURRENT_BINARY_DIR>)) currently filter out in
//     lowerTarget; once a target consumes a configure_file
//     output, its hdrs should pick up the generated header.
//     attributeConfigureFileOutputs walks targets and adds
//     matching outputs to their hdrs.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
	"github.com/sstriker/cmake-to-bazel/converter/internal/ir"
)

// detectConfigureFileOutputs walks cmakeFiles.inputs for `.in`
// files and returns the set of generated-header relative paths
// (relative to source root, where the genrule outputs land).
//
// The genrules themselves are appended to cc.Genrules so they
// emit at package scope. cmakeBuild must be the on-disk path
// to cmake's build dir; if the dir doesn't exist (offline
// --reply-dir path with no captured generated files) the
// returned set is empty and no genrules emit.
func detectConfigureFileOutputs(r *fileapi.Reply, cmakeBuild string, cc *codegenContext) (map[string]bool, error) {
	out := map[string]bool{}
	if cmakeBuild == "" {
		return out, nil
	}
	// Index .in inputs by basename-without-.in. Used to match
	// build-dir files whose name corresponds to a configure_file
	// template. The codemodel doesn't expose configure_file's
	// explicit output path (only the input), so we rely on the
	// canonical `<name>.in → <name>` shape.
	inByBaseStem := map[string]string{} // stem (e.g. "config.h") → input rel-path
	for _, in := range r.CMakeFiles.Inputs {
		if in.IsCMake || in.IsExternal || in.IsGenerated {
			continue
		}
		if !strings.HasSuffix(in.Path, ".in") {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(in.Path), ".in")
		if stem == "" {
			continue
		}
		inByBaseStem[stem] = in.Path
	}
	if len(inByBaseStem) == 0 {
		return out, nil
	}
	// Walk the build tree for files whose basename matches a
	// known stem. Skip cmake's own bookkeeping subdirs.
	skipDirs := map[string]bool{
		"CMakeFiles": true,
		".cmake":     true,
	}
	walkErr := filepath.WalkDir(cmakeBuild, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort walk; ignore unreadable subdirs
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		inputPath, ok := inByBaseStem[base]
		if !ok {
			return nil
		}
		// Output rel-path under the source root: relative to
		// cmakeBuild, so e.g. <build>/config.h → "config.h",
		// <build>/sub/config.h → "sub/config.h".
		outRel, relErr := filepath.Rel(cmakeBuild, p)
		if relErr != nil {
			return nil
		}
		body, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		// The output rel path already has the right shape under
		// the source root; the genrule's `outs` is just the
		// basename stripped (so the file lands at the package
		// root). Use the output rel path so multi-dir cases
		// stay distinct.
		genName := configureFileGenruleName(outRel)
		gen := ir.Target{
			Kind:        ir.KindGenrule,
			Name:        genName,
			Srcs:        []string{inputPath},
			GenruleOuts: []string{outRel},
			GenruleCmd: fmt.Sprintf(`mkdir -p $$(dirname $@) && cat > $@ <<'CONFIGURE_FILE_EOF'
%sCONFIGURE_FILE_EOF`, string(body)),
			Tags:       []string{"cmake-configure-file"},
			Visibility: []string{"//visibility:private"},
		}
		cc.Genrules = append(cc.Genrules, gen)
		out[outRel] = true
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// configureFileGenruleName turns "src/config.h.in" into
// "config_h_configure_file" (Bazel-friendly identifier). Multi-
// dir cases (e.g. "subdir/config.h.in") flatten the slashes
// into underscores so the genrule name stays unique without
// leaking package structure.
func configureFileGenruleName(outRel string) string {
	base := outRel
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.ReplaceAll(base, ".", "_")
	return base + "_configure_file"
}

// attributeConfigureFileOutputs walks the package's cc_library /
// cc_binary / cc_test targets and adds configure_file outputs
// to the hdrs of any target whose CompileGroups Include a path
// inside cmakeBuild (i.e. its target_include_directories names
// the build dir, the canonical `target_include_directories(...
// ${CMAKE_CURRENT_BINARY_DIR})` shape).
//
// Conservative: a target without a build-dir include doesn't
// consume the generated header from cmake's perspective, so
// adding it would be a false positive.
func attributeConfigureFileOutputs(r *fileapi.Reply, cmakeBuild string, generated map[string]bool, pkg *ir.Package) {
	if len(generated) == 0 {
		return
	}
	for ti := range pkg.Targets {
		t := &pkg.Targets[ti]
		if t.Kind == ir.KindGenrule {
			continue
		}
		// Find the codemodel target by name to inspect its raw
		// CompileGroups Includes (lower-pass irt.Includes was
		// already filtered to source-tree-relative entries; we
		// need the absolute paths from the codemodel).
		consumesBuildDir := false
		for _, ct := range r.Targets {
			if ct.Name != t.Name {
				continue
			}
			for _, cg := range ct.CompileGroups {
				for _, inc := range cg.Includes {
					if cmakeBuild != "" && (inc.Path == cmakeBuild || strings.HasPrefix(inc.Path, cmakeBuild+string(filepath.Separator))) {
						consumesBuildDir = true
						break
					}
				}
				if consumesBuildDir {
					break
				}
			}
			break
		}
		if !consumesBuildDir {
			continue
		}
		for outRel := range generated {
			t.Hdrs = append(t.Hdrs, outRel)
		}
		// Re-sort + dedupe in case the new entries collide.
		t.Hdrs = dedupeStrings(t.Hdrs)
	}
}
