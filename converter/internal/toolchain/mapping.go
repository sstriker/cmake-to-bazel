package toolchain

import "strings"

// VariantMapping classifies a Variant into a Bazel-side feature
// slot. The emit package consumes this to decide which
// cc_toolchain_config flag-set slot a variant's delta lands in
// (compile_flags, dbg_compile_flags, opt_compile_flags, or a
// custom feature for sanitizers / coverage / etc.).
//
// Returning "" (BazelFeatureNone) means "this variant's delta is
// not routed anywhere"; the variant was probed for observation
// but produces no Bazel-side flag-set contribution.
type VariantMapping func(v Variant) BazelFeature

// BazelFeature identifies a slot in cc_toolchain_config. The
// strings match Bazel's compilation_mode names where applicable;
// custom feature names go through verbatim.
type BazelFeature string

const (
	BazelFeatureNone BazelFeature = ""
	BazelFeatureDbg  BazelFeature = "dbg" // -> dbg_compile_flags / link
	BazelFeatureOpt  BazelFeature = "opt" // -> opt_compile_flags / link
)

// DefaultVariantMapping is the standard cmake-build-type → Bazel
// compilation_mode mapping. Variants without CMAKE_BUILD_TYPE in
// their CacheVars route to BazelFeatureNone (their delta becomes
// part of the baseline / always-on layer if the empirical
// observer kept it there, or is dropped).
//
//	CMAKE_BUILD_TYPE=Debug                    -> dbg
//	CMAKE_BUILD_TYPE=Release | RelWithDebInfo
//	  | MinSizeRel                            -> opt
//
// Operators with custom variants (sanitizer cells, alt-compiler
// cells, cross-target cells) supply their own VariantMapping.
func DefaultVariantMapping(v Variant) BazelFeature {
	bt, ok := v.CacheVars["CMAKE_BUILD_TYPE"]
	if !ok {
		return BazelFeatureNone
	}
	switch strings.ToUpper(bt) {
	case "DEBUG":
		return BazelFeatureDbg
	case "RELEASE", "MINSIZEREL", "RELWITHDEBINFO":
		return BazelFeatureOpt
	default:
		return BazelFeatureNone
	}
}
