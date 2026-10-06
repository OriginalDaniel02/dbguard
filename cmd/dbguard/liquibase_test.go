package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/report"
)

func lqFile(name string) string { return filepath.Join("..", "..", "testdata", "liquibase", name) }

// summary reduces a report to "rule:acknowledged?" so formats can be compared.
func summary(t *testing.T, path string) (summaries []string, files []report.File, code int) {
	t.Helper()
	t.Setenv("DBGUARD_DSN", "")
	var o, e bytes.Buffer
	code = check([]string{"--format", "json", "--rows", "transactions=14000000", path}, &o, &e)
	if err := json.Unmarshal(o.Bytes(), &files); err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, o.String(), e.String())
	}
	for _, f := range files {
		for _, x := range f.Findings {
			s := x.Rule
			if x.Override != "" {
				s += ":acknowledged"
			}
			summaries = append(summaries, s)
		}
	}
	sort.Strings(summaries)
	return summaries, files, code
}

func TestLiquibaseAllFormatsReportTheSameFindings(t *testing.T) {
	want := []string{
		"add-column-nonconstant-default", // changeSet 3: random()
		"add-foreign-key",                // changeSet 8: addLookupTable adds a validated FK on the big table
		"add-not-null:acknowledged",      // changeSet 5: override (comment above / changeSet comment)
		"add-unique-or-pk",               // changeSet 6
		"alter-column-type",              // changeSet 6
		"create-index",                   // changeSet 4
		"create-index:acknowledged",      // changeSet 10: override in the changeSet's comment
		"drop-column",                    // changeSet 6
	}
	// Not reported: changeSet 1 (index on a table created in the same changelog),
	// created_at DEFAULT ${now} (resolved from <property> to now(): metadata-only),
	// FOREIGN KEY validate=false (NOT VALID), CREATE INDEX CONCURRENTLY, the oracle-only changeSet.
	for _, f := range []string{"changelog.xml", "changelog.yaml", "changelog.json"} {
		got, files, code := summary(t, lqFile(f))
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s findings:\n got  %v\n want %v", f, got, want)
		}
		if code != 1 {
			t.Errorf("%s: blocking findings must exit 1, got %d", f, code)
		}
		var skipped bool
		for _, p := range files[0].Problems {
			skipped = skipped || (strings.Contains(p, "mergeColumns") && strings.Contains(p, "not analyzed"))
		}
		if !skipped {
			t.Errorf("%s: unanalyzed change type must be listed: %v", f, files[0].Problems)
		}
	}
}

func TestLiquibaseFindingsPointAtTheChangeSetLine(t *testing.T) {
	_, files, _ := summary(t, lqFile("changelog.xml"))
	raw, _ := os.ReadFile(lqFile("changelog.xml"))
	lines := strings.Split(string(raw), "\n")
	for _, f := range files[0].Findings {
		if !strings.Contains(lines[f.Line-1], "<changeSet") {
			t.Errorf("%s reported at line %d: %q", f.Rule, f.Line, lines[f.Line-1])
		}
	}
}

func TestLiquibaseAlternativeForIndexMentionsRunInTransaction(t *testing.T) {
	_, files, _ := summary(t, lqFile("changelog.xml"))
	for _, f := range files[0].Findings {
		if f.Rule == "create-index" && f.Override == "" {
			if !strings.Contains(f.Alternative, "runInTransaction") || strings.Contains(f.Alternative, "Flyway") {
				t.Errorf("Liquibase advice must not tell people to configure Flyway: %q", f.Alternative)
			}
		}
	}
}

func TestLiquibaseFormattedSQL(t *testing.T) {
	got, files, code := summary(t, lqFile("formatted.sql"))
	want := []string{"add-column-nonconstant-default", "create-index"}
	if strings.Join(got, ",") != strings.Join(want, ",") || code != 1 {
		t.Fatalf("got %v code=%d, want %v", got, code, want)
	}
	for _, f := range files[0].Findings {
		if f.Rule == "create-index" && !strings.Contains(f.Alternative, "runInTransaction") {
			t.Errorf("formatted SQL is Liquibase: advice should be too: %q", f.Alternative)
		}
	}
}

func TestLiquibaseDirectoryDiscoveryByContent(t *testing.T) {
	d := t.TempDir()
	copyFile := func(src, dst string) {
		b, err := os.ReadFile(lqFile(src))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, dst), b, 0o644)
	}
	copyFile("changelog.xml", "changelog.xml")
	copyFile("formatted.sql", "formatted.sql")
	os.WriteFile(filepath.Join(d, "pom.xml"), []byte("<project/>"), 0o644)
	os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"unrelated": true}`), 0o644)
	os.WriteFile(filepath.Join(d, "notes.sql"), []byte("select 1;"), 0o644)
	os.WriteFile(filepath.Join(d, "V1__flyway.sql"), []byte("create table t (a int);"), 0o644)

	files, err := collect([]string{d})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "V1__flyway.sql,changelog.xml,formatted.sql" {
		t.Fatalf("directory walk must pick changelogs by content, not extension alone: %s", got)
	}
}

func TestLiquibaseBrokenAndEmptyInputs(t *testing.T) {
	d := t.TempDir()
	bad := filepath.Join(d, "bad.xml")
	os.WriteFile(bad, []byte("<databaseChangeLog><changeSet"), 0o644)
	t.Setenv("DBGUARD_DSN", "")
	var o, e bytes.Buffer
	if code := check([]string{bad}, &o, &e); code != 2 {
		t.Errorf("malformed XML must exit 2, got %d", code)
	}
	reasonless := filepath.Join(d, "r.yaml")
	os.WriteFile(reasonless, []byte("databaseChangeLog:\n  - changeSet:\n      id: '1'\n      author: a\n      comment: 'dbguard:ignore create-index'\n      changes:\n        - createIndex:\n            tableName: big\n            indexName: i\n            columns:\n              - column:\n                  name: a\n"), 0o644)
	o.Reset()
	code := check([]string{"--format", "json", "--rows", "big=14000000", reasonless}, &o, &e)
	if code != 1 || !strings.Contains(o.String(), "needs a 'reason") {
		t.Errorf("an ignore without a reason must not unblock, and must be reported: code=%d %s", code, o.String())
	}
}

func TestExplicitNonChangelogFilesAreSkippedNotErrors(t *testing.T) {
	d := t.TempDir()
	cfg := filepath.Join(d, "settings.json")
	os.WriteFile(cfg, []byte(`{"a": 1}`), 0o644)
	pom := filepath.Join(d, "pom.xml")
	os.WriteFile(pom, []byte("<project/>"), 0o644)
	sql := filepath.Join(d, "custom_name.sql")
	os.WriteFile(sql, []byte("create table t (a int);"), 0o644)

	files, err := collect([]string{cfg, pom, sql, lqFile("changelog.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "changelog.yaml,custom_name.sql" {
		t.Fatalf("CI passes changed files explicitly; non-changelog XML/JSON must be skipped, SQL always checked: %s", got)
	}
}

func TestLiquibaseOnMySQL(t *testing.T) {
	t.Setenv("DBGUARD_DSN", "")
	var o, e bytes.Buffer
	code := check([]string{"--engine", "mysql", "--format", "json", "--rows", "orders=14000000", lqFile("mysql-changelog.xml")}, &o, &e)
	var files []report.File
	if err := json.Unmarshal(o.Bytes(), &files); err != nil {
		t.Fatalf("%v\n%s\n%s", err, o.String(), e.String())
	}
	var got []string
	for _, f := range files[0].Findings {
		s := f.Rule
		if f.Override != "" {
			s += ":acknowledged"
		}
		got = append(got, s)
	}
	sort.Strings(got)
	want := []string{
		"add-column-nonconstant-default", // changeSet 3: DEFAULT (UUID()) is an expression default
		"add-foreign-key",                // changeSet 7: foreign_key_checks=1 copies the table
		"add-foreign-key",                // changeSet 10: addLookupTable adds a validated FK on the big table
		"alter-column-type",              // changeSet 5: modifyDataType
		"alter-column-type",              // changeSet 6: offline, MODIFY is assumed to change the type
		"create-index",                   // changeSet 4 (online: low)
		"create-index",                   // changeSet 8: unique (online: low)
		"create-index:acknowledged",      // changeSet 12: override in the changeSet comment
		"drop-column",                    // changeSet 8
	}
	// Not reported: createTable + its index (new table), ${now} -> CURRENT_TIMESTAMP default (INSTANT),
	// ADD PRIMARY KEY on the table created in changeSet 1, the lookup table's own steps, the explicit
	// ALGORITHM=INSTANT statement, the postgresql-only changeSet.
	if strings.Join(got, "\n") != strings.Join(want, "\n") || code != 1 {
		t.Fatalf("code=%d\n got  %v\n want %v\nproblems: %v", code, got, want, files[0].Problems)
	}
	raw, _ := os.ReadFile(lqFile("mysql-changelog.xml"))
	lines := strings.Split(string(raw), "\n")
	for _, f := range files[0].Findings {
		if !strings.Contains(lines[f.Line-1], "<changeSet") {
			t.Errorf("%s reported at line %d: %q", f.Rule, f.Line, lines[f.Line-1])
		}
	}
}
