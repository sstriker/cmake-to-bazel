package main

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseArchExpression(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		varname string
		want    []string
	}{
		{"equals-x86_64", `target_arch == "x86_64"`, "target_arch", []string{"x86_64"}},
		{"equals-single-quote", `target_arch == 'aarch64'`, "target_arch", []string{"aarch64"}},
		{"equals-unsupported-arch", `target_arch == "weird-arch"`, "target_arch", nil},
		{"or-chain", `target_arch == "x86_64" or target_arch == "aarch64"`, "target_arch", []string{"x86_64", "aarch64"}},
		{"in-tuple", `target_arch in ("ppc64le", "ppc64")`, "target_arch", []string{"ppc64le"}},
		{"in-tuple-three", `target_arch in ("x86_64", "aarch64", "riscv64")`, "target_arch", []string{"x86_64", "aarch64", "riscv64"}},
		{"not-equals", `target_arch != "x86_64"`, "target_arch",
			[]string{"aarch64", "i686", "ppc64le", "riscv64", "loongarch64"}},
		{"unrecognized", `target_arch matches /pattern/`, "", nil},
		// Non-target_arch LHS — flags.yml-shape (FDSDK):
		{"bootstrap-build-arch", `bootstrap_build_arch == "x86_64"`, "bootstrap_build_arch", []string{"x86_64"}},
		// Non-target_arch matches accept the literal RHS (not constrained
		// to supportedArches — those are Bazel's @platforms//cpu:* set,
		// only relevant for target_arch).
		{"bootstrap-non-supported-rhs", `bootstrap_build_arch == "weird-arch"`, "bootstrap_build_arch", []string{"weird-arch"}},
		{"host-arch-or-chain", `host_arch == "x86_64" or host_arch == "i686"`, "host_arch", []string{"x86_64", "i686"}},
		// Mixed-LHS or-chain: not yet supported; returns ("", nil).
		{"mixed-lhs", `target_arch == "x86_64" or bootstrap_build_arch == "aarch64"`, "", nil},
		// PR — richer expressions: parens + and-chains.
		{"parens-around-equals", `(target_arch == "x86_64")`, "target_arch", []string{"x86_64"}},
		{"parens-around-or", `(target_arch == "x86_64" or target_arch == "aarch64")`, "target_arch", []string{"x86_64", "aarch64"}},
		// and-chain over same LHS — intersection. The double-!=
		// negation chain is the dominant FDSDK shape: arches
		// excluded by both.
		{"and-double-negate", `target_arch != "x86_64" and target_arch != "i686"`, "target_arch",
			[]string{"aarch64", "ppc64le", "riscv64", "loongarch64"}},
		// in-tuple intersected with a single == — should narrow to the singleton.
		{"and-narrow-to-singleton", `target_arch in ("x86_64", "aarch64") and target_arch == "x86_64"`, "target_arch", []string{"x86_64"}},
		// Mixed-LHS and-chain: not yet supported; returns ("", nil).
		{"mixed-lhs-and", `target_arch == "x86_64" and bootstrap_build_arch == "aarch64"`, "", nil},
		// Empty intersection — well-defined: matches no arches.
		{"and-empty-intersection", `target_arch == "x86_64" and target_arch == "aarch64"`, "target_arch", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotVar, gotArches := parseArchExpression(tc.expr)
			if gotVar != tc.varname {
				t.Errorf("varname: got %q, want %q", gotVar, tc.varname)
			}
			if len(gotArches) != len(tc.want) {
				t.Fatalf("length mismatch\n got: %v\nwant: %v", gotArches, tc.want)
			}
			for i := range gotArches {
				if gotArches[i] != tc.want[i] {
					t.Errorf("entry %d: got %q, want %q", i, gotArches[i], tc.want[i])
				}
			}
		})
	}
}

// TestFoldStaticConditionals covers the non-target_arch fold pass:
// branches keyed by bootstrap_build_arch / host_arch / build_arch
// evaluate against staticDispatchVars at graph-load time, and
// matching branches' overrides fold into the variable map.
// target_arch-keyed branches stay in the returned slice for the
// pipeline-handler's select() lowering.
func TestFoldStaticConditionals(t *testing.T) {
	branches := []conditionalBranch{
		{Varname: "target_arch", Arches: []string{"x86_64"}},
		{Varname: "bootstrap_build_arch", Arches: []string{"x86_64"}},
		{Varname: "bootstrap_build_arch", Arches: []string{"aarch64"}},
	}
	// Fake the override on bootstrap_build_arch == "x86_64": set
	// build_arch_flags.
	branches[1].Overrides = mappingNode("build_arch_flags", "-msse4.2")
	branches[2].Overrides = mappingNode("build_arch_flags", "-march=armv8-a")
	staticVars := map[string]string{"bootstrap_build_arch": "x86_64"}
	vars := map[string]string{"prefix": "/usr"}
	out, remaining := foldStaticConditionals(vars, branches, staticVars, nil)
	if got := out["build_arch_flags"]; got != "-msse4.2" {
		t.Errorf("matching branch override should fold; got %q", got)
	}
	if got := out["prefix"]; got != "/usr" {
		t.Errorf("existing var should survive; got %q", got)
	}
	if len(remaining) != 1 || remaining[0].Varname != "target_arch" {
		t.Errorf("target_arch branch should survive in remaining; got %+v", remaining)
	}
}

// mappingNode builds a yaml.Node MappingNode with one entry for
// test setup convenience.
func mappingNode(key, value string) *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: key},
			{Kind: yaml.ScalarNode, Value: value},
		},
	}
}

func TestArchConstraintLabel(t *testing.T) {
	cases := map[string]string{
		"x86_64":      "@platforms//cpu:x86_64",
		"aarch64":     "@platforms//cpu:aarch64",
		"i686":        "@platforms//cpu:x86_32", // Bazel-side rename.
		"ppc64le":     "@platforms//cpu:ppc64le",
		"riscv64":     "@platforms//cpu:riscv64",
		"loongarch64": "@platforms//cpu:loongarch64",
	}
	for arch, want := range cases {
		if got := archConstraintLabel(arch); got != want {
			t.Errorf("archConstraintLabel(%q) = %q, want %q", arch, got, want)
		}
	}
}

func TestBranchForArch(t *testing.T) {
	branches := []conditionalBranch{
		{Arches: []string{"x86_64"}},
		{Arches: []string{"aarch64", "ppc64le"}},
	}
	if b := branchForArch(branches, "x86_64"); b == nil || b.Arches[0] != "x86_64" {
		t.Errorf("x86_64 should hit first branch")
	}
	if b := branchForArch(branches, "ppc64le"); b == nil || b.Arches[0] != "aarch64" {
		t.Errorf("ppc64le should hit second branch (which contains it)")
	}
	if b := branchForArch(branches, "riscv64"); b != nil {
		t.Errorf("riscv64 not in any branch; want nil, got %+v", b)
	}
}

// TestStripRemainingConditionals_DeepStrip covers the v1
// behaviour: extractConditionalsFromVariables pulls out
// variables:(?):, and stripRemainingConditionals handles the
// rest (under config:, environment:, public:, …) so the
// struct-decode pass succeeds. v1 silently drops the deeper
// branches; a typed extractor for config: lands when an FDSDK
// fixture forces it.
func TestStripRemainingConditionals_DeepStrip(t *testing.T) {
	tmp := t.TempDir()
	bstPath := tmp + "/x.bst"
	body := `kind: autotools
variables:
  prefix: /usr
config:
  configure-commands:
  - "./configure --prefix=%{prefix}"
  (?):
  - target_arch == "x86_64":
      configure-commands:
      - "./configure --prefix=%{prefix} --x86"
environment:
  CC: gcc
  (?):
  - host_arch == "aarch64":
      CC: aarch64-linux-gnu-gcc
`
	if err := os.WriteFile(bstPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bstPath}, "")
	if err != nil {
		t.Fatalf("loadGraph (with deep (?):): %v", err)
	}
	if g.Elements[0].Bst.Kind != "autotools" {
		t.Errorf("kind decode: got %q", g.Elements[0].Bst.Kind)
	}
}

// mappingHasKey is a small helper used by the strip-pass test.
func mappingHasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

// TestStripCondNode_RecursesIntoNestedMappings makes sure the
// strip walker reaches arbitrarily deep mappings (not just the
// top-level config: / environment: blocks).
func TestStripCondNode_RecursesIntoNestedMappings(t *testing.T) {
	body := `top:
  inner:
    deep:
      (?):
      - "x":
          a: 1
      regular: keep-me
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	stripRemainingConditionals(&doc)
	// Walk down to the "deep" map and verify (?): is gone but
	// regular: survived.
	root := doc.Content[0]
	top := root.Content[1]
	inner := top.Content[1]
	deep := inner.Content[1]
	if mappingHasKey(deep, "(?)") {
		t.Errorf("strip didn't recurse: (?): still in deep map")
	}
	if !mappingHasKey(deep, "regular") {
		t.Errorf("strip removed too much: regular: gone")
	}
}

func TestBstArchFromGOARCH(t *testing.T) {
	cases := map[string]string{
		"amd64":   "x86_64",
		"arm64":   "aarch64",
		"386":     "i686",
		"ppc64le": "ppc64le",
		"riscv64": "riscv64",
		"loong64": "loongarch64",
		// Unknown GOARCHes pass through.
		"sparc64": "sparc64",
	}
	for goarch, want := range cases {
		if got := bstArchFromGOARCH(goarch); got != want {
			t.Errorf("bstArchFromGOARCH(%q) = %q, want %q", goarch, got, want)
		}
	}
}

func TestDefaultStaticDispatchVars_SeededFromGOARCH(t *testing.T) {
	got := defaultStaticDispatchVars()
	for _, k := range []string{"build_arch", "host_arch", "bootstrap_build_arch"} {
		if got[k] == "" {
			t.Errorf("%s defaulted to empty; want auto-detected from runtime.GOARCH", k)
		}
	}
	// All three should auto-detect to the same value (host CPU).
	if got["build_arch"] != got["host_arch"] || got["host_arch"] != got["bootstrap_build_arch"] {
		t.Errorf("the three defaults should match (same host CPU): %+v", got)
	}
}

// TestExtractConditionalsFromConfig covers PR's typed extractor
// for `config: (?):` blocks — the FDSDK bootstrap pattern of
// per-arch configure-commands overrides.
func TestExtractConditionalsFromConfig(t *testing.T) {
	tmp := t.TempDir()
	bstPath := tmp + "/x.bst"
	body := `kind: autotools
config:
  configure-commands:
  - "./configure --prefix=/usr"
  (?):
  - target_arch == "x86_64":
      configure-commands:
      - "./configure --prefix=/usr --x86"
  - target_arch == "aarch64":
      configure-commands:
      - "./configure --prefix=/usr --aarch64"
`
	if err := os.WriteFile(bstPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := loadGraph([]string{bstPath}, "")
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	confConds := g.Elements[0].Bst.ConfigConditionals
	if len(confConds) != 2 {
		t.Fatalf("ConfigConditionals: got %d branches, want 2 (%+v)", len(confConds), confConds)
	}
	if confConds[0].Varname != "target_arch" || confConds[0].Arches[0] != "x86_64" {
		t.Errorf("branch 0: got %+v", confConds[0])
	}
	if confConds[1].Varname != "target_arch" || confConds[1].Arches[0] != "aarch64" {
		t.Errorf("branch 1: got %+v", confConds[1])
	}
}

// TestBranchMatchesTuple covers the dispatch-tuple matcher.
func TestBranchMatchesTuple(t *testing.T) {
	b := conditionalBranch{Varname: "target_arch", Arches: []string{"x86_64", "aarch64"}}
	if !branchMatchesTuple(b, map[string]string{"target_arch": "x86_64"}) {
		t.Errorf("x86_64 should match")
	}
	if !branchMatchesTuple(b, map[string]string{"target_arch": "aarch64"}) {
		t.Errorf("aarch64 should match")
	}
	if branchMatchesTuple(b, map[string]string{"target_arch": "ppc64le"}) {
		t.Errorf("ppc64le should not match")
	}
	if branchMatchesTuple(b, map[string]string{}) {
		t.Errorf("empty tuple should not match a target_arch branch")
	}
	// Empty Varname (unrecognized expression) never matches.
	bad := conditionalBranch{Varname: "", Arches: []string{"x86_64"}}
	if branchMatchesTuple(bad, map[string]string{"target_arch": "x86_64"}) {
		t.Errorf("empty Varname should never match")
	}
}
