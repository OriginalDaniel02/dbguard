package liquibase

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Engine is the dbms name Liquibase uses for PostgreSQL in `dbms=` attributes.
const Engine = "postgresql"

// Changeset is one <changeSet>, translated.
type Changeset struct {
	ID, Author string
	Line       int      // line of the changeSet in the changelog file
	Comment    string   // the changeSet's comment (may carry a dbguard:ignore directive)
	SQL        []string // equivalent PostgreSQL statements
	Skipped    []string // change types DB Guard does not analyze
	Problems   []string // translation problems (missing sqlFile, ...)
}

// Changelog is a parsed changelog file.
type Changelog struct {
	Changesets []Changeset
	Properties map[string]string // <property> values, usable as ${name}
}

// Parse reads a changelog. The format is chosen by file extension (.xml, .yaml,
// .yml, .json); path is also used to resolve relative sqlFile references.
func Parse(path string, data []byte) (*Changelog, error) {
	var root *node
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xml":
		root, err = parseXML(data)
	case ".yaml", ".yml", ".json":
		root, err = parseYAML(data)
	default:
		return nil, fmt.Errorf("unsupported changelog format %q", filepath.Ext(path))
	}
	if err != nil {
		return nil, err
	}

	c := &Changelog{Properties: map[string]string{}}
	for _, k := range root.kids {
		if k.name == "property" && dbmsMatches(k.attr("dbms")) {
			if name := k.attr("name"); name != "" {
				if _, set := c.Properties[name]; !set { // Liquibase: first definition wins
					c.Properties[name] = k.attr("value")
				}
			}
		}
	}
	for _, k := range root.kids {
		if k.name != "changeSet" || !dbmsMatches(k.attr("dbms")) {
			continue
		}
		c.Changesets = append(c.Changesets, translateChangeset(k, filepath.Dir(path)))
	}
	return c, nil
}

// Sniff reports whether a file looks like a Liquibase changelog, from its first bytes.
func Sniff(path string, head []byte) bool {
	h := string(head)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xml", ".yaml", ".yml", ".json":
		return strings.Contains(h, "databaseChangeLog")
	case ".sql":
		return strings.HasPrefix(strings.TrimSpace(h), "--liquibase formatted sql")
	}
	return false
}

// IsStructured reports whether the file needs translation (XML/YAML/JSON), as
// opposed to Liquibase formatted SQL, which is plain SQL.
func IsStructured(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xml", ".yaml", ".yml", ".json":
		return true
	}
	return false
}

// Span maps a range of lines in the combined SQL back to a changeset.
type Span struct {
	Start, End int // 1-based, inclusive, in the combined SQL
	Changeset  int // index into Changelog.Changesets
}

// Combined is the whole changelog as one SQL script (so tables created in one
// changeset are known when a later one indexes them) plus a line map.
type Combined struct {
	SQL      string
	Spans    []Span
	Problems []string
}

// SpanAt returns the changeset owning a combined-SQL line, or -1.
func (c Combined) SpanAt(line int) int {
	for _, s := range c.Spans {
		if line >= s.Start && line <= s.End {
			return s.Changeset
		}
	}
	return -1
}

// Combine concatenates every changeset's statements. valid, if non-nil, is called
// per statement; statements it rejects are left out and reported as problems
// rather than failing the whole file.
func (c *Changelog) Combine(valid func(string) error) Combined {
	var out Combined
	var b strings.Builder
	line := 1
	for i, cs := range c.Changesets {
		start := line
		for _, st := range cs.SQL {
			if valid != nil {
				if err := valid(st); err != nil {
					out.Problems = append(out.Problems, fmt.Sprintf("line %d: changeSet %s: could not analyze a translated statement (%v)", cs.Line, label(cs), err))
					continue
				}
			}
			st = strings.TrimRight(st, " \t\r\n")
			if !strings.HasSuffix(st, ";") {
				st += ";"
			}
			b.WriteString(st)
			b.WriteString("\n")
			line += strings.Count(st, "\n") + 1
		}
		if line > start {
			out.Spans = append(out.Spans, Span{Start: start, End: line - 1, Changeset: i})
		}
		for _, s := range cs.Skipped {
			out.Problems = append(out.Problems, fmt.Sprintf("line %d: changeSet %s: change type %q is not analyzed", cs.Line, label(cs), s))
		}
		for _, p := range cs.Problems {
			out.Problems = append(out.Problems, fmt.Sprintf("line %d: changeSet %s: %s", cs.Line, label(cs), p))
		}
	}
	out.SQL = b.String()
	return out
}

func label(cs Changeset) string {
	if cs.Author != "" {
		return cs.ID + ":" + cs.Author
	}
	return cs.ID
}

// dbmsMatches implements Liquibase's dbms attribute for PostgreSQL: empty or
// "all" matches; a list matches if it names postgresql; "!postgresql" excludes it;
// a list of only other exclusions ("!oracle") still matches.
func dbmsMatches(attr string) bool {
	attr = strings.TrimSpace(strings.ToLower(attr))
	if attr == "" || attr == "all" {
		return true
	}
	positive, included := false, false
	for _, d := range strings.Split(attr, ",") {
		d = strings.TrimSpace(d)
		switch {
		case d == "":
		case strings.HasPrefix(d, "!"):
			if d[1:] == Engine {
				return false
			}
		default:
			positive = true
			if d == Engine || d == "all" {
				included = true
			}
		}
	}
	return !positive || included
}

// q quotes an identifier.
func q(s string) string { return `"` + strings.ReplaceAll(strings.TrimSpace(s), `"`, `""`) + `"` }

func tableRef(schema, table string) string {
	if strings.TrimSpace(schema) != "" {
		return q(schema) + "." + q(table)
	}
	return q(table)
}

func colList(s string) string {
	var parts []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			parts = append(parts, q(c))
		}
	}
	return strings.Join(parts, ", ")
}

func isTrue(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "true") }

func lit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var numeric = regexp.MustCompile(`^[-+]?[0-9]*\.?[0-9]+([eE][-+]?[0-9]+)?$`)

// defaultExpr renders a <column>'s default, preserving whether it is a constant
// or a function (which decides if ADD COLUMN rewrites the table).
func defaultExpr(c *node) string {
	switch {
	case c.attr("defaultValueComputed") != "":
		return c.attr("defaultValueComputed")
	case c.attr("defaultValueSequenceNext") != "":
		return "nextval(" + lit(c.attr("defaultValueSequenceNext")) + ")"
	case c.attr("defaultValueNumeric") != "":
		v := strings.TrimSpace(c.attr("defaultValueNumeric"))
		if numeric.MatchString(v) {
			return v
		}
		return c.attr("defaultValueNumeric") // e.g. a ${placeholder}
	case c.attr("defaultValueBoolean") != "":
		return strings.ToLower(strings.TrimSpace(c.attr("defaultValueBoolean")))
	case c.attr("defaultValueDate") != "":
		v := strings.TrimSpace(c.attr("defaultValueDate"))
		if strings.Contains(v, "(") || strings.HasPrefix(strings.ToUpper(v), "CURRENT_") || strings.HasPrefix(v, "${") {
			return v
		}
		return lit(v)
	case c.attr("defaultValue") != "":
		return lit(c.attr("defaultValue"))
	}
	return ""
}

func constraintAttr(c *node, name string) string {
	if k := c.child("constraints"); k != nil {
		return k.attr(name)
	}
	return ""
}

func readSQLFile(baseDir string, n *node) (string, error) {
	path := n.attr("path")
	if path == "" {
		return "", fmt.Errorf("sqlFile has no path")
	}
	candidates := []string{filepath.Join(baseDir, path), path}
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("sqlFile %q not found", path)
}
