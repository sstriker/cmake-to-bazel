package shadow

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// TraceEvent is one record from `cmake --trace-expand --trace-format=json-v1`.
// We deliberately decode only the fields we read; cmake adds more.
type TraceEvent struct {
	File string   `json:"file"`
	Line int      `json:"line"`
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

// ExtractReadPaths returns every source-tree path that the trace shows being
// read (file content actually consumed, not merely referenced). Paths are
// returned package-relative (slash form) and deduplicated.
//
// Recognized read-causing commands:
//   - include(<file>)            : args[0]
//   - configure_file(<in> <out>) : args[0]
//   - file(READ|STRINGS|MD5|SHA*) : args[1]
//
// Anything outside sourceRoot, generated, or unresolvable is silently dropped
// (cmake's own bundled modules under /usr/share/cmake-* show up here and are
// not source-tree files).
func ExtractReadPaths(traceRaw []byte, sourceRoot string) []string {
	seen := map[string]struct{}{}
	for _, line := range bytes.Split(traceRaw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev TraceEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		path := readPathFor(ev)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(ev.File), path)
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			continue
		}
		if rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		seen[filepath.ToSlash(rel)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readPathFor(ev TraceEvent) string {
	switch strings.ToLower(ev.Cmd) {
	case "include":
		if len(ev.Args) > 0 {
			return ev.Args[0]
		}
	case "configure_file":
		if len(ev.Args) > 0 {
			return ev.Args[0]
		}
	case "file":
		if len(ev.Args) >= 2 {
			switch strings.ToUpper(ev.Args[0]) {
			case "READ", "STRINGS", "MD5", "SHA1", "SHA224", "SHA256", "SHA384", "SHA512":
				return ev.Args[1]
			}
		}
	}
	return ""
}
