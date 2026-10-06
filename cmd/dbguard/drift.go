package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OriginalDaniel02/dbguard/internal/drift"
	"github.com/OriginalDaniel02/dbguard/internal/mysqldb"
	"github.com/OriginalDaniel02/dbguard/internal/notify"
	"github.com/OriginalDaniel02/dbguard/internal/pg"
	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// takeSnapshot connects read-only and snapshots the schema. A variable so tests
// can run the commands without a database.
var takeSnapshot = func(ctx context.Context, dsn string) (*snapshot.Schema, error) {
	if mysqldb.IsMySQLDSN(dsn) {
		st, err := mysqldb.Connect(ctx, dsn)
		if err != nil {
			return nil, err
		}
		defer st.Close()
		return st.Snapshot(ctx)
	}
	st, err := pg.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer st.Close(ctx)
	return st.Snapshot(ctx)
}

// sendSlack is likewise replaceable in tests.
var sendSlack = notify.Slack

const maxSlackDiffsPerEnv = 15

func snapshotCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsnEnv := fs.String("dsn-env", "DBGUARD_DSN", "name of the env var holding a read-only Postgres connection string")
	out := fs.String("out", "", "write the snapshot to this file (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dsn := os.Getenv(*dsnEnv)
	if dsn == "" {
		fmt.Fprintf(stderr, "dbguard: $%s is not set\n", *dsnEnv)
		return 2
	}
	s, err := takeSnapshot(context.Background(), dsn)
	if err != nil {
		fmt.Fprintln(stderr, "dbguard:", err)
		return 2
	}
	w := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
		defer f.Close()
		w = f
	}
	if err := snapshot.Write(w, s); err != nil {
		fmt.Fprintln(stderr, "dbguard:", err)
		return 2
	}
	return 0
}

type envFlag [][2]string // name -> env var holding its DSN

func (e *envFlag) String() string { return "" }
func (e *envFlag) Set(v string) error {
	name, varName, ok := strings.Cut(v, "=")
	if !ok || name == "" || varName == "" {
		return fmt.Errorf("want name=ENV_VAR, got %q", v)
	}
	*e = append(*e, [2]string{name, varName})
	return nil
}

type listFlag []string

func (l *listFlag) String() string     { return "" }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type envResult struct {
	Env         string             `json:"env"`
	Differences []drift.Difference `json:"differences"`
	Error       string             `json:"error,omitempty"`
}

func driftCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `usage: dbguard drift (--expected FILE | --baseline-env NAME) --env NAME=ENV_VAR [--env ...] [flags]

Compares each environment's live schema with the expected schema and reports
differences. Exit codes: 0 = no drift, 1 = drift found, 2 = error.

flags:`)
		fs.PrintDefaults()
	}
	var envs envFlag
	var ignores listFlag
	expected := fs.String("expected", "", "snapshot file the environments should match (from 'dbguard snapshot' after migrating a scratch DB)")
	baseline := fs.String("baseline-env", "", "instead of --expected: compare every other --env against this environment")
	ignoreFile := fs.String("ignore-file", "", "file with one ignore pattern per line (# comments allowed)")
	slackEnv := fs.String("slack-env", "", "name of the env var holding a Slack incoming-webhook URL; alerts when drift is found")
	stateFlag := fs.String("state-file", "", "remember what was alerted (JSON); Slack is only notified when drift is new or changed, and when it is resolved. Persist this file between runs (e.g. actions/cache)")
	realert := fs.Duration("realert-after", 7*24*time.Hour, "with --state-file: remind about drift that stays unresolved after this long (0 = never remind)")
	saveDir := fs.String("save-dir", "", "write each environment's snapshot to <dir>/<env>/<UTC timestamp>.json (history)")
	format := fs.String("format", "text", "output format: text | json")
	fs.Var(&envs, "env", "environment to check as name=ENV_VAR (the env var holds its read-only DSN); repeatable")
	fs.Var(&ignores, "ignore", "glob of schema.table or schema.table.object to ignore, e.g. 'public.feature_flags' (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(envs) == 0 || (*expected == "") == (*baseline == "") {
		fs.Usage()
		return 2
	}
	if *ignoreFile != "" {
		pats, err := readIgnoreFile(*ignoreFile)
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
		ignores = append(ignores, pats...)
	}
	opts := drift.Options{Ignore: ignores}
	ctx := context.Background()

	// Snapshot every environment (credentials are only ever read from the environment).
	snaps := map[string]*snapshot.Schema{}
	results := map[string]*envResult{}
	var order []string
	failed := false
	for _, e := range envs {
		name, varName := e[0], e[1]
		order = append(order, name)
		results[name] = &envResult{Env: name}
		dsn := os.Getenv(varName)
		if dsn == "" {
			results[name].Error = fmt.Sprintf("$%s is not set", varName)
			failed = true
			continue
		}
		s, err := takeSnapshot(ctx, dsn)
		if err != nil {
			results[name].Error = err.Error()
			failed = true
			continue
		}
		snaps[name] = s
		if *saveDir != "" {
			if err := saveSnapshot(*saveDir, name, s); err != nil {
				fmt.Fprintln(stderr, "dbguard: warning: could not save snapshot:", err)
			}
		}
	}

	var want *snapshot.Schema
	if *expected != "" {
		f, err := os.Open(*expected)
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
		want, err = snapshot.Read(f)
		f.Close()
		if err != nil {
			fmt.Fprintln(stderr, "dbguard:", err)
			return 2
		}
	} else if want = snaps[*baseline]; want == nil {
		fmt.Fprintf(stderr, "dbguard: baseline environment %q was not snapshotted successfully\n", *baseline)
		return 2
	}

	drifted := false
	for _, name := range order {
		if name == *baseline || snaps[name] == nil {
			continue
		}
		if want.Engine != "" && snaps[name].Engine != "" && want.Engine != snaps[name].Engine {
			results[name].Error = fmt.Sprintf("this environment is %s but the expected schema is %s; they cannot be compared", snaps[name].Engine, want.Engine)
			failed = true
			continue
		}
		results[name].Differences = drift.Compare(want, snaps[name], opts)
		drifted = drifted || len(results[name].Differences) > 0
	}

	switch *format {
	case "json":
		var all []envResult
		for _, n := range order {
			r := *results[n]
			if r.Differences == nil {
				r.Differences = []drift.Difference{}
			}
			all = append(all, r)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(all)
	case "text":
		writeText(stdout, order, results, *baseline)
	default:
		fmt.Fprintf(stderr, "dbguard: unknown format %q\n", *format)
		return 2
	}

	var state stateFile
	if *stateFlag != "" {
		var err error
		if state, err = loadState(*stateFlag); err != nil {
			fmt.Fprintln(stderr, "dbguard: warning: ignoring unreadable state file:", err)
			state = stateFile{}
		}
	}
	plan := planAlerts(state, order, results, *baseline, nowFn(), *realert)
	delivered := true
	if *slackEnv != "" && !plan.empty() {
		if url := os.Getenv(*slackEnv); url == "" {
			fmt.Fprintf(stderr, "dbguard: warning: $%s is not set; no Slack alert sent\n", *slackEnv)
			delivered = false
		} else if err := sendSlack(ctx, url, slackMessage(order, results, *baseline, plan)); err != nil {
			fmt.Fprintln(stderr, "dbguard: warning: Slack alert failed:", err)
			delivered = false // keep state unchanged so the alert is retried next run
		}
	}
	if state != nil && delivered {
		state.apply(order, results, *baseline, plan, nowFn())
		if err := state.save(*stateFlag); err != nil {
			fmt.Fprintln(stderr, "dbguard: warning: could not save state file:", err)
		}
	}

	switch {
	case failed:
		return 2
	case drifted:
		return 1
	}
	return 0
}

func writeText(w io.Writer, order []string, results map[string]*envResult, baseline string) {
	for _, n := range order {
		r := results[n]
		switch {
		case n == baseline:
			fmt.Fprintf(w, "%s: baseline\n", n)
		case r.Error != "":
			fmt.Fprintf(w, "%s: ERROR %s\n", n, r.Error)
		case len(r.Differences) == 0:
			fmt.Fprintf(w, "%s: no drift\n", n)
		default:
			fmt.Fprintf(w, "%s: %d difference(s)\n", n, len(r.Differences))
			for _, d := range r.Differences {
				fmt.Fprintf(w, "  - %s\n", d)
			}
		}
	}
}

func slackMessage(order []string, results map[string]*envResult, baseline string, p alertPlan) string {
	var b strings.Builder
	if len(p.New) > 0 || len(p.Reminder) > 0 {
		b.WriteString(":warning: *DB Guard: schema drift detected*\n")
		for _, n := range order {
			r := results[n]
			_, remind := p.Reminder[n]
			if !p.New[n] && !remind {
				continue
			}
			fmt.Fprintf(&b, "\n*%s* - %d difference(s)", n, len(r.Differences))
			if remind {
				fmt.Fprintf(&b, " (still unresolved since %s)", p.Reminder[n].UTC().Format("2006-01-02"))
			}
			b.WriteString("\n")
			for i, d := range r.Differences {
				if i == maxSlackDiffsPerEnv {
					fmt.Fprintf(&b, "...and %d more\n", len(r.Differences)-maxSlackDiffsPerEnv)
					break
				}
				fmt.Fprintf(&b, "• `%s` %s\n", d.Table, d)
			}
		}
		b.WriteString("\nReconcile by writing a migration that formalizes the change, or revert the manual one.")
	}
	for _, n := range p.Resolved {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, ":white_check_mark: *DB Guard: drift resolved* - %s now matches the expected schema.", n)
	}
	return b.String()
}

func saveSnapshot(dir, env string, s *snapshot.Schema) error {
	d := filepath.Join(dir, env)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(d, time.Now().UTC().Format("20060102T150405Z")+".json"))
	if err != nil {
		return err
	}
	defer f.Close()
	return snapshot.Write(f, s)
}

func readIgnoreFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out, sc.Err()
}
