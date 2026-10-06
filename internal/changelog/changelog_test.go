package changelog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OriginalDaniel02/dbguard/internal/drift"
	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

func table(name string, cols ...snapshot.Column) snapshot.Table {
	return snapshot.Table{Schema: "public", Name: name, Columns: cols}
}

func schema(tables ...snapshot.Table) *snapshot.Schema {
	s := &snapshot.Schema{Engine: "postgres", Tables: tables}
	s.Normalize()
	return s
}

func save(t *testing.T, dir, env string, at time.Time, s *snapshot.Schema) {
	t.Helper()
	d := filepath.Join(dir, env)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(d, at.UTC().Format(TimeLayout)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := snapshot.Write(f, s); err != nil {
		t.Fatal(err)
	}
}

var (
	id      = snapshot.Column{Name: "id", Type: "bigint", NotNull: true}
	email   = snapshot.Column{Name: "email", Type: "text"}
	day     = func(n int) time.Time { return time.Date(2026, 10, n, 6, 0, 0, 0, time.UTC) }
	history = func(t *testing.T) string {
		dir := t.TempDir()
		// production: day 1 baseline; day 2 adds a column; day 3 changes its type; day 4 drops the table "legacy".
		save(t, dir, "production", day(1), schema(table("accounts", id, email), table("legacy", id)))
		save(t, dir, "production", day(2), schema(table("accounts", id, email, snapshot.Column{Name: "plan", Type: "text"}), table("legacy", id)))
		save(t, dir, "production", day(3), schema(table("accounts", id, email, snapshot.Column{Name: "plan", Type: "varchar(20)"}), table("legacy", id)))
		save(t, dir, "production", day(4), schema(table("accounts", id, email, snapshot.Column{Name: "plan", Type: "varchar(20)"})))
		// staging: only day 1 and 3, so its change spans two days.
		save(t, dir, "staging", day(1), schema(table("accounts", id, email)))
		save(t, dir, "staging", day(3), schema(table("accounts", id, email), table("scratch", id)))
		return dir
	}
)

func build(t *testing.T, dir string) []Entry {
	t.Helper()
	snaps, warns, err := LoadDir(dir)
	if err != nil || len(warns) != 0 {
		t.Fatalf("load: %v %v", err, warns)
	}
	return Build(snaps, drift.Options{})
}

func TestAnswersWhenDidTheColumnTypeChange(t *testing.T) {
	entries := build(t, history(t))
	got := Filter(entries, Query{Table: "accounts", Object: "plan", Kind: drift.ColumnTypeChanged})
	if len(got) != 1 {
		t.Fatalf("want exactly one type change, got %+v", got)
	}
	e := got[0]
	if !e.Time.Equal(day(3)) || !e.PrevTime.Equal(day(2)) || e.Env != "production" {
		t.Errorf("the change was detected on day 3, between day 2 and day 3: %+v", e)
	}
	if e.Before != "text" || e.After != "varchar(20)" || !strings.Contains(e.Change, "type changed: text -> varchar(20)") {
		t.Errorf("before/after: %+v", e)
	}
}

func TestEveryKindOfChangeIsRecordedWithTheRightWindow(t *testing.T) {
	entries := build(t, history(t))
	want := map[string]struct {
		env    string
		action string
		day    int
	}{
		"public.accounts|plan|column-extra":        {"production", "added", 2},
		"public.accounts|plan|column-type-changed": {"production", "changed", 3},
		"public.legacy||table-missing":             {"production", "dropped", 4},
		"public.scratch||table-extra":              {"staging", "added", 3},
	}
	if len(entries) != len(want) {
		t.Fatalf("want %d entries, got %d: %+v", len(want), len(entries), entries)
	}
	for _, e := range entries {
		w, ok := want[e.Table+"|"+e.Object+"|"+e.Kind]
		if !ok || w.env != e.Env || w.action != e.Action || !e.Time.Equal(day(w.day)) {
			t.Errorf("unexpected entry %+v", e)
		}
	}
	for _, e := range entries {
		if e.Env == "staging" && !e.PrevTime.Equal(day(1)) {
			t.Errorf("staging's window spans day 1 to 3 because day 2 was not snapshotted: %+v", e)
		}
	}
}

func TestNewestFirstAndFirstSnapshotIsOnlyABaseline(t *testing.T) {
	entries := build(t, history(t))
	for i := 1; i < len(entries); i++ {
		if entries[i].Time.After(entries[i-1].Time) {
			t.Fatalf("entries must be newest first: %v then %v", entries[i-1].Time, entries[i].Time)
		}
	}
	for _, e := range entries {
		if e.Time.Equal(day(1)) {
			t.Errorf("the first snapshot is a baseline and produces no changes: %+v", e)
		}
	}
	// A single snapshot has no history.
	one := t.TempDir()
	save(t, one, "production", day(1), schema(table("a", id)))
	if got := build(t, one); len(got) != 0 {
		t.Errorf("one snapshot, no changes: %+v", got)
	}
}

func TestFiltersAndSearch(t *testing.T) {
	entries := build(t, history(t))
	cases := []struct {
		name string
		q    Query
		want int
	}{
		{"env", Query{Env: "staging"}, 1},
		{"table exact (unqualified)", Query{Table: "accounts"}, 2},
		{"table glob", Query{Table: "public.l*"}, 1},
		{"object glob", Query{Object: "pl*"}, 2},
		{"kind noun", Query{Kind: "column"}, 2},
		{"kind exact", Query{Kind: drift.TableMissing}, 1},
		{"action", Query{Action: "dropped"}, 1},
		{"text in change", Query{Text: "VARCHAR"}, 1},
		{"text in table name", Query{Text: "scratch"}, 1},
		{"since", Query{Since: day(3)}, 3},
		{"until", Query{Until: day(2)}, 1},
		{"since+until", Query{Since: day(3), Until: day(3)}, 2},
		{"no match", Query{Text: "nonexistent"}, 0},
		{"everything", Query{}, 4},
	}
	for _, c := range cases {
		if got := Filter(entries, c.q); len(got) != c.want {
			t.Errorf("%s: got %d, want %d", c.name, len(got), c.want)
		}
	}
}

func TestIgnorePatternsApplyToTheHistory(t *testing.T) {
	snaps, _, _ := LoadDir(history(t))
	got := Build(snaps, drift.Options{Ignore: []string{"public.scratch", "public.legacy"}})
	if len(got) != 2 {
		t.Fatalf("scratch and legacy are ignored: %+v", got)
	}
}

func TestStrayFilesAreSkippedWithWarnings(t *testing.T) {
	dir := history(t)
	os.WriteFile(filepath.Join(dir, "production", "notes.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(dir, "production", "20261009T060000Z.json"), []byte(`not json`), 0o644)
	snaps, warns, err := LoadDir(dir)
	if err != nil || len(warns) != 2 {
		t.Fatalf("err=%v warnings=%v", err, warns)
	}
	if len(snaps) != 6 {
		t.Errorf("the six real snapshots must still load, got %d", len(snaps))
	}
	if _, _, err := LoadDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing directory is an error")
	}
}

func TestSnapshotsDirectlyInTheDirBelongToDefaultEnv(t *testing.T) {
	dir := t.TempDir()
	for i, s := range []*snapshot.Schema{schema(table("a", id)), schema(table("a", id, email))} {
		f, _ := os.Create(filepath.Join(dir, day(i+1).Format(TimeLayout)+".json"))
		snapshot.Write(f, s)
		f.Close()
	}
	got := build(t, dir)
	if len(got) != 1 || got[0].Env != "default" || got[0].Action != "added" {
		t.Fatalf("%+v", got)
	}
}

func TestMySQLNamesHaveNoSchemaPrefix(t *testing.T) {
	dir := t.TempDir()
	a := &snapshot.Schema{Engine: "mysql", Tables: []snapshot.Table{{Name: "orders", Columns: []snapshot.Column{id}}}}
	b := &snapshot.Schema{Engine: "mysql", Tables: []snapshot.Table{{Name: "orders", Columns: []snapshot.Column{id, {Name: "note", Type: "varchar(10)"}}}}}
	save(t, dir, "prod", day(1), a)
	save(t, dir, "prod", day(2), b)
	got := build(t, dir)
	if len(got) != 1 || got[0].Table != "orders" || !strings.Contains(got[0].Change, "column note added (varchar(10))") {
		t.Fatalf("%+v", got)
	}
	if len(Filter(got, Query{Table: "orders"})) != 1 {
		t.Error("an unqualified table name must match")
	}
}

func TestRendering(t *testing.T) {
	entries := build(t, history(t))
	var b bytes.Buffer
	Text(&b, entries)
	for _, want := range []string{"public.accounts", "column plan type changed: text -> varchar(20)", "detected between 2026-10-02 06:00 and 2026-10-03 06:00 UTC", "4 change(s) in 3 object(s)"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("text missing %q:\n%s", want, b.String())
		}
	}
	b.Reset()
	Markdown(&b, entries)
	if !strings.Contains(b.String(), "## `public.accounts`") || !strings.Contains(b.String(), "| 2026-10-03 06:00 | production |") {
		t.Errorf("markdown:\n%s", b.String())
	}
	b.Reset()
	if err := JSON(&b, nil); err != nil || strings.TrimSpace(b.String()) != "[]" {
		t.Errorf("an empty result must be [] and not null: %q", b.String())
	}
	b.Reset()
	Text(&b, nil)
	if !strings.Contains(b.String(), "no matching schema changes") {
		t.Errorf("empty text: %q", b.String())
	}
}
