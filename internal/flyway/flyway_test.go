package flyway

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIsMigration(t *testing.T) {
	good := []string{"V1__init.sql", "V2.1__add_col.sql", "V10_3__x.sql", "R__views.sql"}
	bad := []string{"init.sql", "V1_init.sql", "V__x.sql", "V1__x.txt", "v1__x.sql", "README.md"}
	for _, n := range good {
		if !IsMigration(n) {
			t.Errorf("%s should be a migration", n)
		}
	}
	for _, n := range bad {
		if IsMigration(n) {
			t.Errorf("%s should not be a migration", n)
		}
	}
}

func TestCollect(t *testing.T) {
	d := t.TempDir()
	must := func(p string) string {
		full := filepath.Join(d, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("select 1;"), 0o644)
		return full
	}
	a, b := must("db/V1__a.sql"), must("db/sub/V2__b.sql")
	must("db/notes.sql") // not Flyway-named: skipped when walking a directory
	odd := must("other/custom.sql")

	got, err := Collect([]string{filepath.Join(d, "db")})
	if err != nil || !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("dir walk: %v %v", got, err)
	}
	// An explicit file is taken as-is, even without Flyway naming (CI passes changed files).
	got, _ = Collect([]string{odd})
	if !reflect.DeepEqual(got, []string{odd}) {
		t.Fatalf("explicit file: %v", got)
	}
	if _, err := Collect([]string{filepath.Join(d, "nope")}); err == nil {
		t.Fatal("missing path should error")
	}
}
