// Command dbguard checks database migrations for risky locking operations.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/OriginalDaniel02/dbguard/internal/mysqldb"
	"github.com/OriginalDaniel02/dbguard/internal/pg"
	"github.com/OriginalDaniel02/dbguard/internal/report"
	"github.com/OriginalDaniel02/dbguard/internal/rules"
)

const usage = `usage: dbguard <command> [flags]

commands:
  check     check Flyway or Liquibase migrations for risky locking operations
  snapshot  capture a read-only schema snapshot as JSON
  drift     compare live environments with the expected schema
  gitlab-comment  post/update the DB Guard comment on a GitLab merge request

usage of check: dbguard check [flags] <migration-file-or-dir>...
Exit codes: 0 = no blocking findings, 1 = blocking findings, 2 = error.

flags of check:`

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "check":
			os.Exit(check(os.Args[2:], os.Stdout, os.Stderr))
		case "snapshot":
			os.Exit(snapshotCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "drift":
			os.Exit(driftCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "gitlab-comment":
			os.Exit(gitlabCommentCmd(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	fmt.Fprintln(os.Stderr, usage)
	fmt.Fprintln(os.Stderr, "  (run 'dbguard <command> -h' for flags)")
	os.Exit(2)
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
	r[k] = rows
	return nil
}

// Rows makes rowsFlag a rules.Stats for offline use.
func (r rowsFlag) Rows(schema, table string) (int64, bool) {
	if n, ok := r[schema+"."+table]; ok {
		return n, true
	}
	n, ok := r[table] // bare name: applies to whichever schema is in effect
	return n, ok
}

type kvFlag map[string]string

func (k kvFlag) String() string { return "" }
func (k kvFlag) Set(v string) error {
	name, val, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return fmt.Errorf("want name=value, got %q", v)
	}
	k[name] = val
	return nil
}

func check(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, usage); fs.PrintDefaults() }
	dsnEnv := fs.String("dsn-env", "DBGUARD_DSN", "name of the env var holding a read-only PostgreSQL or MySQL connection string")
	format := fs.String("format", "text", "output format: text | markdown | json")
	failOn := fs.String("fail-on", "medium-high", "lowest risk that fails the check: low | medium | medium-high | high")
	large := fs.Int64("large-rows", 100_000, "tables with fewer estimated rows are treated as low risk")
	pgVer := fs.Int("pg-version", 0, "assumed PostgreSQL major version when not connected (default 16)")
	engineFlag := fs.String("engine", "", "database engine: postgres | mysql (default: from the connection string, else postgres)")
	myVer := fs.String("mysql-version", "", "assumed MySQL version when not connected, e.g. 8.0.28 (default 8.0.36)")
	schema := fs.String("schema", "", "schema for unqualified table names (default: connection's current_schema, else public)")
	placeholders := kvFlag{}
	fs.Var(placeholders, "placeholder", "Flyway placeholder value, name=value (repeatable); unset ${placeholders} are treated as opaque")
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
		fmt.Fprintln(stderr, "dbguard:", err)
		return 2
	}
	files, err := collect(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, "dbguard:", err)
		return 2
	}

	opts := rules.Options{PGVersion: *pgVer, LargeRows: *large, DefaultSchema: *schema, Placeholders: placeholders}
	dsn := os.Getenv(*dsnEnv)
	engine := *engineFlag
	if engine == "" {
		engine = "postgres"
		if dsn != "" && mysqldb.IsMySQLDSN(dsn) {
			engine = "mysql"
		}
	}
	if engine != "postgres" && engine != "mysql" {
		fmt.Fprintf(stderr, "dbguard: unknown --engine %q (want postgres or mysql)\n", engine)
		return 2
	}
	if dsn != "" && (engine == "mysql") != mysqldb.IsMySQLDSN(dsn) {
		fmt.Fprintf(stderr, "dbguard: the connection string in $%s is not a %s connection string\n", *dsnEnv, engine)
		return 2
	}
	if *myVer != "" {
		v, _ := mysqldb.ParseVersion(*myVer)
		if v == 0 {
			fmt.Fprintf(stderr, "dbguard: cannot read --mysql-version %q (want e.g. 8.0.28)\n", *myVer)
			return 2
		}
		opts.MySQLVersion = v
	}
	switch {
	case dsn != "" && engine == "mysql":
		st, err := mysqldb.Connect(context.Background(), dsn)
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
		defer st.Close()
		opts.Stats = st
		if st.Version > 0 {
			opts.MySQLVersion = st.Version
		}
		if opts.DefaultSchema == "" {
			opts.DefaultSchema = st.Schema
		}
		if st.Flavor == "MariaDB" {
			fmt.Fprintln(stderr, "dbguard: warning: connected to MariaDB; the MySQL rules target MySQL 8.0 and MariaDB's online DDL differs")
		}
	case dsn != "":
		st, err := pg.Connect(context.Background(), dsn)
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
		defer st.Close(context.Background())
		opts.Stats = st
		if st.Version > 0 {
			opts.PGVersion = st.Version
		}
		if opts.DefaultSchema == "" {
			opts.DefaultSchema = st.Schema
		}
	case len(rows) > 0:
		opts.Stats = rows
	default:
		fmt.Fprintf(stderr, "dbguard: $%s not set and no --rows given; table sizes unknown, assuming large\n", *dsnEnv)
	}

	var out []report.File
	blocking := false
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
		found, problems, err := analyze(path, string(b), opts, engine)
		if err != nil {
			fmt.Fprintf(stderr, "dbguard: %s: %v\n", path, err)
			return 2
		}
		for _, f := range found {
			blocking = blocking || report.Blocking(f, threshold)
		}
		out = append(out, report.File{Path: path, Findings: found, Problems: problems})
	}

	switch *format {
	case "text":
		report.Text(stdout, out, threshold)
	case "markdown":
		report.Markdown(stdout, out, threshold)
	case "json":
		if err := report.JSON(stdout, out); err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "dbguard: unknown format %q\n", *format)
		return 2
	}
	if blocking {
		return 1
	}
	return 0
}
