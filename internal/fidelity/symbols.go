// Package fidelity diffs cmake-built artifacts against bazel-built
// artifacts produced by the converter. Used by the e2e fidelity
// gate to assert that "the converter says it converted X" actually
// means "the bazel-built X behaves the same as the cmake-built X".
//
// Three diff tiers, each progressively stricter — symbols.go is
// the first and load-bearing one:
//
//   - Symbol tier:     same `nm --defined-only` symbol set.
//   - Behavioral tier: same (exit code, stdout, stderr) for runs
//                      with identical input. (behavior.go)
//   - Byte tier:       stripped artifacts byte-identical
//                      (informational only; build-id and embedded
//                      paths typically defeat it). (bytes.go)
//
// See docs/m5b-fidelity-plan.md for the surrounding milestone.
package fidelity

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Symbol is one defined symbol from `nm --defined-only` output.
// We retain the type code (T = text/code, D = initialized data,
// B = uninitialized data, R = read-only data, ...) so the diff
// can distinguish "missing function" from "missing global" if a
// future tier needs that detail. Callers default to comparing on
// Name only.
type Symbol struct {
	Type byte
	Name string
}

// SymbolSet returns the set of defined symbols in path, parsed
// from `nm --defined-only --no-sort <path>` output. Works on both
// static archives (.a) and shared objects (.so) — `nm` walks
// either.
//
// Calls out to the host's `nm`. Errors when nm isn't on PATH or
// the file isn't a recognizable object. Empty output (no defined
// symbols) returns an empty set, not an error — empty sets are
// valid (header-only libraries, etc.).
func SymbolSet(path string) (map[string]Symbol, error) {
	cmd := exec.Command("nm", "--defined-only", "--no-sort", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nm %s: %w\n%s", path, err, stderr.String())
	}
	return ParseNM(stdout.Bytes())
}

// ParseNM turns `nm --defined-only --no-sort` output into a map
// keyed by symbol name. Format expected:
//
//	<addr> <type> <name>
//	<type> <name>          (when nm omits address for some
//	                        symbols / formats)
//
// Lines that don't match either form are skipped silently — nm
// emits archive headers ("foo.a:", "hello.o:", blank lines)
// interleaved with symbol rows. Blank lines and headers are not
// symbols; we drop them.
func ParseNM(stdout []byte) (map[string]Symbol, error) {
	out := map[string]Symbol{}
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// Archive member headers like "libfoo.a:" or
		// "hello.o:" end in `:`. Skip.
		if strings.HasSuffix(line, ":") {
			continue
		}
		fields := strings.Fields(line)
		var typeChar byte
		var name string
		switch len(fields) {
		case 2:
			// `<type> <name>` (no address, common for U /
			// undefined; defined-only filter excludes those, but
			// also for archive members in some nm output forms).
			typeChar = fields[0][0]
			name = fields[1]
		case 3:
			// `<addr> <type> <name>`.
			if len(fields[1]) != 1 {
				continue
			}
			typeChar = fields[1][0]
			name = fields[2]
		default:
			continue
		}
		if name == "" {
			continue
		}
		out[name] = Symbol{Type: typeChar, Name: name}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SymbolDiff is the result of comparing two symbol sets.
type SymbolDiff struct {
	// LeftOnly is symbols present in the left set but not the
	// right. In fidelity tests, "left = cmake-built reference" and
	// LeftOnly means the converter dropped these symbols.
	LeftOnly []string

	// RightOnly is the inverse: symbols the converter added that
	// aren't in the cmake reference. Often points at extra
	// translation units or different compile flags exporting more.
	RightOnly []string
}

// Empty reports whether the diff is clean — no symbols on either
// side that aren't on the other.
func (d *SymbolDiff) Empty() bool {
	return len(d.LeftOnly) == 0 && len(d.RightOnly) == 0
}

// Format renders the diff as operator-readable prose. Empty diffs
// produce "no differences"; populated diffs list each side under a
// header.
func (d *SymbolDiff) Format() string {
	if d.Empty() {
		return "no differences"
	}
	var b strings.Builder
	if len(d.LeftOnly) > 0 {
		b.WriteString("missing in right (cmake-only):\n")
		for _, s := range d.LeftOnly {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}
	if len(d.RightOnly) > 0 {
		b.WriteString("extra in right (bazel-only):\n")
		for _, s := range d.RightOnly {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}
	return b.String()
}

// DiffSymbols computes the symmetric difference of two symbol sets
// keyed by name. Symbols whose Type differs but Name matches are
// NOT flagged as differences in this tier — type changes (T → D
// for a global moved into .data, etc.) usually reflect harmless
// implementation details. Operators who care add a separate Type-
// aware tier.
func DiffSymbols(left, right map[string]Symbol) SymbolDiff {
	var d SymbolDiff
	for name := range left {
		if _, ok := right[name]; !ok {
			d.LeftOnly = append(d.LeftOnly, name)
		}
	}
	for name := range right {
		if _, ok := left[name]; !ok {
			d.RightOnly = append(d.RightOnly, name)
		}
	}
	sort.Strings(d.LeftOnly)
	sort.Strings(d.RightOnly)
	return d
}
