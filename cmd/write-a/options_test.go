package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderOptionsBuild_StringFlagsAndExclusion(t *testing.T) {
	options := map[string]bstOption{
		"prod_keys": {
			Type:        "bool",
			Description: "Use production keys",
			Variable:    "prod_keys",
			Default:     yaml.Node{Kind: yaml.ScalarNode, Value: "False"},
		},
		"snap_grade": {
			Type:        "enum",
			Description: "Grade",
			Variable:    "snap_grade",
			Values:      []string{"devel", "stable"},
			Default:     yaml.Node{Kind: yaml.ScalarNode, Value: "devel"},
		},
		// target_arch is excluded from the //options package; uses
		// @platforms//cpu:* instead.
		"target_arch": {
			Type:     "arch",
			Variable: "target_arch",
			Values:   []string{"x86_64", "aarch64"},
		},
	}
	got := renderOptionsBuild(options)
	for _, marker := range []string{
		`load("@bazel_skylib//rules:common_settings.bzl", "string_flag")`,
		`string_flag(`,
		`name = "prod_keys"`,
		`build_setting_default = "False"`,
		// bool gets ["True", "False"] auto-shaped values.
		`"True"`,
		`"False"`,
		// enum gets its declared values.
		`name = "snap_grade"`,
		`build_setting_default = "devel"`,
		`"devel"`,
		`"stable"`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("rendered //options/BUILD.bazel missing %q\n--body--\n%s", marker, got)
		}
	}
	// target_arch must NOT appear as a string_flag.
	if strings.Contains(got, `name = "target_arch"`) {
		t.Errorf("target_arch should be excluded from //options string_flag rendering:\n%s", got)
	}
}

func TestRenderOptionsBuild_FlagsTypeJoinsDefaults(t *testing.T) {
	options := map[string]bstOption{
		"minimal_vm": {
			Type:        "flags",
			Description: "Parts to include in minimal vm builds",
			Values:      []string{"firmware", "locale"},
			// flags-typed defaults are sequences in YAML; defaultAsString
			// joins with ",".
			Default: yaml.Node{
				Kind: yaml.SequenceNode,
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "locale"},
				},
			},
		},
	}
	got := renderOptionsBuild(options)
	if !strings.Contains(got, `build_setting_default = "locale"`) {
		t.Errorf("flags-typed default should join with comma; got:\n%s", got)
	}
}

func TestRenderOptionsBuild_EmptyOptionsHeaderOnly(t *testing.T) {
	// All options excluded from rendering → only the header
	// preamble survives (no string_flag rules emitted).
	options := map[string]bstOption{
		"target_arch": {Type: "arch", Variable: "target_arch", Values: []string{"x86_64"}},
	}
	got := renderOptionsBuild(options)
	if strings.Contains(got, "string_flag(") {
		t.Errorf("with only excluded options no string_flag should render; got:\n%s", got)
	}
	if !strings.Contains(got, `load("@bazel_skylib//rules:common_settings.bzl"`) {
		t.Errorf("header preamble (load + package()) should still render; got:\n%s", got)
	}
}

func TestDefaultAsString(t *testing.T) {
	cases := []struct {
		name string
		node yaml.Node
		want string
	}{
		{"zero", yaml.Node{}, ""},
		{"scalar", yaml.Node{Kind: yaml.ScalarNode, Value: "devel"}, "devel"},
		{"sequence", yaml.Node{
			Kind: yaml.SequenceNode,
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "a"},
				{Kind: yaml.ScalarNode, Value: "b"},
			},
		}, "a,b"},
		{"empty-sequence", yaml.Node{Kind: yaml.SequenceNode}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultAsString(tc.node); got != tc.want {
				t.Errorf("defaultAsString: got %q, want %q", got, tc.want)
			}
		})
	}
}
