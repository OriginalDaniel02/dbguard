package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OriginalDaniel02/dbguard/internal/changelog"
	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

func clDay(n int) time.Time { return time.Date(2026, 10, n, 6, 0, 0, 0, time.UTC) }

func saveSnap(t *testing.T, dir, env string, at time.Time, s *snapshot.Schema) {
	t.Helper()
	d := filepath.Join(dir, env)
	os.MkdirAll(d, 0o755)
	f, err := os.Create(filepath.Join(d, at.Format(changelog.TimeLayout)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	snapshot.Write(f, s)
}

func clHistory(t *testing.T) string {
	dir := t.TempDir()
	mk := func(cols ...snapshot.Column) *snapshot.Schema {
		s := &snapshot.Schema{Engine: "postgres", Tables: []snapshot.Table{{Schema: "public", Name: "orders", Columns: cols}}}
		s.Normalize()
		return s
	}
	status := func(typ string) snapshot.Column { return snapshot.Column{Name: "status", Type: typ} }
	saveSnap(t, dir, "production", clDay(1), mk(colID, status("text")))
	saveSnap(t, dir, "production", clDay(2), mk(colID, status("text"), snapshot.Column{Name: "discount", Type: "numeric"}))
	saveSnap(t, dir, "production", clDay(3), mk(colID, status("varchar(20)"), snapshot.Column{Name: "discount", Type: "numeric"}))
	return dir
}

func runChangelog(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	return changelogCmd(args, &o, &e), o.String(), e.String()
}

func TestChangelogCommandAnswersWhenTheTypeChanged(t *testing.T) {
	dir := clHistory(t)
	code, out, errOut := runChangelog(t, "--dir", dir, "--table", "orders", "--object", "status", "--kind", "column-type-changed")
	if code != 0 || !strings.Contains(out, "type changed: text -> varchar(20)") || !strings.Contains(out, "2026-10-03 06:00") ||
		!strings.Contains(out, "detected between 2026-10-02 06:00 and 2026-10-03 06:00 UTC") || strings.Contains(out, "discount") {
		t.Fatalf("code=%d\n%s\n%s", code, out, errOut)
	}
}

func TestChangelogCommandFiltersFormatsAndLimit(t *testing.T) {
	dir := clHistory(t)
	_, out, _ := runChangelog(t, "--dir", dir, "--format", "json")
	var entries []changelog.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 2 {
		t.Fatalf("%v %s", err, out)
	}
	if entries[0].Kind != "column-type-changed" {
		t.Errorf("newest first: %+v", entries)
	}
	if _, out, _ := runChangelog(t, "--dir", dir, "--limit", "1", "--format", "json"); strings.Count(out, `"kind"`) != 1 {
		t.Errorf("--limit: %s", out)
	}
	if _, out, _ := runChangelog(t, "--dir", dir, "--search", "numeric"); !strings.Contains(out, "discount") || strings.Contains(out, "status") {
		t.Errorf("--search: %s", out)
	}
	if _, out, _ := runChangelog(t, "--dir", dir, "--action", "added", "--format", "markdown"); !strings.Contains(out, "# Schema changelog") || !strings.Contains(out, "discount") {
		t.Errorf("markdown: %s", out)
	}
	if _, out, _ := runChangelog(t, "--dir", dir, "--env", "staging"); !strings.Contains(out, "no matching schema changes") {
		t.Errorf("no match: %s", out)
	}
	ign := filepath.Join(t.TempDir(), "ignore")
	os.WriteFile(ign, []byte("public.orders.discount\n"), 0o644)
	if _, out, _ := runChangelog(t, "--dir", dir, "--ignore-file", ign, "--format", "json"); strings.Contains(out, "discount") {
		t.Errorf("ignore file: %s", out)
	}
}

func TestChangelogDatesAbsoluteAndRelative(t *testing.T) {
	dir := clHistory(t)
	old := nowFn
	nowFn = func() time.Time { return clDay(4) }
	defer func() { nowFn = old }()

	count := func(args ...string) int {
		_, out, _ := runChangelog(t, append([]string{"--dir", dir, "--format", "json"}, args...)...)
		return strings.Count(out, `"kind"`)
	}
	if got := count("--since", "2026-10-03"); got != 1 {
		t.Errorf("since an absolute date: %d", got)
	}
	if got := count("--since", "1d"); got != 1 { // one day before day 4 06:00 = day 3 06:00
		t.Errorf("since 1d: %d", got)
	}
	if got := count("--since", "10d"); got != 2 {
		t.Errorf("since 10d: %d", got)
	}
	if got := count("--until", "2026-10-02"); got != 1 {
		t.Errorf("a bare --until date includes that whole day: %d", got)
	}
	if got := count("--since", "2026-10-02", "--until", "2026-10-03"); got != 2 {
		t.Errorf("range: %d", got)
	}
}

func TestChangelogCommandErrors(t *testing.T) {
	dir := clHistory(t)
	for name, args := range map[string][]string{
		"no dir":          {},
		"missing dir":     {"--dir", filepath.Join(dir, "nope")},
		"bad since":       {"--dir", dir, "--since", "last tuesday"},
		"bad until":       {"--dir", dir, "--until", "soon"},
		"bad format":      {"--dir", dir, "--format", "xml"},
		"empty directory": {"--dir", t.TempDir()},
	} {
		if code, _, _ := runChangelog(t, args...); code != 2 {
			t.Errorf("%s: want exit 2, got %d", name, code)
		}
	}
}

// The producer and the consumer must agree: the layout `drift --save-dir` writes is what
// `changelog` reads.
func TestChangelogReadsWhatDriftSaves(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROD_DSN", "dsn")
	exp := expectedFile(t, schemaWith(colID, colEmail))

	fakeDB(t, map[string]*snapshot.Schema{"dsn": schemaWith(colID, colEmail)})
	runDrift(t, "--expected", exp, "--env", "production=PROD_DSN", "--save-dir", dir)
	time.Sleep(1100 * time.Millisecond)                                                // snapshot file names have 1-second resolution
	fakeDB(t, map[string]*snapshot.Schema{"dsn": schemaWith(colID, colEmail, colHot)}) // a manual ALTER happens
	runDrift(t, "--expected", exp, "--env", "production=PROD_DSN", "--save-dir", dir)

	code, out, errOut := runChangelog(t, "--dir", dir, "--format", "json")
	if code != 0 || !strings.Contains(out, "hotfix_flag") || !strings.Contains(out, `"action": "added"`) {
		t.Fatalf("the changelog must read snapshots saved by the drift command: code=%d out=%s err=%s", code, out, errOut)
	}
}
