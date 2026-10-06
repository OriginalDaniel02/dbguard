package rules

import (
	"fmt"
	"strings"
)

// scanSkip returns the index just past a string, quoted identifier or comment
// starting at i, or i itself if none starts there.
func scanSkip(s string, i int) int {
	switch {
	case s[i] == '\'' || s[i] == '"' || s[i] == '`':
		q := s[i]
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' && q != '`' {
				j++
				continue
			}
			if s[j] == q {
				if j+1 < len(s) && s[j+1] == q { // doubled quote
					j++
					continue
				}
				return j + 1
			}
		}
		return len(s)
	case strings.HasPrefix(s[i:], "--") && (i+2 >= len(s) || s[i+2] == ' ' || s[i+2] == '\t' || s[i+2] == '\n' || s[i+2] == '\r'), s[i] == '#':
		if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
			return i + j
		}
		return len(s)
	case strings.HasPrefix(s[i:], "/*"):
		if j := strings.Index(s[i+2:], "*/"); j >= 0 {
			return i + 2 + j + 2
		}
		return len(s)
	}
	return i
}

func isWordByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// markExpressionDefaults replaces every `DEFAULT ( ... )` with an opaque function
// call. On MySQL a parenthesized default is an *expression default*, which
// supports neither INSTANT nor INPLACE (verified on 8.0.46 even for `DEFAULT (5)`),
// but the parser's tree drops the parentheses and rejects some expressions
// (e.g. arithmetic). The returned func restores the original text in output.
func markExpressionDefaults(sql string) (string, func(string) string) {
	var b strings.Builder
	back := map[string]string{}
	n := 0
	i := 0
	for i < len(sql) {
		if j := scanSkip(sql, i); j != i {
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if (sql[i] == 'D' || sql[i] == 'd') && len(sql)-i >= 7 && strings.EqualFold(sql[i:i+7], "default") &&
			(i == 0 || !isWordByte(sql[i-1])) && (i+7 >= len(sql) || !isWordByte(sql[i+7])) {
			k := i + 7
			for k < len(sql) && (sql[k] == ' ' || sql[k] == '\t' || sql[k] == '\r' || sql[k] == '\n') {
				k++
			}
			if k < len(sql) && sql[k] == '(' {
				if end := matchParen(sql, k); end > 0 {
					n++
					marker := fmt.Sprintf("(dbguard_expr_%d())", n)
					back[marker] = sql[k : end+1]
					b.WriteString(sql[i:k])
					b.WriteString(marker)
					// Keep line numbers stable if the expression spans lines.
					b.WriteString(strings.Repeat("\n", strings.Count(sql[k:end+1], "\n")))
					i = end + 1
					continue
				}
			}
		}
		b.WriteByte(sql[i])
		i++
	}
	return b.String(), func(s string) string {
		for m, orig := range back {
			s = strings.ReplaceAll(s, m, orig)
		}
		return s
	}
}

// matchParen returns the index of the ")" matching the "(" at open, or -1.
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		if j := scanSkip(s, i); j != i {
			i = j - 1
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

type segment struct {
	start int // byte offset in the SQL
	text  string
}

// splitSQL splits a script into statements at top-level semicolons.
func splitSQL(sql string) []segment {
	var segs []segment
	start := 0
	for i := 0; i < len(sql); i++ {
		if j := scanSkip(sql, i); j != i {
			i = j - 1
			continue
		}
		if sql[i] == ';' {
			if t := strings.TrimSpace(sql[start:i]); t != "" {
				segs = append(segs, segment{start: start, text: sql[start : i+1]})
			}
			start = i + 1
		}
	}
	if t := strings.TrimSpace(sql[start:]); t != "" {
		segs = append(segs, segment{start: start, text: sql[start:]})
	}
	return segs
}
