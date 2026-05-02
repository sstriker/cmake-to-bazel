package main

import "testing"

func TestParseArchExpression(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want []string
	}{
		{"equals-x86_64", `target_arch == "x86_64"`, []string{"x86_64"}},
		{"equals-single-quote", `target_arch == 'aarch64'`, []string{"aarch64"}},
		{"equals-unsupported-arch", `target_arch == "weird-arch"`, nil},
		{"or-chain", `target_arch == "x86_64" or target_arch == "aarch64"`, []string{"x86_64", "aarch64"}},
		{"in-tuple", `target_arch in ("ppc64le", "ppc64")`, []string{"ppc64le"}},
		{"in-tuple-three", `target_arch in ("x86_64", "aarch64", "riscv64")`, []string{"x86_64", "aarch64", "riscv64"}},
		{"not-equals", `target_arch != "x86_64"`,
			[]string{"aarch64", "i686", "ppc64le", "riscv64", "loongarch64"}},
		{"unrecognized", `target_arch matches /pattern/`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseArchExpression(tc.expr)
			if len(got) != len(tc.want) {
				t.Fatalf("length mismatch\n got: %v\nwant: %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
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
