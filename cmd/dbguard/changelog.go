package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/OriginalDaniel02/dbguard/internal/changelog"
	"github.com/OriginalDaniel02/dbguard/internal/drift"
)

// parseWhen reads a point in time: 2026-10-01, 2026-10-01T06:00:00Z, or a relative
// age such as 30d, 12h, 90m (meaning "that long before now").
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a date (use 2026-10-01, an RFC 3339 time, or an age like 30d / 12h)", s)
}

func changelogCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("changelog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `usage: dbguard changelog --dir <snapshots> [filters] [flags]

Builds a per-table schema changelog from the snapshots saved by 'dbguard drift --save-dir':
for every pair of consecutive snapshots of an environment, what changed between them.
Example: when did the type of orders.status change?

  dbguard changelog --dir snapshots --table orders --object status --kind column-type-changed

flags:`)
		fs.PrintDefaults()
	}
	var ignores listFlag
	dir := fs.String("dir", "", "directory of snapshots: <dir>/<environment>/<timestamp>.json (required)")
	env := fs.String("env", "", "only this environment")
	tbl := fs.String("table", "", "only this table (name, or a glob such as 'public.order*')")
	obj := fs.String("object", "", "only this column / index / constraint / trigger (name or glob)")
	kind := fs.String("kind", "", "only this kind, e.g. column-type-changed, or a noun: column, table, index, constraint, view")
	action := fs.String("action", "", "only added | dropped | changed")
	search := fs.String("search", "", "case-insensitive text to find in the change, table or values")
	since := fs.String("since", "", "only changes detected since: a date (2026-10-01), an RFC 3339 time, or an age (30d, 12h)")
	until := fs.String("until", "", "only changes detected until: same formats")
	limit := fs.Int("limit", 0, "show at most this many changes (newest first); 0 = all")
	ignoreFile := fs.String("ignore-file", "", "file with one ignore pattern per line (same as 'drift')")
	format := fs.String("format", "text", "output format: text | markdown | json")
	fs.Var(&ignores, "ignore", "glob of a table or object to leave out (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fs.Usage()
		return 2
	}

	now := nowFn().UTC()
	q := changelog.Query{Env: *env, Table: *tbl, Object: *obj, Kind: *kind, Action: *action, Text: *search}
	var err error
	if *since != "" {
		if q.Since, err = parseWhen(*since, now); err != nil {
			fmt.Fprintln(stderr, "dbguard: --since:", err)
			return 2
		}
	}
	if *until != "" {
		if q.Until, err = parseWhen(*until, now); err != nil {
			fmt.Fprintln(stderr, "dbguard: --until:", err)
			return 2
		}
		// A bare date means the whole day.
		if len(*until) == len("2006-01-02") && !strings.HasSuffix(*until, "d") {
			q.Until = q.Until.Add(24*time.Hour - time.Second)
		}
	}
	if *ignoreFile != "" {
		pats, err := readIgnoreFile(*ignoreFile)
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
		ignores = append(ignores, pats...)
	}

	snaps, warnings, err := changelog.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "dbguard:", err)
		return 2
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "dbguard: warning:", w)
	}
	if len(snaps) == 0 {
		fmt.Fprintf(stderr, "dbguard: no snapshots found in %s (they are written by 'dbguard drift --save-dir')\n", *dir)
		return 2
	}

	entries := changelog.Filter(changelog.Build(snaps, drift.Options{Ignore: ignores}), q)
	if *limit > 0 && len(entries) > *limit {
		entries = entries[:*limit]
	}
	switch *format {
	case "text":
		changelog.Text(stdout, entries)
	case "markdown":
		changelog.Markdown(stdout, entries)
	case "json":
		if err := changelog.JSON(stdout, entries); err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "dbguard: unknown format %q\n", *format)
		return 2
	}
	return 0
}
