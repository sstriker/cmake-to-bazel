package main

import (
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
	out, remaining := foldStaticConditionals(vars, branches, staticVars)
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
