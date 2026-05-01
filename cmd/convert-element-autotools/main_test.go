package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sstriker/cmake-to-bazel/internal/manifest"
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

// TestClassifyArgv_Compile / Link / Archive cover the four
// invocation shapes we recognize. Compile and link branch on
// `-c`; archive branches on `ar`'s mode-flag arg.
func TestClassifyArgv(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		ok   bool
		want Event
	}{
		{
			"compile-only",
			[]string{"cc", "-c", "-O2", "-o", "foo.o", "foo.c"},
			true,
			Event{Kind: EventCompile, Out: "foo.o", Srcs: []string{"foo.c"}, Copts: []string{"-O2"}},
		},
		{
			"compile-and-link greet-style",
			[]string{"cc", "-O2", "-o", "greet", "greet.c"},
			true,
			Event{Kind: EventLink, Out: "greet", Srcs: []string{"greet.c"}, Copts: []string{"-O2"}},
		},
		{
			"link-only with -l",
			[]string{"cc", "-O2", "-o", "myapp", "myapp.o", "-L.", "-lfoo"},
			true,
			Event{Kind: EventLink, Out: "myapp", Objs: []string{"myapp.o"}, Libs: []string{"foo"}, Copts: []string{"-O2"}},
		},
		{
			"archive ar rcs",
			[]string{"ar", "rcs", "libfoo.a", "foo.o", "bar.o"},
			true,
			Event{Kind: EventArchive, Out: "libfoo.a", Objs: []string{"foo.o", "bar.o"}},
		},
		{
			"gcc-internal cc1 (filtered)",
			[]string{"/usr/libexec/gcc/x86_64-linux-gnu/13/cc1", "-quiet", "x.c"},
			false, Event{},
		},
		{
			"ar list-mode (filtered: not r/q)",
			[]string{"ar", "t", "libfoo.a"},
			false, Event{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := classifyArgv(c.argv)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !c.ok {
				return
			}
			if got.Kind != c.want.Kind {
				t.Errorf("Kind = %d, want %d", got.Kind, c.want.Kind)
			}
			if got.Out != c.want.Out {
				t.Errorf("Out = %q, want %q", got.Out, c.want.Out)
			}
			if !reflect.DeepEqual(got.Srcs, c.want.Srcs) {
				t.Errorf("Srcs = %#v, want %#v", got.Srcs, c.want.Srcs)
			}
			if !reflect.DeepEqual(got.Objs, c.want.Objs) {
				t.Errorf("Objs = %#v, want %#v", got.Objs, c.want.Objs)
			}
			if !reflect.DeepEqual(got.Libs, c.want.Libs) {
				t.Errorf("Libs = %#v, want %#v", got.Libs, c.want.Libs)
			}
			if !reflect.DeepEqual(got.Copts, c.want.Copts) {
				t.Errorf("Copts = %#v, want %#v", got.Copts, c.want.Copts)
			}
		})
	}
}

// TestCorrelate_LibAndApp exercises the full correlation
// pipeline for an autotools project that produces a static
// library + a binary linking against it. Three compile events
// (foo.c → foo.o, bar.c → bar.o, myapp.c → myapp.o), one
// archive (libfoo.a from foo.o + bar.o), one link (myapp from
// myapp.o + -lfoo).
func TestCorrelate_LibAndApp(t *testing.T) {
	events := []Event{
		{Kind: EventCompile, Out: "foo.o", Srcs: []string{"foo.c"}, Copts: []string{"-O2"}},
		{Kind: EventCompile, Out: "bar.o", Srcs: []string{"bar.c"}, Copts: []string{"-O2"}},
		{Kind: EventCompile, Out: "myapp.o", Srcs: []string{"myapp.c"}, Copts: []string{"-O2"}},
		{Kind: EventArchive, Out: "libfoo.a", Objs: []string{"foo.o", "bar.o"}},
		{Kind: EventLink, Out: "myapp", Objs: []string{"myapp.o"}, Libs: []string{"foo"}, Copts: []string{"-O2"}},
	}
	got := emitBuild(correlate(events), nil)

	for _, marker := range []string{
		`load("@rules_cc//cc:defs.bzl", "cc_binary", "cc_library")`,
		`cc_library(`,
		`name = "foo"`,
		`srcs = ["bar.c", "foo.c"]`,
		`copts = ["-O2"]`,
		`linkstatic = True`,
		`cc_binary(`,
		`name = "myapp"`,
		`srcs = ["myapp.c"]`,
		`deps = [":foo"]`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("missing marker %q\n--body--\n%s", marker, got)
		}
	}
	// Library should come before binary (sort: cc_library < cc_binary
	// by alphabetical name happens to also satisfy "libs first").
	libIdx := strings.Index(got, `cc_library(`)
	binIdx := strings.Index(got, `cc_binary(`)
	if libIdx < 0 || binIdx < 0 || libIdx > binIdx {
		t.Errorf("expected cc_library before cc_binary; lib=%d bin=%d", libIdx, binIdx)
	}
}

// TestCorrelate_GreetStandalone covers the original spike's
// shape: a single compile-and-link invocation without any
// archives. Falls through to the EventLink path with srcs
// directly listed.
func TestCorrelate_GreetStandalone(t *testing.T) {
	events := []Event{
		{Kind: EventLink, Out: "greet", Srcs: []string{"greet.c"}, Copts: []string{"-O2"}},
	}
	got := emitBuild(correlate(events), nil)
	for _, marker := range []string{
		`load("@rules_cc//cc:defs.bzl", "cc_binary")`,
		`cc_binary(`,
		`name = "greet"`,
		`srcs = ["greet.c"]`,
		`copts = ["-O2"]`,
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("missing marker %q\n--body--\n%s", marker, got)
		}
	}
	// No cc_library involved → no linkstatic.
	if strings.Contains(got, `linkstatic`) {
		t.Errorf("greet-only render should not include linkstatic")
	}
}

// TestEmitBuild_ImportsManifestFallback covers the
// cross-element dep edge path: a link command's `-l<name>`
// that doesn't match an in-trace archive falls back to the
// imports manifest's LookupLinkLibrary, mapping `-lz` →
// `//elements/zlib:zlib`. Mirrors lower's cmake STATIC
// IMPORTED dep recovery shape.
func TestEmitBuild_ImportsManifestFallback(t *testing.T) {
	tmp := t.TempDir()
	mf := filepath.Join(tmp, "imports.json")
	if err := os.WriteFile(mf, []byte(`{
  "version": 1,
  "elements": [{
    "name": "zlib",
    "exports": [{
      "cmake_target": "ZLIB::ZLIB",
      "bazel_label": "//elements/zlib:zlib",
      "link_libraries": ["z"]
    }]
  }]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	imports, err := manifest.Load(mf)
	if err != nil {
		t.Fatal(err)
	}

	events := []Event{
		{Kind: EventLink, Out: "myapp", Srcs: []string{"myapp.c"}, Libs: []string{"z"}, Copts: []string{"-O2"}},
	}
	got := emitBuild(correlate(events), imports)
	if !strings.Contains(got, `deps = ["//elements/zlib:zlib"]`) {
		t.Errorf("expected deps to resolve -lz via manifest:\n%s", got)
	}

	// Negative check: nil manifest (no fallback) drops the
	// unresolved -lz silently.
	got2 := emitBuild(correlate(events), nil)
	if strings.Contains(got2, "deps") {
		t.Errorf("nil manifest should not produce deps; got:\n%s", got2)
	}
}

// TestStripLibPrefixSuffix covers the lib<name>.a → <name>
// conversion used to (a) name the cc_library rule and
// (b) match -l<name> link flags.
func TestStripLibPrefixSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"libfoo.a", "foo"},
		{"./libfoo.a", "foo"},
		{"build/libfoo.a", "foo"},
		{"lib.a", ""},
		{"foo.a", "foo"}, // no `lib` prefix: leave name intact
	}
	for _, c := range cases {
		if got := stripLibPrefixSuffix(c.in); got != c.want {
			t.Errorf("stripLibPrefixSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
