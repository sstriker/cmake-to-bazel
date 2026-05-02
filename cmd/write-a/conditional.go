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

// conditionalBranch is one entry in a (?): block's list. Arches is
// the parsed set of CPU arches the branch applies to (post-
// expression-evaluation, so a `target_arch == "x86_64" or
// target_arch == "aarch64"` expression yields ["x86_64",
// "aarch64"]). Overrides is the variable-overrides yaml.Node the
// branch contributes when one of its arches matches.
type conditionalBranch struct {
	Arches    []string
	Overrides *yaml.Node
}

// archEquals matches `target_arch == "X"` (with single-quote
// alternative). Whitespace tolerant.
var archEqualsRE = regexp.MustCompile(`^\s*target_arch\s*==\s*['"]([^'"]+)['"]\s*$`)

// archNotEquals matches `target_arch != "X"`.
var archNotEqualsRE = regexp.MustCompile(`^\s*target_arch\s*!=\s*['"]([^'"]+)['"]\s*$`)

// archIn matches `target_arch in ("X", "Y", "Z")` — list-membership.
var archInRE = regexp.MustCompile(`^\s*target_arch\s+in\s+\(([^)]+)\)\s*$`)

// archInTuple extracts arch names from the tuple body of archInRE.
var archInTupleEntryRE = regexp.MustCompile(`['"]([^'"]+)['"]`)

// parseArchExpression returns the set of supportedArches a
// BuildStream conditional expression matches. Unrecognized syntax
// yields nil (caller silently skips the branch); explicit-empty
// match (e.g. an arch listed that's not in supportedArches) is
// also nil.
func parseArchExpression(expr string) []string {
	expr = strings.TrimSpace(expr)
	// `or`-joined target_arch == X chain.
	if strings.Contains(expr, "or") && !strings.Contains(expr, "and") {
		parts := strings.Split(expr, "or")
		var out []string
		for _, p := range parts {
			matches := parseArchExpression(strings.TrimSpace(p))
			out = mergeArches(out, matches)
		}
		return out
	}
	if m := archEqualsRE.FindStringSubmatch(expr); m != nil {
		if isSupported(m[1]) {
			return []string{m[1]}
		}
		return nil
	}
	if m := archNotEqualsRE.FindStringSubmatch(expr); m != nil {
		out := make([]string, 0, len(supportedArches))
		for _, a := range supportedArches {
			if a != m[1] {
				out = append(out, a)
			}
		}
		return out
	}
	if m := archInRE.FindStringSubmatch(expr); m != nil {
		var out []string
		for _, em := range archInTupleEntryRE.FindAllStringSubmatch(m[1], -1) {
			if isSupported(em[1]) {
				out = append(out, em[1])
			}
		}
		return out
	}
	return nil
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
		branches = append(branches, conditionalBranch{
			Arches:    parseArchExpression(expr),
			Overrides: overrides,
		})
	}
	return branches, nil
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
