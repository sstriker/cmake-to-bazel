package main

import (
	"bufio"
	"bytes"
	"strings"
)

// MakeDB is the parsed `make -np` output: every rule's target
// → prerequisites + recipe lines, plus the variable assignments
// in scope at print time. Captured post-build (after configure
// substitutions are baked into the Makefile and after make has
// already done all its work), so values are fully resolved.
//
// Today's converter records but doesn't consume MakeDB — it's
// plumbed through so future enhancements (Makefile-aware
// target naming, install-tree → typed-filegroup mapping,
// cross-validation of trace-recovered cc rules against
// Makefile-declared targets) have a structural data source.
//
// Parser scope (what we extract):
//   - Rules: lines like `target: prereq prereq ...` outside the
//     comment blocks, paired with the recipe lines that follow.
//   - Variables: lines like `VAR = value` at top level.
//
// Parser non-scope (silently dropped):
//   - Implicit-rule definitions, pattern rules, suffix rules.
//     These need pattern-vs-target distinction we don't yet
//     need.
//   - `# Files` / `# Variables` etc. section headers — we use
//     them only as section delimiters.
//   - Comments: discarded.
type MakeDB struct {
	// Rules maps a target name to its prereqs + recipe.
	// Multiple rules with the same target overwrite (matches
	// make's last-rule-wins semantics for explicit rules).
	Rules map[string]MakeRule
	// Variables maps a variable name to its fully-resolved
	// value. `VAR = ...`, `VAR := ...`, `VAR ::= ...`, etc.
	// all collapse to the same map; conditional assignments
	// (`?=`) only get recorded if not already present.
	Variables map[string]string
}

// MakeRule is one explicit Makefile rule: target, prereqs, recipe.
type MakeRule struct {
	Target  string
	Prereqs []string
	Recipe  []string
}

// parseMakeDB walks `make -np` output and returns the parsed
// rules + variables. Tolerates malformed input — returns a
// best-effort partial result rather than failing.
//
// The output format `make -np` emits is line-oriented and
// roughly section-delimited:
//
//	# Variables
//	# environment
//	CC = cc
//	CFLAGS = -O2
//	# automatic
//	@F = ...
//	# Files
//	greet: greet.o
//	#  Last modified ...
//	#  recipe to execute (from 'Makefile', line 11):
//		$(CC) $(CFLAGS) -o $@ $^
//
// The recipe lines start with a TAB. The rule line itself
// (`target: prereqs`) has the colon; subsequent comment lines
// (`#  ...`) describe the target's metadata; a blank line
// separates rules.
func parseMakeDB(body []byte) *MakeDB {
	db := &MakeDB{
		Rules:     map[string]MakeRule{},
		Variables: map[string]string{},
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	var current *MakeRule
	for scanner.Scan() {
		line := scanner.Text()

		// Recipe lines: TAB-prefixed text following a rule
		// line. Append to the most recent rule's Recipe.
		if current != nil && strings.HasPrefix(line, "\t") {
			current.Recipe = append(current.Recipe, strings.TrimPrefix(line, "\t"))
			continue
		}

		// Blank line ends the current rule's recipe block —
		// any TAB-prefixed lines after this belong to a
		// different rule.
		if line == "" {
			if current != nil {
				db.Rules[current.Target] = *current
				current = nil
			}
			continue
		}

		// Comment lines: rule-metadata comments
		// (`#  Last modified ...`, `#  recipe to execute
		// ...`) interleave with the rule's recipe TAB-lines
		// and shouldn't close the current rule. Section
		// headers (`# Files`, `# Variables`, ...) also pass
		// through harmlessly since the next non-comment
		// non-blank line either starts a new rule or
		// declares a variable, both of which close any
		// pending rule via the explicit branches below.
		if strings.HasPrefix(line, "#") {
			continue
		}

		// Variable assignment at top level. Recognized
		// operators: `=`, `:=`, `::=`, `:::=`, `?=`, `+=`.
		// Order of checks matters — `:=` shares prefix with
		// `=` but takes precedence.
		if v, value, ok := parseAssignment(line); ok {
			if _, exists := db.Variables[v]; !exists || !strings.Contains(line, "?=") {
				db.Variables[v] = value
			}
			if current != nil {
				db.Rules[current.Target] = *current
				current = nil
			}
			continue
		}

		// Explicit rule: `target [target ...]: [prereqs ...]`.
		// We only record the first target name; multi-target
		// rules expand to one entry per target, but for spike
		// purposes the first is sufficient.
		if idx := strings.Index(line, ":"); idx > 0 {
			// Reject pattern rules (target contains `%`)
			// and double-colon rules — out of scope.
			lhs := line[:idx]
			if strings.ContainsAny(lhs, "%") {
				continue
			}
			rhs := strings.TrimSpace(line[idx+1:])
			// Skip target-specific variable assignments like
			// `target: VAR = value`.
			if _, _, ok := parseAssignment(rhs); ok {
				continue
			}
			if current != nil {
				db.Rules[current.Target] = *current
			}
			targets := strings.Fields(lhs)
			if len(targets) == 0 {
				current = nil
				continue
			}
			var prereqs []string
			if rhs != "" {
				prereqs = strings.Fields(rhs)
			}
			current = &MakeRule{
				Target:  targets[0],
				Prereqs: prereqs,
			}
			continue
		}
	}
	if current != nil {
		db.Rules[current.Target] = *current
	}
	return db
}

// parseAssignment recognizes `VAR <op> value` assignments. ok
// reports whether the line is an assignment; name + value are
// the captured identifier + RHS (whitespace-trimmed).
func parseAssignment(line string) (name, value string, ok bool) {
	for _, op := range []string{":::=", "::=", ":=", "?=", "+=", "="} {
		idx := strings.Index(line, op)
		if idx <= 0 {
			continue
		}
		// `target: prereqs` looks like `:` to indexOf — exclude
		// by requiring the LHS to be a valid Make variable
		// identifier (alphanumeric, _, -).
		lhs := strings.TrimSpace(line[:idx])
		if !isMakeIdent(lhs) {
			continue
		}
		return lhs, strings.TrimSpace(line[idx+len(op):]), true
	}
	return "", "", false
}

// isMakeIdent reports whether s is a syntactically valid Make
// variable name (letters, digits, _, -). Empty rejected.
func isMakeIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-', c == '.':
			continue
		}
		return false
	}
	return true
}
