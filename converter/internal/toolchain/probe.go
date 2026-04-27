package toolchain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sstriker/cmake-to-bazel/converter/internal/cmakerun"
	"github.com/sstriker/cmake-to-bazel/converter/internal/fileapi"
)

// Variant identifies one cmake configure invocation in the probe
// matrix. The CacheVars map fully describes the variant: each entry
// is passed as `-D<name>=<value>` to cmake. This generalizes the
// older BuildType-only shape — build types live alongside compiler
// overrides, sanitizer flags, and any other cmake cache knob that
// distinguishes one cell of the matrix from another.
//
// Two variants with identical CacheVars are equivalent; their Names
// must differ for log readability and for use as Bazel feature
// names downstream.
type Variant struct {
	// Name is a stable, human-readable identifier ("baseline",
	// "debug", "clang-15", "asan"). Lowercase, alphanumeric +
	// hyphens; sanitized into filesystem paths and Bazel labels at
	// emit time.
	Name string

	// CacheVars are passed to cmake as -D<name>=<value>. Empty map
	// = baseline (no overrides). Common entries:
	//
	//	CMAKE_BUILD_TYPE=Debug | Release | RelWithDebInfo | MinSizeRel
	//	CMAKE_C_COMPILER=/usr/bin/clang-15
	//	CMAKE_CXX_COMPILER=/usr/bin/clang++-15
	//	CMAKE_TOOLCHAIN_FILE=/path/to/cross.cmake
	//	CMAKE_C_FLAGS=-fsanitize=address
	CacheVars map[string]string
}

// DefaultVariants is the standard baseline + four-build-type matrix.
// Operators trim or extend via the discover/declare layer (see
// discover.go) or by passing []Variant directly to ProbeOptions.
var DefaultVariants = []Variant{
	{Name: "baseline"},
	{Name: "debug", CacheVars: map[string]string{"CMAKE_BUILD_TYPE": "Debug"}},
	{Name: "release", CacheVars: map[string]string{"CMAKE_BUILD_TYPE": "Release"}},
	{Name: "relwithdebinfo", CacheVars: map[string]string{"CMAKE_BUILD_TYPE": "RelWithDebInfo"}},
	{Name: "minsizerel", CacheVars: map[string]string{"CMAKE_BUILD_TYPE": "MinSizeRel"}},
}

// ProbeOptions controls one probe run.
type ProbeOptions struct {
	// SourceRoot is the path to the probe project — typically
	// converter/testdata/toolchain-probe, but any cmake project
	// works as long as it declares the languages we care about.
	SourceRoot string

	// BuildRoot is where each variant's build directory lives. The
	// probe creates BuildRoot/<variant.Name>/ per Configure call.
	// Created if absent.
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
// returning a ProbeResult per variant. Callers feed the slice into
// Observe to derive baseline + per-variant deltas, then into the
// emit package to render Bazel rules.
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
		buildDir := filepath.Join(opts.BuildRoot, sanitizeVariantName(v.Name))
		if err := os.MkdirAll(buildDir, 0o755); err != nil {
			return results, fmt.Errorf("toolchain.Probe: mkdir variant %q: %w", v.Name, err)
		}

		// cmakerun.Configure has a BuildType field (legacy) plus the
		// rest comes from -D arguments via env / cmake argv. For now
		// we map known keys to the dedicated fields and pass the
		// rest as raw cache settings — but cmakerun.Options doesn't
		// have a "raw cache vars" surface yet (queued). For
		// correctness the build-type case is the one that matters
		// today.
		buildType := v.CacheVars["CMAKE_BUILD_TYPE"]

		reply, err := cmakerun.Configure(ctx, cmakerun.Options{
			HostSourceRoot: opts.SourceRoot,
			HostBuildDir:   buildDir,
			BuildType:      buildType,
			Stdout:         opts.Stdout,
			Stderr:         opts.Stderr,
		})
		if err != nil {
			return results, fmt.Errorf("toolchain.Probe: configure variant %q: %w", v.Name, err)
		}
		r, err := fileapi.Load(reply.HostPath)
		if err != nil {
			return results, fmt.Errorf("toolchain.Probe: load reply for %q: %w", v.Name, err)
		}
		m, err := FromReply(r)
		if err != nil {
			return results, fmt.Errorf("toolchain.Probe: extract %q: %w", v.Name, err)
		}
		results = append(results, ProbeResult{Variant: v, Model: m, BuildDir: buildDir, Reply: r})
	}
	return results, nil
}

// ProbeResult is one row in the variant matrix: the input variant
// and the Model + raw Reply cmake produced for it. The raw Reply is
// retained so Observe can walk every cache entry empirically rather
// than only the fields we lift into Model.
type ProbeResult struct {
	Variant  Variant
	Model    *Model
	BuildDir string
	Reply    *fileapi.Reply
}

// sanitizeVariantName produces a filesystem-safe directory name.
// Empty defaults to "baseline"; non-empty maps to lowercased
// alphanumerics with non-alphanumerics replaced.
func sanitizeVariantName(name string) string {
	if name == "" {
		return "baseline"
	}
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
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

// SortedCacheVarKeys returns Variant.CacheVars' keys in lexicographic
// order. Used by Observe + the emit package to produce
// deterministic output regardless of map iteration order.
func SortedCacheVarKeys(v Variant) []string {
	keys := make([]string, 0, len(v.CacheVars))
	for k := range v.CacheVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// VariantString renders a Variant as a stable single-line string.
// Used in test logs and as a deterministic key for comparisons.
func VariantString(v Variant) string {
	if len(v.CacheVars) == 0 {
		return v.Name + "{}"
	}
	keys := SortedCacheVarKeys(v)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+v.CacheVars[k])
	}
	return v.Name + "{" + strings.Join(parts, ",") + "}"
}
