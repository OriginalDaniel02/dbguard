package changelog

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const stamp = "2006-01-02 15:04"

// Text writes entries grouped by table, newest change first within each table.
func Text(w io.Writer, entries []Entry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "no matching schema changes")
		return
	}
	for _, g := range group(entries) {
		fmt.Fprintln(w, g.table)
		for _, e := range g.entries {
			fmt.Fprintf(w, "  %s UTC  %-12s %s\n", e.Time.Format(stamp), e.Env, e.Change)
			fmt.Fprintf(w, "      detected between %s and %s UTC\n", e.PrevTime.Format(stamp), e.Time.Format(stamp))
		}
	}
	fmt.Fprintf(w, "\n%d change(s) in %d object(s)\n", len(entries), len(group(entries)))
}

// Markdown writes a document suitable for a wiki or a PR.
func Markdown(w io.Writer, entries []Entry) {
	fmt.Fprintln(w, "# Schema changelog")
	if len(entries) == 0 {
		fmt.Fprintln(w, "\nNo matching schema changes.")
		return
	}
	for _, g := range group(entries) {
		fmt.Fprintf(w, "\n## `%s`\n\n| Detected (UTC) | Environment | Change | Window |\n|---|---|---|---|\n", g.table)
		for _, e := range g.entries {
			fmt.Fprintf(w, "| %s | %s | %s | %s to %s |\n", e.Time.Format(stamp), e.Env, escapeMD(e.Change), e.PrevTime.Format(stamp), e.Time.Format(stamp))
		}
	}
}

// JSON writes the entries as an array.
func JSON(w io.Writer, entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

type tableGroup struct {
	table   string
	entries []Entry
}

func group(entries []Entry) []tableGroup {
	idx := map[string]int{}
	var groups []tableGroup
	for _, e := range entries {
		i, ok := idx[e.Table]
		if !ok {
			i = len(groups)
			idx[e.Table] = i
			groups = append(groups, tableGroup{table: e.Table})
		}
		groups[i].entries = append(groups[i].entries, e)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].table < groups[j].table })
	return groups
}

func escapeMD(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}
