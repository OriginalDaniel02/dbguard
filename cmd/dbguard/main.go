// Command dbguard checks database migrations for risky locking operations.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dbguard/dbguard/internal/flyway"
	"github.com/dbguard/dbguard/internal/override"
	"github.com/dbguard/dbguard/internal/pg"
	"github.com/dbguard/dbguard/internal/report"
	"github.com/dbguard/dbguard/internal/rules"
)

const usage = `usage: dbguard check [flags] <migration-file-or-dir>...

Exit codes: 0 = no blocking findings, 1 = blocking findings, 2 = error.

flags:`

func main() {
	if len(os.Args) < 2 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, usage)
		fmt.Fprintln(os.Stderr, "  (run 'dbguard check -h' for flags)")
		os.Exit(2)
	}
	os.Exit(check(os.Args[2:]))
}

type rowsFlag map[string]int64

func (r rowsFlag) String() string { return "" }
func (r rowsFlag) Set(v string) error {
	k, n, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want table=rows, got %q", v)
	}
	rows, err := strconv.ParseInt(n, 10, 64)
	if err != nil {
		return err
	}
	if !strings.Contains(k, ".") {
		k = "public." + k
	}
	r[k] = rows
	return nil
}

// Rows makes rowsFlag a rules.Stats for offline use.
func (r rowsFlag) Rows(schema, table string) (int64, bool) {
	n, ok := r[schema+"."+table]
	return n, ok
}

func check(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage); fs.PrintDefaults() }
	dsnEnv := fs.String("dsn-env", "DBGUARD_DSN", "name of the env var holding a read-only Postgres connection string")
	format := fs.String("format", "text", "output format: text | markdown | json")
	failOn := fs.String("fail-on", "medium-high", "lowest risk that fails the check: low | medium | medium-high | high")
	large := fs.Int64("large-rows", 100_000, "tables with fewer estimated rows are treated as low risk")
	pgVer := fs.Int("pg-version", 0, "assumed PostgreSQL major version when not connected (default 16)")
	rows := rowsFlag{}
	fs.Var(rows, "rows", "offline table size, table=rows (repeatable); used when no DSN is set")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	threshold, err := rules.ParseRisk(*failOn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbguard:", err)
		return 2
	}
	files, err := flyway.Collect(fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbguard:", err)
		return 2
	}

	opts := rules.Options{PGVersion: *pgVer, LargeRows: *large}
	if dsn := os.Getenv(*dsnEnv); dsn != "" {
		st, err := pg.Connect(context.Background(), dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dbguard:", err)
			return 2
		}
		defer st.Close(context.Background())
		opts.Stats = st
		if st.Version > 0 {
			opts.PGVersion = st.Version
		}
	} else if len(rows) > 0 {
		opts.Stats = rows
	} else {
		fmt.Fprintf(os.Stderr, "dbguard: $%s not set and no --rows given; table sizes unknown, assuming large\n", *dsnEnv)
	}

	var out []report.File
	blocking := false
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dbguard:", err)
			return 2
		}
		sql := string(b)
		found, err := rules.Check(sql, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dbguard: %s: %v\n", path, err)
			return 2
		}
		problems := override.Apply(sql, found)
		for _, f := range found {
			blocking = blocking || report.Blocking(f, threshold)
		}
		out = append(out, report.File{Path: path, Findings: found, Problems: problems})
	}

	switch *format {
	case "text":
		report.Text(os.Stdout, out, threshold)
	case "markdown":
		report.Markdown(os.Stdout, out, threshold)
	case "json":
		if err := report.JSON(os.Stdout, out); err != nil {
			fmt.Fprintln(os.Stderr, "dbguard:", err)
			return 2
		}
	default:
		fmt.Fprintf(os.Stderr, "dbguard: unknown format %q\n", *format)
		return 2
	}
	if blocking {
		return 1
	}
	return 0
}
