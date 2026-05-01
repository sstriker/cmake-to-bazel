// convert-element-autotools is the spike implementation of the
// B→A trace-driven autotools-to-Bazel converter described in the
// design discussion. Round 1 of building a kind:autotools element
// in project B runs the build under a process tracer; the trace
// gets registered (CAS-keyed by srckey) and read back by project
// A's render in round 2. With the trace in hand, the autotools
// element converts to native cc_library / cc_binary targets just
// like cmake elements do, instead of the opaque install_tree.tar
// genrule.
//
// Spike scope (intentionally narrow to validate the shape):
//   - Input: a strace text-format trace file (see --trace).
//   - Trace event filter: top-level compiler driver execve calls
//     (cc / gcc / g++ / clang / c++). gcc-internal sub-invocations
//     (cc1 / as / collect2 / ld) are noise the driver re-emits
//     in a portable form; skip them.
//   - Conversion: single-step compile-and-link invocations with
//     both sources and a `-o <output>` flag map to one cc_binary.
//     Other shapes (compile-only `-c` to a .o, link-from-objects,
//     archive via `ar`, install-only commands, …) are deferred —
//     they need cross-event correlation that's out of scope for
//     this spike.
//   - Output: BUILD.bazel.out; just one cc_binary rule per
//     compile-and-link command, named by the `-o` argument's
//     basename.
//
// Once this end-to-end shape is validated against the
// autotools-greet fixture, follow-up work expands the converter
// to handle compile-link-archive correlation, cross-element dep
// edges, and the registry-keyed CAS lookup write-a needs to
// gate the coarse-vs-native branch.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	tracePath := flag.String("trace", "", "path to strace text-format output (`-f -e trace=execve -s 4096 -o <path>`)")
	outBuild := flag.String("out-build", "", "path to write BUILD.bazel.out")
	flag.Parse()

	if *tracePath == "" || *outBuild == "" {
		fmt.Fprintln(os.Stderr, "convert-element-autotools: --trace and --out-build are required")
		os.Exit(2)
	}

	traceFile, err := os.Open(*tracePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "convert-element-autotools: open trace: %v\n", err)
		os.Exit(1)
	}
	defer traceFile.Close()

	var binaries []ccBinary
	scanner := bufio.NewScanner(traceFile)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // strace lines can be long
	for scanner.Scan() {
		line := scanner.Text()
		argv, ok := parseExecveLine(line)
		if !ok {
			continue
		}
		if !isUserCompilerDriver(argv) {
			continue
		}
		bin, ok := classifyCompileLink(argv)
		if !ok {
			continue
		}
		binaries = append(binaries, bin)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "convert-element-autotools: scan trace: %v\n", err)
		os.Exit(1)
	}

	out := emitBuild(binaries)
	if err := os.MkdirAll(filepath.Dir(*outBuild), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "convert-element-autotools: mkdir: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outBuild, []byte(out), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "convert-element-autotools: write: %v\n", err)
		os.Exit(1)
	}
}

// ccBinary captures the spike-shape native target derived from
// a compile-and-link invocation. Populated by classifyCompileLink
// and emitted by emitBuild.
type ccBinary struct {
	Name  string   // basename of -o argument
	Srcs  []string // positional source-file args (.c/.cc/.cpp/.cxx)
	Copts []string // every other flag, sorted, deduped
}

// parseExecveLine returns the argv from an strace `execve(...)` line
// when the call succeeded. Lines from `strace -f -e trace=execve`
// look like:
//
//	1234  execve("/usr/bin/cc", ["cc", "-O2", "-o", "greet", "greet.c"], 0x... /* N vars */) = 0
//
// We tolerate the optional PID prefix and reject failed execves
// (return value != 0) as well as `<unfinished ...>` continuation
// fragments.
func parseExecveLine(line string) ([]string, bool) {
	idx := strings.Index(line, "execve(")
	if idx < 0 {
		return nil, false
	}
	// Trailing `= 0` (or other return code). Bail on failures and
	// on the unfinished-call resumption fragments strace emits when
	// other syscalls interleave.
	tail := line[len(line)-12:]
	if !strings.Contains(tail, "= 0") {
		return nil, false
	}
	if strings.Contains(line, "<unfinished") || strings.Contains(line, "resumed>") {
		return nil, false
	}
	rest := line[idx+len("execve("):]
	// First arg is the path string; we don't actually need it
	// because argv[0] in the array carries the basename. Skip past
	// the closing `"` and the comma.
	pathEnd := indexAfterQuoted(rest, 0)
	if pathEnd < 0 {
		return nil, false
	}
	rest = rest[pathEnd+1:] // skip closing quote
	rest = strings.TrimLeft(rest, ", ")
	if !strings.HasPrefix(rest, "[") {
		return nil, false
	}
	rest = rest[1:]
	end := indexUnescapedClose(rest, ']')
	if end < 0 {
		return nil, false
	}
	argvBlock := rest[:end]
	return parseArgvBlock(argvBlock), true
}

// indexAfterQuoted returns the index of the closing double-quote
// of a strace-style quoted string starting at start. strace
// escapes embedded quotes / backslashes / non-printables;
// supports the few we encounter in argv.
func indexAfterQuoted(s string, start int) int {
	if start >= len(s) || s[start] != '"' {
		return -1
	}
	i := start + 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i
		}
		i++
	}
	return -1
}

// indexUnescapedClose returns the index of the first close
// character at top level (not inside a quoted string).
func indexUnescapedClose(s string, close byte) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			j := indexAfterQuoted(s, i)
			if j < 0 {
				return -1
			}
			i = j
		case close:
			return i
		}
	}
	return -1
}

// parseArgvBlock splits the inside-the-brackets text of strace's
// argv into individual unquoted strings.
func parseArgvBlock(block string) []string {
	var out []string
	i := 0
	for i < len(block) {
		// skip whitespace + commas
		for i < len(block) && (block[i] == ' ' || block[i] == ',') {
			i++
		}
		if i >= len(block) {
			break
		}
		if block[i] != '"' {
			break
		}
		end := indexAfterQuoted(block, i)
		if end < 0 {
			break
		}
		raw := block[i+1 : end]
		out = append(out, unescapeStraceString(raw))
		i = end + 1
	}
	return out
}

// unescapeStraceString reverses strace's argv-quoting. Handles
// the small set of escapes we see in compiler argv (\\, \", \n,
// \t, \r, octal \NNN). Unknown escapes pass through as the
// escaped character.
func unescapeStraceString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		next := s[i]
		switch next {
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(next)
		}
	}
	return b.String()
}

// isUserCompilerDriver reports whether argv is a top-level
// compiler driver invocation. Filters out gcc-internal cc1 / as
// / collect2 / ld sub-processes which strace also captures but
// which we treat as implementation details of the user-facing
// driver invocation.
func isUserCompilerDriver(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	bin := filepath.Base(argv[0])
	switch bin {
	case "cc", "gcc", "g++", "clang", "clang++", "c++", "cxx":
		return true
	}
	return false
}

// classifyCompileLink walks argv to decide whether it's a
// single-step compile-and-link invocation we know how to lower
// to cc_binary. Returns false for compile-only (`-c`) invocations
// and for invocations missing either srcs or `-o`.
func classifyCompileLink(argv []string) (ccBinary, bool) {
	var srcs []string
	var copts []string
	output := ""
	compileOnly := false
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-c":
			compileOnly = true
		case a == "-o" && i+1 < len(argv):
			output = argv[i+1]
			i++
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			output = a[2:]
		case strings.HasSuffix(a, ".c"),
			strings.HasSuffix(a, ".cc"),
			strings.HasSuffix(a, ".cpp"),
			strings.HasSuffix(a, ".cxx"),
			strings.HasSuffix(a, ".c++"),
			strings.HasSuffix(a, ".C"):
			srcs = append(srcs, a)
		default:
			if strings.HasPrefix(a, "-") {
				copts = append(copts, a)
			}
		}
	}
	if compileOnly || output == "" || len(srcs) == 0 {
		return ccBinary{}, false
	}
	sort.Strings(copts)
	copts = dedup(copts)
	sort.Strings(srcs)
	return ccBinary{
		Name:  filepath.Base(output),
		Srcs:  srcs,
		Copts: copts,
	}, true
}

func dedup(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := []string{in[0]}
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// emitBuild renders the spike's BUILD.bazel.out — a header
// comment plus one cc_binary per recovered compile-and-link
// invocation. Stable ordering by Name so reruns of the same
// trace produce byte-identical output.
func emitBuild(bins []ccBinary) string {
	if len(bins) == 0 {
		return "# Generated by convert-element-autotools. DO NOT EDIT.\n# (no compile-and-link invocations recovered from trace)\n"
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].Name < bins[j].Name })

	var b strings.Builder
	b.WriteString("# Generated by convert-element-autotools. DO NOT EDIT.\n\n")
	b.WriteString(`load("@rules_cc//cc:defs.bzl", "cc_binary")` + "\n")
	for _, bin := range bins {
		b.WriteString("\n")
		fmt.Fprintf(&b, "cc_binary(\n    name = %q,\n", bin.Name)
		fmt.Fprintf(&b, "    srcs = %s,\n", strList(bin.Srcs))
		if len(bin.Copts) > 0 {
			fmt.Fprintf(&b, "    copts = %s,\n", strList(bin.Copts))
		}
		b.WriteString("    visibility = [\"//visibility:public\"],\n")
		b.WriteString(")\n")
	}
	return b.String()
}

func strList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
