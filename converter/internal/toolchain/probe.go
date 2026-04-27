package toolchain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sstriker/cmake-to-bazel/converter/internal/cmakerun"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
)

// Variant identifies one cmake configure invocation in the probe
// matrix. Empty BuildType means "no -DCMAKE_BUILD_TYPE" (cmake's
// uninitialized state) and produces the baseline flag set without
// any per-build-type additions.
//
// ToolchainFile, when non-empty, is mounted into the sandbox and
// passed via -DCMAKE_TOOLCHAIN_FILE. Used for cross-compile
// probes; left empty for host-build.
type Variant struct {
	BuildType     string
	ToolchainFile string // host path; ignored when empty
}

// DefaultVariants is the standard four-variant matrix for a
// host-build probe: a baseline (no build type) plus the four CMake-
// canonical build types. Operators trim or extend via --variant.
var DefaultVariants = []Variant{
	{BuildType: ""},
	{BuildType: "Debug"},
	{BuildType: "Release"},
	{BuildType: "RelWithDebInfo"},
	{BuildType: "MinSizeRel"},
}

// ProbeOptions controls one probe run.
type ProbeOptions struct {
	// SourceRoot is the path to the probe project — typically
	// converter/testdata/toolchain-probe, but any cmake project
	// works as long as it declares the languages we care about.
	SourceRoot string

	// BuildRoot is where each variant's build directory lives. The
	// probe creates BuildRoot/<sanitized-variant>/ per Configure
	// call. Created if absent.
	BuildRoot string

	// Variants is the matrix to probe. Empty falls back to
	// DefaultVariants.
	Variants []Variant

	// Stdout, Stderr capture cmake output. Nil discards.
	Stdout, Stderr interface {
		Write([]byte) (int, error)
	}
}

// Probe runs cmake configure against SourceRoot once per variant,
// returning a Model per variant. Callers feed the slice into Diff
// to derive per-build-type flag deltas, then into the emit package
// to render Bazel rules.
//
// Errors short-circuit: if any single Configure fails, Probe
// returns the first error and the partial slice. Best-effort
// recovery is the caller's choice (e.g. retry one variant on a
// transient sandbox blip).
func Probe(ctx context.Context, opts ProbeOptions) ([]ProbeResult, error) {
	if opts.SourceRoot == "" {
		return nil, fmt.Errorf("toolchain.Probe: SourceRoot required")
	}
	if opts.BuildRoot == "" {
		return nil, fmt.Errorf("toolchain.Probe: BuildRoot required")
	}
	variants := opts.Variants
	if len(variants) == 0 {
		variants = DefaultVariants
	}
	if err := os.MkdirAll(opts.BuildRoot, 0o755); err != nil {
		return nil, fmt.Errorf("toolchain.Probe: mkdir build root: %w", err)
	}

	results := make([]ProbeResult, 0, len(variants))
	for _, v := range variants {
		buildDir := filepath.Join(opts.BuildRoot, sanitizeVariantName(v))
		if err := os.MkdirAll(buildDir, 0o755); err != nil {
			return results, fmt.Errorf("toolchain.Probe: mkdir variant %q: %w", v.BuildType, err)
		}

		reply, err := cmakerun.Configure(ctx, cmakerun.Options{
			HostSourceRoot: opts.SourceRoot,
			HostBuildDir:   buildDir,
			BuildType:      v.BuildType,
			Stdout:         opts.Stdout,
			Stderr:         opts.Stderr,
		})
		if err != nil {
			return results, fmt.Errorf("toolchain.Probe: configure variant %q: %w", v.BuildType, err)
		}
		r, err := fileapi.Load(reply.HostPath)
		if err != nil {
			return results, fmt.Errorf("toolchain.Probe: load reply for %q: %w", v.BuildType, err)
		}
		m, err := FromReply(r)
		if err != nil {
			return results, fmt.Errorf("toolchain.Probe: extract %q: %w", v.BuildType, err)
		}
		results = append(results, ProbeResult{Variant: v, Model: m, BuildDir: buildDir})
	}
	return results, nil
}

// ProbeResult is one row in the variant matrix: the input variant
// (BuildType + ToolchainFile) and the Model cmake produced for it.
type ProbeResult struct {
	Variant  Variant
	Model    *Model
	BuildDir string
}

// sanitizeVariantName produces a filesystem-safe directory name
// per variant. Empty BuildType maps to "baseline"; non-empty maps
// to lowercased "<build-type>" with non-alphanumerics replaced.
func sanitizeVariantName(v Variant) string {
	bt := v.BuildType
	if bt == "" {
		bt = "baseline"
	}
	out := make([]byte, 0, len(bt))
	for i := 0; i < len(bt); i++ {
		c := bt[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c-'A'+'a')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
