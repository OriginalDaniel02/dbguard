// Package override handles explicit, auditable acknowledgments of findings.
//
// Syntax, inside the migration file (so it lands in git history and code review):
//
//	-- dbguard:ignore <rule-id> reason: <why this is acceptable>
//
// The comment must sit on the line(s) directly above the statement, or inside
// it. A reason is mandatory; an ignore without one is itself reported.
package override

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/dbguard/dbguard/internal/rules"
)

var re = regexp.MustCompile(`^\s*--\s*dbguard:ignore\s+([a-z0-9-]+)(?:\s+reason:\s*(.*\S))?\s*$`)

// Directive is one parsed ignore comment.
type Directive struct {
	Line   int
	Rule   string
	Reason string
}

// Parse extracts every ignore directive from a migration's SQL.
func Parse(sql string) []Directive {
	var out []Directive
	for i, l := range strings.Split(sql, "\n") {
		if m := re.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			out = append(out, Directive{Line: i + 1, Rule: m[1], Reason: m[2]})
		}
	}
	return out
}

// Apply marks acknowledged findings (Finding.Override = reason). It returns
// directives that were invalid (no reason) or matched nothing, so callers can surface them.
func Apply(sql string, fs []rules.Finding) (problems []string) {
	lines := strings.Split(sql, "\n")
	used := map[int]bool{}
	ds := Parse(sql)
	for _, d := range ds {
		if d.Reason == "" {
			problems = append(problems, "line "+strconv.Itoa(d.Line)+": dbguard:ignore "+d.Rule+" needs a 'reason: ...'")
			used[d.Line] = true
		}
	}
	for i := range fs {
		f := &fs[i]
		end := f.Line + strings.Count(f.Statement, "\n")
		for _, d := range ds {
			if d.Rule != f.Rule || d.Reason == "" {
				continue
			}
			if (d.Line >= f.Line && d.Line <= end) || directlyAbove(lines, d.Line, f.Line) {
				f.Override = d.Reason
				used[d.Line] = true
			}
		}
	}
	for _, d := range ds {
		if !used[d.Line] {
			problems = append(problems, "line "+strconv.Itoa(d.Line)+": dbguard:ignore "+d.Rule+" matched no finding")
		}
	}
	return problems
}

// directlyAbove: every line from the directive up to the statement is a comment or blank.
func directlyAbove(lines []string, dline, stmtLine int) bool {
	if dline >= stmtLine {
		return false
	}
	for l := dline + 1; l < stmtLine; l++ {
		t := strings.TrimSpace(lines[l-1])
		if t != "" && !strings.HasPrefix(t, "--") {
			return false
		}
	}
	return true
}
