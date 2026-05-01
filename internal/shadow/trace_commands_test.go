package shadow

import (
	"strings"
	"testing"
)

// Trace-event fixtures are constructed inline so the test
// asserts the parser's behaviour independent of cmake's
// version-specific wire format. Each line is a real
// JSON-v1 trace event — one fewer integration with cmake's
// recording at test time.

const traceMixed = `{"args":["t","PUBLIC","inc","PRIVATE","inc/priv"],"cmd":"target_include_directories","file":"/src/CMakeLists.txt","line":4}
{"args":["cmTC_xxx","libfake"],"cmd":"target_link_libraries","file":"/build/CMakeFiles/CMakeScratch/TryCompile-1/CMakeLists.txt","line":1}
{"args":["t","PUBLIC","ZLIB::ZLIB","PRIVATE","privatedep"],"cmd":"target_link_libraries","file":"/src/CMakeLists.txt","line":6}
{"args":["t2","libfoo","libbar"],"cmd":"target_link_libraries","file":"/src/sub/CMakeLists.txt","line":3}
{"args":["in.h.in","out.h","@ONLY"],"cmd":"configure_file","file":"/src/CMakeLists.txt","line":7}
{"args":["/usr/share/cmake-3.28/Modules/CMakeSystem.cmake.in","/build/CMakeFiles/3.28.3/CMakeSystem.cmake","@ONLY"],"cmd":"configure_file","file":"/usr/share/cmake-3.28/Modules/CMakeDetermineSystem.cmake","line":246}
`

func TestExtractTargetIncludes(t *testing.T) {
	got := ExtractTargetIncludes([]byte(traceMixed), "/src", nil)
	if len(got) != 1 {
		t.Fatalf("want 1 user call, got %d (%+v)", len(got), got)
	}
	c := got[0]
	if c.Target != "t" {
		t.Errorf("target: %q want t", c.Target)
	}
	if len(c.Groups) != 2 {
		t.Fatalf("groups: %+v want 2", c.Groups)
	}
	if c.Groups[0].Visibility != "PUBLIC" || c.Groups[0].Dirs[0] != "inc" {
		t.Errorf("group 0: %+v", c.Groups[0])
	}
	if c.Groups[1].Visibility != "PRIVATE" || c.Groups[1].Dirs[0] != "inc/priv" {
		t.Errorf("group 1: %+v", c.Groups[1])
	}
}

func TestExtractTargetLinks_PublicPrivate(t *testing.T) {
	got := ExtractTargetLinks([]byte(traceMixed), "/src", nil)
	// 2 user calls: t (with PUBLIC/PRIVATE), t2 (legacy
	// positional). The cmTC_xxx scratch-target call is filtered
	// out (file is in build dir, not source).
	if len(got) != 2 {
		t.Fatalf("want 2 calls; got %d (%+v)", len(got), got)
	}
	tCall := got[0]
	if tCall.Target != "t" {
		t.Errorf("target 0: %q want t", tCall.Target)
	}
	if len(tCall.Groups) != 2 {
		t.Fatalf("t groups: %+v", tCall.Groups)
	}
	if tCall.Groups[0].Visibility != "PUBLIC" || tCall.Groups[0].Libs[0] != "ZLIB::ZLIB" {
		t.Errorf("t public: %+v", tCall.Groups[0])
	}
	if tCall.Groups[1].Visibility != "PRIVATE" || tCall.Groups[1].Libs[0] != "privatedep" {
		t.Errorf("t private: %+v", tCall.Groups[1])
	}

	t2Call := got[1]
	if t2Call.Target != "t2" {
		t.Errorf("target 1: %q want t2", t2Call.Target)
	}
	if len(t2Call.Groups) != 1 || t2Call.Groups[0].Visibility != "" {
		t.Errorf("t2 should be one unkeyed group; got %+v", t2Call.Groups)
	}
	if len(t2Call.Groups[0].Libs) != 2 ||
		t2Call.Groups[0].Libs[0] != "libfoo" || t2Call.Groups[0].Libs[1] != "libbar" {
		t.Errorf("t2 libs: %+v", t2Call.Groups[0].Libs)
	}
}

func TestExtractConfigureFiles_FiltersCmakeInternal(t *testing.T) {
	got := ExtractConfigureFiles([]byte(traceMixed), "/src")
	if len(got) != 1 {
		t.Fatalf("want 1 user configure_file (cmake-internal one filtered); got %d (%+v)", len(got), got)
	}
	c := got[0]
	if c.Input != "in.h.in" || c.Output != "out.h" {
		t.Errorf("input/output: %+v", c)
	}
	if len(c.Options) != 1 || c.Options[0] != "@ONLY" {
		t.Errorf("options: %+v", c.Options)
	}
}

func TestExtractTargetIncludes_SystemAndOrder(t *testing.T) {
	// SYSTEM + BEFORE + visibility — the order keywords prefix
	// the visibility group.
	trace := `{"args":["t","SYSTEM","BEFORE","INTERFACE","sys/inc"],"cmd":"target_include_directories","file":"/src/CMakeLists.txt","line":1}
`
	got := ExtractTargetIncludes([]byte(trace), "/src", nil)
	if len(got) != 1 || len(got[0].Groups) != 1 {
		t.Fatalf("got %+v", got)
	}
	g := got[0].Groups[0]
	if g.Visibility != "INTERFACE" || !g.System || g.Order != "BEFORE" {
		t.Errorf("group: %+v want SYSTEM BEFORE INTERFACE", g)
	}
}

func TestExtractTargetIncludes_PositionalDirs(t *testing.T) {
	// Legacy pre-3.0 shape: bare positional dirs without a
	// visibility keyword — group as PRIVATE per cmake's
	// historical default.
	trace := `{"args":["t","inc1","inc2"],"cmd":"target_include_directories","file":"/src/CMakeLists.txt","line":1}
`
	got := ExtractTargetIncludes([]byte(trace), "/src", nil)
	if len(got) != 1 || len(got[0].Groups) != 1 {
		t.Fatalf("got %+v", got)
	}
	g := got[0].Groups[0]
	if g.Visibility != "PRIVATE" || len(g.Dirs) != 2 {
		t.Errorf("group: %+v", g)
	}
}

// TestExtractTargetLinks_KnownTargetsRescue covers the
// macro-from-import case: a producer element's .cmake module
// (outside the consumer source root) calls
// target_link_libraries on a consumer-defined target. The
// strict file-path filter would drop that call; the
// knownTargets second arm keeps it.
func TestExtractTargetLinks_KnownTargetsRescue(t *testing.T) {
	trace := `{"args":["consumer_target","PUBLIC","ZLIB::ZLIB"],"cmd":"target_link_libraries","file":"/opt/producer-modules/Helpers.cmake","line":3}
{"args":["producer_internal","libfoo"],"cmd":"target_link_libraries","file":"/opt/producer-modules/Helpers.cmake","line":7}
`
	if got := ExtractTargetLinks([]byte(trace), "/src", nil); len(got) != 0 {
		t.Errorf("nil knownTargets: want 0 calls, got %d (%+v)", len(got), got)
	}
	known := map[string]bool{"consumer_target": true}
	got := ExtractTargetLinks([]byte(trace), "/src", known)
	if len(got) != 1 || got[0].Target != "consumer_target" {
		t.Fatalf("with knownTargets: want 1 call on consumer_target, got %+v", got)
	}
	if len(got[0].Groups) != 1 || got[0].Groups[0].Libs[0] != "ZLIB::ZLIB" {
		t.Errorf("rescued call libs: %+v", got[0].Groups[0])
	}
}

func TestInSourceTree(t *testing.T) {
	cases := []struct {
		file, root string
		want       bool
	}{
		{"/src/CMakeLists.txt", "/src", true},
		{"/src/sub/CMakeLists.txt", "/src", true},
		{"/usr/share/cmake-3.28/Modules/Foo.cmake", "/src", false},
		{"/build/CMakeFiles/scratch/CMakeLists.txt", "/src", false},
		{"/src", "/src", true},        // edge: source root itself
		{"/src-other", "/src", false}, // edge: prefix that's not a directory boundary
		{"", "/src", false},
		{"/src/CMakeLists.txt", "", false},
	}
	for _, c := range cases {
		t.Run(c.file+"::"+c.root, func(t *testing.T) {
			if got := inSourceTree(c.file, c.root); got != c.want {
				t.Errorf("inSourceTree(%q, %q) = %v, want %v", c.file, c.root, got, c.want)
			}
		})
	}
}

// TestExtract_RealCmakeTrace walks a hand-curated subset of
// real cmake-3.28 trace lines (captured from the trace-test
// fixture) to make sure the parser handles cmake's actual
// wire format — extra fields (frame / global_frame / time /
// line_end), arg encoding, etc.
func TestExtract_RealCmakeTrace(t *testing.T) {
	real := strings.Join([]string{
		`{"args":["t","PUBLIC","inc","PRIVATE","inc/priv"],"cmd":"target_include_directories","file":"/src/CMakeLists.txt","frame":1,"global_frame":1,"line":4,"time":1777633549.355098}`,
		`{"args":["t","PUBLIC","ZLIB::ZLIB"],"cmd":"target_link_libraries","file":"/src/CMakeLists.txt","frame":1,"global_frame":1,"line":6,"time":1777633549.3724971}`,
		`{"args":["in.h.in","out.h","@ONLY"],"cmd":"configure_file","file":"/src/CMakeLists.txt","frame":1,"global_frame":1,"line":7,"line_end":7,"time":1777633549.3725619}`,
	}, "\n") + "\n"
	if len(ExtractTargetIncludes([]byte(real), "/src", nil)) != 1 {
		t.Errorf("missed target_include_directories with extra fields")
	}
	if len(ExtractTargetLinks([]byte(real), "/src", nil)) != 1 {
		t.Errorf("missed target_link_libraries with extra fields")
	}
	if len(ExtractConfigureFiles([]byte(real), "/src")) != 1 {
		t.Errorf("missed configure_file with extra fields")
	}
}
