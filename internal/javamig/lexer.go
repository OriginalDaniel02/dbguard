// Package javamig reads Flyway Java migrations. It never runs Java: it lexes the
// source, finds the SQL string literals (including text blocks, "a" + "b"
// concatenation and String.format templates) and hands the SQL to the rules.
package javamig

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type tokKind int

const (
	tString tokKind = iota
	tIdent
	tNumber
	tPunct
)

type token struct {
	kind     tokKind
	text     string // identifier, number or punctuation text; for strings, the evaluated value
	line     int    // 1-based line where the token starts
	endLine  int
	block    bool // a text block
	startOff int  // byte offsets in the source
	endOff   int
}

// lex turns Java source into tokens. Comments are dropped. Problems (such as an
// unterminated string) are returned, not fatal: the rest of the file is still lexed.
func lex(src string) ([]token, []string) {
	var toks []token
	var problems []string
	line := 1
	i := 0
	n := len(src)

	for i < n {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++

		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}

		case c == '/' && i+1 < n && src[i+1] == '*':
			start := line
			i += 2
			closed := false
			for i < n {
				if src[i] == '*' && i+1 < n && src[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				if src[i] == '\n' {
					line++
				}
				i++
			}
			if !closed {
				problems = append(problems, fmt.Sprintf("line %d: unterminated /* comment", start))
			}

		case c == '"' && strings.HasPrefix(src[i:], `"""`):
			tok, next, err := lexTextBlock(src, i, line)
			if err != "" {
				problems = append(problems, fmt.Sprintf("line %d: %s", line, err))
				i = next
				line = tok.endLine
				continue
			}
			toks = append(toks, tok)
			i, line = next, tok.endLine

		case c == '"':
			tok, next, err := lexString(src, i, line)
			if err != "" {
				problems = append(problems, fmt.Sprintf("line %d: %s", line, err))
				i = next
				continue
			}
			toks = append(toks, tok)
			i = next

		case c == '\'':
			// A char literal: skip it (it cannot hold SQL).
			j := i + 1
			for j < n && src[j] != '\'' && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1

		case isIdentStart(c):
			j := i
			for j < n && isIdentPart(src[j]) {
				j++
			}
			toks = append(toks, token{kind: tIdent, text: src[i:j], line: line, endLine: line, startOff: i, endOff: j})
			i = j

		case c >= '0' && c <= '9':
			j := i
			for j < n && (isIdentPart(src[j]) || src[j] == '.') {
				j++
			}
			toks = append(toks, token{kind: tNumber, text: src[i:j], line: line, endLine: line, startOff: i, endOff: j})
			i = j

		default:
			toks = append(toks, token{kind: tPunct, text: string(c), line: line, endLine: line, startOff: i, endOff: i + 1})
			i++
		}
	}
	return toks, problems
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

// lexString reads a "..." literal starting at src[i] and evaluates its escapes.
func lexString(src string, i, line int) (token, int, string) {
	n := len(src)
	j := i + 1
	for j < n {
		switch src[j] {
		case '\\':
			j += 2
			continue
		case '"':
			raw := src[i+1 : j]
			val, err := unescape(raw)
			if err != "" {
				return token{}, j + 1, err
			}
			return token{kind: tString, text: val, line: line, endLine: line, startOff: i, endOff: j + 1}, j + 1, ""
		case '\n':
			return token{}, j, "unterminated string literal"
		}
		j++
	}
	return token{}, n, "unterminated string literal"
}

// lexTextBlock reads a """ text block following Java's algorithm (JLS 3.10.6):
// normalize line endings, strip the common incidental indentation (the closing
// delimiter's line counts), strip trailing spaces, then interpret escapes.
func lexTextBlock(src string, i, line int) (token, int, string) {
	n := len(src)
	j := i + 3
	// After the opening delimiter only spaces are allowed, then a line terminator.
	for j < n && (src[j] == ' ' || src[j] == '\t' || src[j] == '\f') {
		j++
	}
	if j < n && src[j] == '\r' {
		j++
	}
	if j >= n || src[j] != '\n' {
		return token{line: line, endLine: line}, i + 3, "a text block's opening \"\"\" must be followed by a line break"
	}
	j++ // past the newline
	contentStart := j
	endLine := line + 1
	for j < n {
		if src[j] == '\\' {
			if j+1 < n && src[j+1] == '\n' {
				endLine++
			}
			j += 2
			continue
		}
		if src[j] == '\n' {
			endLine++
		}
		if strings.HasPrefix(src[j:], `"""`) {
			raw := src[contentStart:j]
			val, err := evalTextBlock(raw)
			if err != "" {
				return token{line: line, endLine: endLine}, j + 3, err
			}
			return token{kind: tString, text: val, line: line, endLine: endLine, block: true, startOff: i, endOff: j + 3}, j + 3, ""
		}
		j++
	}
	return token{line: line, endLine: endLine}, n, "unterminated text block"
}

func evalTextBlock(raw string) (string, string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	last := len(lines) - 1

	isBlank := func(s string) bool { return strings.Trim(s, " \t\f") == "" }
	indent := func(s string) int {
		k := 0
		for k < len(s) && (s[k] == ' ' || s[k] == '\t' || s[k] == '\f') {
			k++
		}
		return k
	}
	min := -1
	for idx, l := range lines {
		// Blank lines do not count, except the line holding the closing delimiter.
		if isBlank(l) && idx != last {
			continue
		}
		if k := indent(l); min < 0 || k < min {
			min = k
		}
	}
	if min < 0 {
		min = 0
	}
	for idx, l := range lines {
		if isBlank(l) {
			lines[idx] = ""
			continue
		}
		if len(l) >= min {
			l = l[min:]
		}
		lines[idx] = strings.TrimRight(l, " \t\f")
	}
	return unescape(strings.Join(lines, "\n"))
}

// unescape interprets Java escape sequences (JLS 3.10.7), including \s and the
// text-block line continuation "\<newline>".
func unescape(s string) (string, string) {
	if !strings.ContainsRune(s, '\\') {
		return s, ""
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", "a backslash at the end of a literal"
		}
		switch s[i] {
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case 's':
			b.WriteByte(' ')
		case '"', '\'', '\\':
			b.WriteByte(s[i])
		case '\n':
			// line continuation: nothing is emitted
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case 'u':
			// \uXXXX (any number of u's)
			j := i
			for j < len(s) && s[j] == 'u' {
				j++
			}
			if j+4 > len(s) {
				return "", "an incomplete \\u escape"
			}
			v, err := strconv.ParseUint(s[j:j+4], 16, 32)
			if err != nil {
				return "", "an invalid \\u escape"
			}
			b.WriteRune(rune(v))
			i = j + 3
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// octal: up to three digits, the first at most 3 when three are used
			j := i
			max := 2
			if s[i] <= '3' {
				max = 3
			}
			for j < len(s) && j-i < max && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseUint(s[i:j], 8, 32)
			b.WriteRune(rune(v))
			i = j - 1
		default:
			return "", fmt.Sprintf("an invalid escape \\%c", s[i])
		}
	}
	out := b.String()
	if !utf8.ValidString(out) {
		return out, ""
	}
	return out, ""
}
