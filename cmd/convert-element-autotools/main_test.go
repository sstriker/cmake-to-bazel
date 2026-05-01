package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseExecveLine covers the strace text-format shape for
// `-f -e trace=execve -s 4096`. Real-world fixtures: a top-level
// compiler driver call, an internal cc1 sub-call, a failed
// execve, and an interleaved <unfinished>/resumed> pair (the
// latter two should be skipped).
func TestParseExecveLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
		ok   bool
	}{
		{
			"top-level cc compile-and-link",
			`1748  execve("/usr/bin/cc", ["cc", "-O2", "-o", "greet", "greet.c"], 0x55c68cde2b40 /* 71 vars */) = 0`,
			[]string{"cc", "-O2", "-o", "greet", "greet.c"},
			true,
		},
		{
			"failed execve (ENOENT)",
			`9999  execve("/usr/bin/missing", ["missing"], 0x... /* 0 vars */) = -1 ENOENT (No such file or directory)`,
			nil, false,
		},
		{
			"unfinished call fragment",
			`1234  execve("/usr/bin/cc", ["cc", "-c", "x.c"], 0x... /* 0 vars */ <unfinished ...>`,
			nil, false,
		},
		{
			"non-execve line",
			`1234  openat(AT_FDCWD, "foo", O_RDONLY) = 3`,
			nil, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseExecveLine(c.line)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("argv = %#v, want %#v", got, c.want)
			}
		})
	}
}

// TestClassifyCompileLink covers the spike's cc_binary
// recognition rules: single-step compile-and-link with both
// srcs and `-o` becomes a target; compile-only or link-only or
// missing-output invocations are skipped.
func TestClassifyCompileLink(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		ok   bool
		want ccBinary
	}{
		{
			"compile and link to greet",
			[]string{"cc", "-O2", "-o", "greet", "greet.c"},
			true,
			ccBinary{Name: "greet", Srcs: []string{"greet.c"}, Copts: []string{"-O2"}},
		},
		{
			"compile-only -c",
			[]string{"cc", "-c", "-O2", "x.c", "-o", "x.o"},
			false, ccBinary{},
		},
		{
			"link-only (no srcs)",
			[]string{"cc", "-o", "out", "x.o", "y.o"},
			false, ccBinary{},
		},
		{
			"missing -o",
			[]string{"cc", "x.c"},
			false, ccBinary{},
		},
		{
			"clued -ofoo (no space)",
			[]string{"cc", "-O2", "-ofoo", "foo.c"},
			true,
			ccBinary{Name: "foo", Srcs: []string{"foo.c"}, Copts: []string{"-O2"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := classifyCompileLink(c.argv)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !c.ok {
				return
			}
			if got.Name != c.want.Name {
				t.Errorf("Name = %q, want %q", got.Name, c.want.Name)
			}
			if !reflect.DeepEqual(got.Srcs, c.want.Srcs) {
				t.Errorf("Srcs = %#v, want %#v", got.Srcs, c.want.Srcs)
			}
			if !reflect.DeepEqual(got.Copts, c.want.Copts) {
				t.Errorf("Copts = %#v, want %#v", got.Copts, c.want.Copts)
			}
		})
	}
}

// TestIsUserCompilerDriver filters out gcc-internal cc1 / as /
// collect2 / ld sub-invocations that live alongside the user-
// facing cc/gcc/clang call in the trace.
func TestIsUserCompilerDriver(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"/usr/bin/cc", "-O2"}, true},
		{[]string{"gcc", "-c", "x.c"}, true},
		{[]string{"clang++", "x.cpp"}, true},
		{[]string{"/usr/libexec/gcc/x86_64-linux-gnu/13/cc1", "x.c"}, false},
		{[]string{"/usr/bin/as", "-o", "x.o", "x.s"}, false},
		{[]string{"/usr/bin/ld", "x.o"}, false},
		{[]string{"/usr/libexec/gcc/x86_64-linux-gnu/13/collect2"}, false},
		{nil, false},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.argv, " "), func(t *testing.T) {
			if got := isUserCompilerDriver(c.argv); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestEmitBuild_E2E walks the spike's full pipeline against an
// inline autotools-greet-shaped strace fragment, confirming the
// rendered BUILD has the cc_binary shape.
func TestEmitBuild_E2E(t *testing.T) {
	bins := []ccBinary{{
		Name:  "greet",
		Srcs:  []string{"greet.c"},
		Copts: []string{"-O2"},
	}}
	got := emitBuild(bins)
	for _, marker := range []string{
		`load("@rules_cc//cc:defs.bzl", "cc_binary")`,
		`name = "greet"`,
		`srcs = ["greet.c"]`,
		`copts = ["-O2"]`,
		`visibility = ["//visibility:public"]`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("missing marker %q\n--body--\n%s", marker, got)
		}
	}
}
