package ctest

import (
	"fmt"
	"strings"
	"unicode"
)

// call is one CTestTestfile.cmake function invocation: a name followed
// by a paren-delimited argument list.
type call struct {
	name string
	args []string
}

// scanCalls reads the body of a CTestTestfile.cmake file and returns
// every recognized invocation in source order. CTestTestfile.cmake is
// machine-generated, so the parser only needs to handle the subset
// CMake actually emits: bracket-quoted names (`[=[name]=]`), double-
// quoted strings, and unquoted identifiers, separated by whitespace.
//
// Unrecognized call names (anything other than add_test,
// set_tests_properties, subdirs, include) are still tokenized but
// returned for the caller to ignore. Comments (`#` to end-of-line)
// and blank lines are skipped.
func scanCalls(src []byte) ([]call, error) {
	s := &scanner{src: src}
	var out []call
	for !s.eof() {
		s.skipWhitespaceAndComments()
		if s.eof() {
			break
		}
		name := s.readIdent()
		if name == "" {
			return nil, fmt.Errorf("expected identifier at offset %d", s.pos)
		}
		s.skipSpacesAndTabs()
		if s.peek() != '(' {
			return nil, fmt.Errorf("expected '(' after %q at offset %d", name, s.pos)
		}
		s.pos++ // consume '('
		args, err := s.readArgsUntilCloseParen()
		if err != nil {
			return nil, fmt.Errorf("call %q: %w", name, err)
		}
		out = append(out, call{name: name, args: args})
	}
	return out, nil
}

type scanner struct {
	src []byte
	pos int
}

func (s *scanner) eof() bool   { return s.pos >= len(s.src) }
func (s *scanner) peek() byte  { return s.src[s.pos] }
func (s *scanner) advance() {
	if !s.eof() {
		s.pos++
	}
}

func (s *scanner) skipWhitespaceAndComments() {
	for !s.eof() {
		c := s.peek()
		switch {
		case c == '#':
			for !s.eof() && s.peek() != '\n' {
				s.advance()
			}
		case unicode.IsSpace(rune(c)):
			s.advance()
		default:
			return
		}
	}
}

func (s *scanner) skipSpacesAndTabs() {
	for !s.eof() {
		c := s.peek()
		if c == ' ' || c == '\t' {
			s.advance()
			continue
		}
		return
	}
}

func (s *scanner) readIdent() string {
	start := s.pos
	for !s.eof() {
		c := s.peek()
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			s.advance()
			continue
		}
		break
	}
	return string(s.src[start:s.pos])
}

// readArgsUntilCloseParen reads tokens until the matching ')' (the
// opening '(' was already consumed). Whitespace and comments inside
// the call are ignored.
func (s *scanner) readArgsUntilCloseParen() ([]string, error) {
	var args []string
	for !s.eof() {
		s.skipWhitespaceAndComments()
		if s.eof() {
			return nil, fmt.Errorf("unterminated argument list")
		}
		c := s.peek()
		if c == ')' {
			s.advance()
			return args, nil
		}
		tok, err := s.readToken()
		if err != nil {
			return nil, err
		}
		args = append(args, tok)
	}
	return nil, fmt.Errorf("unterminated argument list")
}

// readToken reads one CMake-syntax token: bracket-quoted, double-
// quoted, or unquoted identifier-ish.
func (s *scanner) readToken() (string, error) {
	switch s.peek() {
	case '[':
		// Possibly bracket-quoted: [=*[ ... ]=*]
		if eq, ok := s.tryBracketOpen(); ok {
			return s.readBracketQuoted(eq)
		}
		// Stray '[' as an unquoted token is unusual; treat as
		// part of an unquoted run.
		return s.readUnquoted()
	case '"':
		return s.readDoubleQuoted()
	default:
		return s.readUnquoted()
	}
}

// tryBracketOpen recognizes `[=*[` and returns the count of '=' if
// matched. Leaves position unchanged on failure.
func (s *scanner) tryBracketOpen() (int, bool) {
	if s.peek() != '[' {
		return 0, false
	}
	p := s.pos + 1
	eq := 0
	for p < len(s.src) && s.src[p] == '=' {
		eq++
		p++
	}
	if p < len(s.src) && s.src[p] == '[' {
		s.pos = p + 1
		return eq, true
	}
	return 0, false
}

func (s *scanner) readBracketQuoted(eq int) (string, error) {
	closeMarker := "]" + strings.Repeat("=", eq) + "]"
	rest := s.src[s.pos:]
	idx := strings.Index(string(rest), closeMarker)
	if idx < 0 {
		return "", fmt.Errorf("unterminated bracket-quoted string")
	}
	body := string(rest[:idx])
	s.pos += idx + len(closeMarker)
	return body, nil
}

func (s *scanner) readDoubleQuoted() (string, error) {
	if s.peek() != '"' {
		return "", fmt.Errorf("expected '\"'")
	}
	s.advance()
	var b strings.Builder
	for !s.eof() {
		c := s.peek()
		switch c {
		case '"':
			s.advance()
			return b.String(), nil
		case '\\':
			s.advance()
			if s.eof() {
				return "", fmt.Errorf("unterminated escape in double-quoted string")
			}
			esc := s.peek()
			s.advance()
			switch esc {
			case '"', '\\':
				b.WriteByte(esc)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				// Unknown escape: keep the literal char (CMake's
				// behavior is to drop the backslash).
				b.WriteByte(esc)
			}
		default:
			b.WriteByte(c)
			s.advance()
		}
	}
	return "", fmt.Errorf("unterminated double-quoted string")
}

func (s *scanner) readUnquoted() (string, error) {
	var b strings.Builder
	for !s.eof() {
		c := s.peek()
		if c == ')' || c == '(' || c == '"' || c == '#' || c == '\\' {
			break
		}
		if unicode.IsSpace(rune(c)) {
			break
		}
		b.WriteByte(c)
		s.advance()
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("expected token at offset %d", s.pos)
	}
	return b.String(), nil
}
