package fidelity

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseNM_3FieldRows(t *testing.T) {
	body := `0000000000000000 T hello_message
0000000000000010 D global_data
                 U undefined_should_not_appear
`
	got, err := ParseNM([]byte(body))
	if err != nil {
		t.Fatalf("ParseNM: %v", err)
	}
	// `defined-only` is enforced on the nm side; here we just
	// observe parser behavior. The U line passes through because
	// our parser doesn't filter — but in practice nm wouldn't
	// emit it under --defined-only.
	if _, ok := got["hello_message"]; !ok {
		t.Errorf("missing hello_message: %v", got)
	}
	if got["hello_message"].Type != 'T' {
		t.Errorf("hello_message type = %c, want T", got["hello_message"].Type)
	}
	if _, ok := got["global_data"]; !ok {
		t.Errorf("missing global_data")
	}
}

func TestParseNM_2FieldRowsAndArchiveHeaders(t *testing.T) {
	body := `
hello.o:
T hello_message
T hello_helper

foo.o:
T foo_one
`
	got, err := ParseNM([]byte(body))
	if err != nil {
		t.Fatalf("ParseNM: %v", err)
	}
	for _, want := range []string{"hello_message", "hello_helper", "foo_one"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	// Archive header line "hello.o:" must NOT be parsed as a
	// symbol named "hello.o:".
	for name := range got {
		if strings.HasSuffix(name, ":") {
			t.Errorf("archive header leaked as symbol: %q", name)
		}
	}
}

func TestParseNM_BlankLinesAndMalformedLinesAreSkipped(t *testing.T) {
	body := `

0000000000000000 T good_one

malformed line with too many words and no recognizable type at field 1

`
	got, err := ParseNM([]byte(body))
	if err != nil {
		t.Fatalf("ParseNM: %v", err)
	}
	if _, ok := got["good_one"]; !ok {
		t.Errorf("good_one not parsed: %v", got)
	}
	if len(got) != 1 {
		t.Errorf("got %d symbols, want 1; %v", len(got), got)
	}
}

func TestDiffSymbols_LeftOnlyAndRightOnly(t *testing.T) {
	left := map[string]Symbol{
		"shared":    {Name: "shared"},
		"left_only": {Name: "left_only"},
	}
	right := map[string]Symbol{
		"shared":     {Name: "shared"},
		"right_only": {Name: "right_only"},
	}
	got := DiffSymbols(left, right)
	if !reflect.DeepEqual(got.LeftOnly, []string{"left_only"}) {
		t.Errorf("LeftOnly = %v, want [left_only]", got.LeftOnly)
	}
	if !reflect.DeepEqual(got.RightOnly, []string{"right_only"}) {
		t.Errorf("RightOnly = %v, want [right_only]", got.RightOnly)
	}
	if got.Empty() {
		t.Errorf("Empty() = true, want false")
	}
}

func TestDiffSymbols_EmptyOnIdenticalSets(t *testing.T) {
	s := map[string]Symbol{
		"a": {Name: "a"},
		"b": {Name: "b"},
	}
	d := DiffSymbols(s, s)
	if !d.Empty() {
		t.Errorf("Empty() = false on identical sets: %v", d)
	}
	if got := d.Format(); got != "no differences" {
		t.Errorf("Format() = %q", got)
	}
}

func TestDiffSymbols_TypeChangesNotReported(t *testing.T) {
	// Same name, different type — symbol-tier ignores; only
	// reported if a future Type-aware tier is added.
	left := map[string]Symbol{"x": {Name: "x", Type: 'T'}}
	right := map[string]Symbol{"x": {Name: "x", Type: 'D'}}
	d := DiffSymbols(left, right)
	if !d.Empty() {
		t.Errorf("type-only differences should not surface in Empty(); got %v", d)
	}
}

func TestSymbolDiff_FormatOrdersLeftThenRight(t *testing.T) {
	d := SymbolDiff{
		LeftOnly:  []string{"only_in_cmake"},
		RightOnly: []string{"only_in_bazel"},
	}
	got := d.Format()
	cIdx := strings.Index(got, "only_in_cmake")
	bIdx := strings.Index(got, "only_in_bazel")
	if cIdx < 0 || bIdx < 0 {
		t.Fatalf("Format missing expected names: %q", got)
	}
	if cIdx > bIdx {
		t.Errorf("LeftOnly should appear before RightOnly:\n%s", got)
	}
	if !strings.Contains(got, "cmake-only") {
		t.Errorf("LeftOnly header missing 'cmake-only' phrase: %s", got)
	}
}
