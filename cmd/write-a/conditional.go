package main

// BuildStream (?): per-arch conditional blocks → project-B select().
//
// Real .bst files declare per-arch variable overrides via the `(?):`
// directive inside `variables:` blocks:
//
//   variables:
//     arch_options: ''
//     (?):
//     - target_arch == "x86_64":
//         arch_options: "--enable-sse2 --enable-avx"
//     - target_arch == "aarch64":
//         arch_options: "--enable-neon"
//
// 81 of 1 092 FDSDK elements (7 %) declare these. They control the
// shape of compiler flags, install destinations, and command lists
// per CPU architecture.
//
// At write-a time we don't pick an arch — that would bake host-arch
// into a cross-compile-capable system. Instead we extract the
// (?): block into a structured form and let the pipeline handler
// emit `cmd = select({...})` over `@platforms//cpu:*` so Bazel
// resolves per target platform at build time.
//
// extractConditionalsFromVariables walks a yaml.Node tree, finds the
// (?): block inside the top-level `variables:` map (the only place
// FDSDK puts them), parses each branch's expression, and removes
// the (?): key from the tree so the subsequent struct-decode step
// doesn't choke on the unhandled shape. Returns the parsed branches.
//
// Expression syntax recognized for v1:
//   target_arch == "X"
//   target_arch == "X" or target_arch == "Y" or ...
//   target_arch in ("X", "Y", ...)
//   target_arch != "X"        (matches every supported-arch except X)
// Expressions referencing other variables (host_arch, build_arch,
// custom %{variant}) and richer combinators (and / parentheses)
// are not yet supported — branches with unrecognized expressions
// produce no arches and are silently skipped (a real-FDSDK miss
// surfaces as a missing `cmd` branch in the rendered select(),
// which is preferable to baking in a wrong default).

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// supportedArches enumerates the CPU arches write-a renders
// `cmd = select({...})` branches for. Each entry maps to a Bazel
// constraint label; FDSDK uses these arch identifiers in its
// target_arch == "..." expressions. The set mirrors FDSDK's
// project.conf arch.yml branches.
var supportedArches = []string{
	"x86_64",
	"aarch64",
	"i686",
	"ppc64le",
	"riscv64",
	"loongarch64",
}

// archConstraintLabel returns the Bazel @platforms//cpu:* label
// each supported arch lowers to. i686 is a Bazel-side rename of
// "x86_32"; everything else matches the BuildStream-side identifier.
func archConstraintLabel(arch string) string {
	switch arch {
	case "i686":
		return "@platforms//cpu:x86_32"
	default:
		return "@platforms//cpu:" + arch
	}
}

// conditionalBranch is one entry in a (?): block's list. Varname
// is the LHS variable the expression dispatches on (`target_arch`,
// `bootstrap_build_arch`, ...); Arches is the set of values the
// branch matches. Overrides is the variable-overrides yaml.Node
// the branch contributes when one of its values matches.
//
// `target_arch`-keyed branches lower to project-B select() over
// @platforms//cpu:*. Branches keyed by other variables get
// statically evaluated against the staticDispatchVars map at
// graph-load time and folded into the parent variable layer.
type conditionalBranch struct {
	Varname   string
	Arches    []string
	Overrides *yaml.Node
}

// varEqualsRE matches `<var> == "X"` (with single-quote alternative).
// Whitespace tolerant. Captures (var, value).
var varEqualsRE = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_-]*)\s*==\s*['"]([^'"]+)['"]\s*$`)

// varNotEqualsRE matches `<var> != "X"`.
var varNotEqualsRE = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_-]*)\s*!=\s*['"]([^'"]+)['"]\s*$`)

// varInRE matches `<var> in ("X", "Y", "Z")`.
var varInRE = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_-]*)\s+in\s+\(([^)]+)\)\s*$`)

// archInTupleEntryRE extracts quoted arch names from the tuple
// body of varInRE.
var archInTupleEntryRE = regexp.MustCompile(`['"]([^'"]+)['"]`)

// parseArchExpression returns the LHS variable and the set of
// supported arches a BuildStream conditional expression matches.
// Most FDSDK uses target_arch on the LHS; bootstrap_build_arch /
// host_arch / build_arch and a few custom dispatch variables also
// appear (see flags.yml). Recognized syntax:
//
//	<var> == "X"
//	<var> != "X"
//	<var> in ("X", "Y", ...)
//	<var> == "X" or <var> == "Y" or ...   (consistent LHS variable)
//
// Unrecognized syntax / mixed-LHS or-chains return ("", nil) — the
// caller skips the branch (no select() entry emitted; static
// dispatch falls through).
//
// Arches matching is filtered to supportedArches for the LHS that
// drives Bazel select() lowering (target_arch); for other LHS
// variables the arches set returns the literal RHS values
// (bootstrap_build_arch / host_arch values aren't constrained to
// the @platforms//cpu:* set).
func parseArchExpression(expr string) (varname string, arches []string) {
	expr = strings.TrimSpace(expr)
	// `or`-joined chain — recurse on each part. The branch only
	// makes sense if every part has the same LHS variable; mixed-
	// LHS chains return ("", nil).
	if strings.Contains(expr, " or ") && !strings.Contains(expr, " and ") {
		parts := strings.Split(expr, " or ")
		var lhs string
		var out []string
		for _, p := range parts {
			v, vs := parseArchExpression(strings.TrimSpace(p))
			if v == "" {
				return "", nil
			}
			if lhs == "" {
				lhs = v
			} else if lhs != v {
				return "", nil
			}
			out = mergeArches(out, vs)
		}
		return lhs, out
	}
	if m := varEqualsRE.FindStringSubmatch(expr); m != nil {
		varname = m[1]
		v := m[2]
		if varname == "target_arch" && !isSupported(v) {
			return varname, nil
		}
		return varname, []string{v}
	}
	if m := varNotEqualsRE.FindStringSubmatch(expr); m != nil {
		varname = m[1]
		notv := m[2]
		// !=  only well-defined for target_arch — we know the
		// closed set @platforms//cpu:* allows. For other vars,
		// can't enumerate the complement; leave nil.
		if varname != "target_arch" {
			return varname, nil
		}
		out := make([]string, 0, len(supportedArches))
		for _, a := range supportedArches {
			if a != notv {
				out = append(out, a)
			}
		}
		return varname, out
	}
	if m := varInRE.FindStringSubmatch(expr); m != nil {
		varname = m[1]
		var out []string
		for _, em := range archInTupleEntryRE.FindAllStringSubmatch(m[2], -1) {
			v := em[1]
			if varname == "target_arch" && !isSupported(v) {
				continue
			}
			out = append(out, v)
		}
		return varname, out
	}
	return "", nil
}

func isSupported(arch string) bool {
	for _, a := range supportedArches {
		if a == arch {
			return true
		}
	}
	return false
}

func mergeArches(a, b []string) []string {
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			a = append(a, x)
			seen[x] = true
		}
	}
	return a
}

// extractConditionalsFromVariables looks for a (?): key inside the
// top-level `variables:` map of doc, removes it from the tree, and
// returns the parsed branches. doc is expected to be the post-
// composer DocumentNode wrapping a top-level mapping (a .bst or
// project.conf).
//
// Branches whose expression we don't recognize get parsed with
// Arches=nil — they survive in the returned slice so a future
// expression-evaluator can evaluate them, but the v1 pipeline
// handler skips them (no select() branch emitted).
func extractConditionalsFromVariables(doc *yaml.Node) ([]conditionalBranch, error) {
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil, nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil
	}
	// Find variables: key.
	varsIdx := -1
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "variables" {
			varsIdx = i + 1
			break
		}
	}
	if varsIdx < 0 {
		return nil, nil
	}
	varsNode := root.Content[varsIdx]
	if varsNode.Kind != yaml.MappingNode {
		return nil, nil
	}
	// Find (?): inside variables.
	condIdx := -1
	for i := 0; i < len(varsNode.Content); i += 2 {
		if varsNode.Content[i].Value == "(?)" {
			condIdx = i
			break
		}
	}
	if condIdx < 0 {
		return nil, nil
	}
	condValue := varsNode.Content[condIdx+1]
	// Excise the (?): from variables: map.
	varsNode.Content = append(varsNode.Content[:condIdx], varsNode.Content[condIdx+2:]...)

	if condValue.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("(?): expected a sequence of branches, got node kind %d", condValue.Kind)
	}
	var branches []conditionalBranch
	for _, branchNode := range condValue.Content {
		// Each branch is a mapping with one key (the expression)
		// and the override map as its value.
		if branchNode.Kind != yaml.MappingNode || len(branchNode.Content) < 2 {
			return nil, fmt.Errorf("(?): branch must be a mapping with one expression key, got node kind %d", branchNode.Kind)
		}
		expr := branchNode.Content[0].Value
		overrides := branchNode.Content[1]
		varname, arches := parseArchExpression(expr)
		branches = append(branches, conditionalBranch{
			Varname:   varname,
			Arches:    arches,
			Overrides: overrides,
		})
	}
	return branches, nil
}

// staticDispatchVars carries the values of dispatch variables that
// (?): branches reference besides target_arch. write-a evaluates
// them statically — these aren't multi-arch-select() candidates
// (the dispatch values aren't constrained to @platforms//cpu:*),
// they're configuration knobs whose value is fixed at build-graph-
// load time.
//
// Defaults below match a host-arch-x86_64 build of FDSDK. The
// --build-arch flag overrides build_arch and bootstrap_build_arch
// together, since they typically share the host's CPU
// architecture.
var staticDispatchVars = map[string]string{
	"build_arch":           "x86_64",
	"host_arch":            "x86_64",
	"bootstrap_build_arch": "x86_64",
}

// foldStaticConditionals partitions branches into target_arch-
// keyed (returned unchanged for select() lowering) and others
// (statically evaluated against staticVars; matching branches'
// overrides folded into vars). Returns the filtered branch list
// + the augmented vars map.
//
// Branches with empty Varname (unrecognized expression) survive
// unchanged in the returned slice — the pipeline handler skips
// them, but they're available for a future expression-evaluator
// extension.
func foldStaticConditionals(vars map[string]string, branches []conditionalBranch, staticVars map[string]string) (map[string]string, []conditionalBranch) {
	out := map[string]string{}
	for k, v := range vars {
		out[k] = v
	}
	var remaining []conditionalBranch
	for _, b := range branches {
		switch b.Varname {
		case "target_arch", "":
			remaining = append(remaining, b)
		default:
			val, ok := staticVars[b.Varname]
			if !ok {
				// Unknown dispatch variable — preserve the branch
				// (caller skips); nothing to fold.
				remaining = append(remaining, b)
				continue
			}
			matches := false
			for _, a := range b.Arches {
				if a == val {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
			// Fold the matching branch's overrides into vars.
			out = applyConditional(out, &b)
		}
	}
	return out, remaining
}

// applyConditional layers the override variables for one
// conditional branch onto vars. Returns the merged map (does not
// mutate the input). Override values that reference other %{...}
// variables remain unresolved — callers run the result through the
// resolver to expand.
func applyConditional(vars map[string]string, branch *conditionalBranch) map[string]string {
	if branch == nil || branch.Overrides == nil {
		return vars
	}
	out := map[string]string{}
	for k, v := range vars {
		out[k] = v
	}
	if branch.Overrides.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i < len(branch.Overrides.Content); i += 2 {
		k := branch.Overrides.Content[i].Value
		out[k] = branch.Overrides.Content[i+1].Value
	}
	return out
}

// branchForArch returns the first branch whose Arches contains
// arch, or nil if no branch matches. Branches are evaluated in
// declaration order (BuildStream's contract: "first matching
// branch wins").
func branchForArch(branches []conditionalBranch, arch string) *conditionalBranch {
	for i := range branches {
		for _, a := range branches[i].Arches {
			if a == arch {
				return &branches[i]
			}
		}
	}
	return nil
}
