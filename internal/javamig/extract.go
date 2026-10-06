package javamig

import (
	"fmt"
	"regexp"
	"strings"
)

// Piece is one part of a SQL string: where it starts in the SQL text and in the Java file.
type Piece struct {
	SQLLine  int  // 0-based line within Segment.SQL where this piece starts
	JavaLine int  // 1-based Java line the piece's first character is on
	Block    bool // a text block: each SQL line is one Java line further down
}

// Segment is one SQL string found in the source.
type Segment struct {
	SQL     string
	Line    int      // Java line where the string starts
	Pieces  []Piece  // for mapping SQL lines back to Java lines
	Dynamic []string // Java expressions that were part of the string and are unknown (shown as ${expr})
}

// JavaLine maps a 0-based line inside Segment.SQL to its Java line.
func (s Segment) JavaLine(sqlLine int) int {
	p := s.Pieces[0]
	for _, c := range s.Pieces {
		if c.SQLLine <= sqlLine {
			p = c
		}
	}
	if p.Block {
		return p.JavaLine + (sqlLine - p.SQLLine)
	}
	return p.JavaLine
}

// Extraction is everything found in one Java file.
type Extraction struct {
	Segments  []Segment
	Problems  []string
	Migration bool // the file looks like a Flyway Java migration
}

// Sniff reports whether a Java file looks like a Flyway Java migration, from its head.
func Sniff(head string) bool {
	return strings.Contains(head, "org.flywaydb.core.api.migration") ||
		strings.Contains(head, "BaseJavaMigration") ||
		strings.Contains(head, "JavaMigration")
}

var sqlStart = regexp.MustCompile(`(?is)^\s*(` +
	`alter\s+(table|index)\b` +
	`|create\s+((unique|fulltext|spatial|temporary|temp|global|unlogged|materialized|recursive|or\s+replace|concurrently)\s+)*(table|index|view|sequence|type|trigger|extension)\b` +
	`|drop\s+(table|index|column|view)\b` +
	`|optimize\s+table\b` +
	`|rename\s+table\b` +
	`|set\s+((session|global)\s+)?(@@(session\.|global\.)?)?foreign_key_checks\b` +
	`)`)

var leadingComments = regexp.MustCompile(`(?s)^(\s+|--[^\n]*\n|/\*.*?\*/)+`)

var (
	alterTable  = regexp.MustCompile(`(?is)^\s*alter\s+table\s+(if\s+exists\s+)?(only\s+)?\S+\s+(add|drop|alter|modify|change|rename|set|convert|engine|algorithm|lock|force|disable|enable|validate|owner|attach|detach|auto_increment|comment|row_format|charset|character|default|collate|inherit|no|cluster|replica|reset)\b`)
	hasOn       = regexp.MustCompile(`(?is)\bon\b`)
	hasBody     = regexp.MustCompile(`(?is)\(|\bas\b|\blike\b`)
	startsAlter = regexp.MustCompile(`(?is)^\s*alter\s+table\b`)
	startsIndex = regexp.MustCompile(`(?is)^\s*create\s+((unique|fulltext|spatial|concurrently)\s+)*index\b`)
	startsTable = regexp.MustCompile(`(?is)^\s*create\s+((temporary|temp|global|unlogged)\s+)*table\b`)
)

// isSQL reports whether a string is one of the statements DB Guard analyzes. The checks go
// beyond the first words so that ordinary text such as a log message ("Alter table failed")
// is not mistaken for SQL.
func isSQL(s string) bool {
	s = leadingComments.ReplaceAllString(s, "")
	if !sqlStart.MatchString(s) {
		return false
	}
	switch {
	case startsAlter.MatchString(s):
		return alterTable.MatchString(s)
	case startsIndex.MatchString(s):
		return hasOn.MatchString(s)
	case startsTable.MatchString(s):
		return hasBody.MatchString(s)
	}
	return true
}

var formatSpec = regexp.MustCompile(`%(\d+\$)?[sSdD]`)

// Extract finds the SQL in a Java source file.
func Extract(src string) *Extraction {
	toks, problems := lex(src)
	ex := &Extraction{Problems: problems, Migration: Sniff(src)}

	for i := 0; i < len(toks); {
		if toks[i].kind != tString {
			i++
			continue
		}
		seg, next := group(src, toks, i)
		i = next
		if !isSQL(seg.SQL) {
			continue
		}
		// String.format templates: %s / %d are unknown values.
		seg.SQL = formatSpec.ReplaceAllLiteralString(seg.SQL, "${arg}")
		seg.SQL = strings.ReplaceAll(seg.SQL, "%%", "%")
		if len(seg.Dynamic) > 0 || strings.Contains(seg.SQL, "${arg}") {
			ex.Problems = append(ex.Problems, fmt.Sprintf(
				"line %d: part of this SQL is only known at runtime, so it was analyzed with that part unknown (tables behind it are assumed large)", seg.Line))
		}
		ex.Segments = append(ex.Segments, seg)
	}
	return ex
}

// group reads  "literal" (+ operand)*  starting at toks[i] and returns the SQL it builds.
func group(src string, toks []token, i int) (Segment, int) {
	var b strings.Builder
	seg := Segment{Line: toks[i].line}
	add := func(text string, line int, block bool) {
		seg.Pieces = append(seg.Pieces, Piece{SQLLine: strings.Count(b.String(), "\n"), JavaLine: line, Block: block})
		b.WriteString(text)
	}

	first := toks[i]
	if first.block {
		add(first.text, first.line+1, true) // the content starts on the line after the opening """
	} else {
		add(first.text, first.line, false)
	}
	j := i + 1
	for j+1 < len(toks) && toks[j].kind == tPunct && toks[j].text == "+" {
		k := j + 1
		switch t := toks[k]; {
		case t.kind == tString:
			if t.block {
				add(t.text, t.line+1, true)
			} else {
				add(t.text, t.line, false)
			}
			j = k + 1
		case t.kind == tNumber:
			add(t.text, t.line, false)
			j = k + 1
		default:
			end, ok := operandEnd(toks, k)
			if !ok {
				seg.SQL = b.String()
				return seg, j
			}
			expr := sanitize(src[toks[k].startOff:toks[end-1].endOff])
			seg.Dynamic = append(seg.Dynamic, expr)
			add("${"+expr+"}", toks[k].line, false)
			j = end
		}
	}
	seg.SQL = b.String()
	return seg, j
}

// operandEnd parses a non-literal operand (a name, a call chain, a parenthesized
// expression) starting at toks[k] and returns the index after it.
func operandEnd(toks []token, k int) (int, bool) {
	t := toks[k]
	switch {
	case t.kind == tPunct && t.text == "(":
		return balanced(toks, k, "(", ")")
	case t.kind == tIdent:
		j := k + 1
		for j < len(toks) {
			switch {
			case toks[j].kind == tPunct && toks[j].text == ".":
				if j+1 < len(toks) && toks[j+1].kind == tIdent {
					j += 2
					continue
				}
			case toks[j].kind == tPunct && toks[j].text == "(":
				end, ok := balanced(toks, j, "(", ")")
				if !ok {
					return k, false
				}
				j = end
				continue
			case toks[j].kind == tPunct && toks[j].text == "[":
				end, ok := balanced(toks, j, "[", "]")
				if !ok {
					return k, false
				}
				j = end
				continue
			}
			break
		}
		return j, true
	}
	return k, false
}

func balanced(toks []token, k int, open, close string) (int, bool) {
	depth := 0
	for j := k; j < len(toks); j++ {
		if toks[j].kind != tPunct {
			continue
		}
		switch toks[j].text {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return k, false
}

// sanitize makes a Java expression safe to use as a ${placeholder} name.
func sanitize(expr string) string {
	expr = strings.Join(strings.Fields(expr), "")
	expr = strings.NewReplacer("}", "_", "{", "_", "\n", "").Replace(expr)
	if len(expr) > 60 {
		expr = expr[:60]
	}
	return expr
}

// Span maps a range of lines in the combined SQL back to a segment.
type Span struct {
	Start, End int // 1-based, inclusive, in the combined SQL
	Segment    int
}

// Combined is every extracted statement as one script (so a table created earlier in the
// migration is known when a later statement touches it), plus the line map.
type Combined struct {
	SQL      string
	Spans    []Span
	Problems []string
	segs     []Segment
}

// Combine joins the segments. valid, if non-nil, is called per segment; a segment it
// rejects is left out and reported rather than failing the whole file.
func (e *Extraction) Combine(valid func(string) error) Combined {
	out := Combined{Problems: append([]string(nil), e.Problems...)}
	var b strings.Builder
	line := 1
	for idx, s := range e.Segments {
		sql := strings.TrimRight(s.SQL, " \t\r\n")
		if !strings.HasSuffix(sql, ";") {
			sql += ";"
		}
		if valid != nil {
			if err := valid(sql); err != nil {
				out.Problems = append(out.Problems, fmt.Sprintf(
					"line %d: could not parse this SQL, so it was NOT analyzed (it may be built dynamically): %s", s.Line, firstLine(err.Error())))
				continue
			}
		}
		n := strings.Count(sql, "\n") + 1
		out.Spans = append(out.Spans, Span{Start: line, End: line + n - 1, Segment: len(out.segs)})
		out.segs = append(out.segs, e.Segments[idx])
		b.WriteString(sql)
		b.WriteString("\n")
		line += n
	}
	out.SQL = b.String()
	return out
}

// JavaLine maps a line of the combined SQL to the Java file line, or 0 if unknown.
func (c Combined) JavaLine(combinedLine int) int {
	for _, sp := range c.Spans {
		if combinedLine >= sp.Start && combinedLine <= sp.End {
			return c.segs[sp.Segment].JavaLine(combinedLine - sp.Start)
		}
	}
	return 0
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
