package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, sql string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	t.Setenv("DBGUARD_DSN", "")
	var o, e bytes.Buffer
	code = check(args, &o, &e)
	return code, o.String(), e.String()
}

func TestExitCodes(t *testing.T) {
	d := t.TempDir()
	risky := write(t, d, "V2__idx.sql", "CREATE INDEX i ON big (a);")
	clean := write(t, d, "V1__ok.sql", "CREATE TABLE t (a int);")
	broken := write(t, d, "V3__bad.sql", "ALTER TABL oops;")

	if code, out, _ := run(t, "--rows", "big=14000000", risky); code != 1 || !strings.Contains(out, "create-index") {
		t.Errorf("risky: code=%d out=%s", code, out)
	}
	if code, _, _ := run(t, clean); code != 0 {
		t.Errorf("clean: code=%d", code)
	}
	if code, _, _ := run(t, broken); code != 2 {
		t.Errorf("unparseable SQL: code=%d, want 2", code)
	}
	if code, _, _ := run(t); code != 2 {
		t.Errorf("no args: code=%d, want 2", code)
	}
	if code, _, _ := run(t, "--fail-on", "nonsense", clean); code != 2 {
		t.Errorf("bad flag value: want 2")
	}
	if code, _, _ := run(t, filepath.Join(d, "missing.sql")); code != 2 {
		t.Errorf("missing file: want 2")
	}
}

func TestSmallTableAndFailOn(t *testing.T) {
	d := t.TempDir()
	f := write(t, d, "V2__fk.sql", "ALTER TABLE big ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id);")
	if code, _, _ := run(t, "--rows", "big=500", f); code != 0 {
		t.Errorf("small table must not block, code=%d", code)
	}
	// medium-high finding is not blocking when the threshold is high.
	if code, _, _ := run(t, "--fail-on", "high", "--rows", "big=14000000", f); code != 0 {
		t.Errorf("fail-on high should pass a medium-high finding, code=%d", code)
	}
}

func TestOverrideUnblocks(t *testing.T) {
	d := t.TempDir()
	f := write(t, d, "V2__idx.sql", "-- dbguard:ignore create-index reason: quiet window\nCREATE INDEX i ON big (a);")
	code, out, _ := run(t, "--rows", "big=14000000", f)
	if code != 0 || !strings.Contains(out, "ACKNOWLEDGED") {
		t.Errorf("code=%d out=%s", code, out)
	}
}

func TestPlaceholdersAndSchemaFlags(t *testing.T) {
	d := t.TempDir()
	f := write(t, d, "V2__idx.sql", "CREATE INDEX i ON ${schema}.orders (a);")
	// Value supplied: the real table size (small) is used, so nothing blocks.
	if code, _, _ := run(t, "--placeholder", "schema=app", "--rows", "app.orders=10", f); code != 0 {
		t.Errorf("code=%d", code)
	}
	// Unqualified name + --schema matches a bare --rows key.
	g := write(t, d, "V3__idx.sql", "CREATE INDEX j ON orders (a);")
	if code, _, _ := run(t, "--schema", "app", "--rows", "orders=10", g); code != 0 {
		t.Errorf("bare rows key with --schema: code=%d", code)
	}
}

func TestFormats(t *testing.T) {
	d := t.TempDir()
	f := write(t, d, "V2__idx.sql", "CREATE INDEX i ON big (a);")
	_, md, _ := run(t, "--format", "markdown", "--rows", "big=14000000", f)
	if !strings.HasPrefix(md, "<!-- dbguard-report -->") {
		t.Errorf("markdown must start with the marker: %q", md)
	}
	_, js, _ := run(t, "--format", "json", "--rows", "big=14000000", f)
	if !strings.Contains(js, `"rule": "create-index"`) {
		t.Errorf("json: %s", js)
	}
	if code, _, _ := run(t, "--format", "xml", f); code != 2 {
		t.Errorf("unknown format: want 2")
	}
}

func TestPickVersion(t *testing.T) {
	for _, c := range []struct{ stamped, info, want string }{
		{"v0.3.0", "", "v0.3.0"}, // release build, stamped by the linker
		{"v0.3.0", "v0.2.1-0.20261006062114-48c3ffe6e6a7", "v0.3.0"}, // the stamp wins
		{"dev", "v0.3.1", "v0.3.1"},                                  // go install ...@v0.3.1
		{"dev", "v0.2.1-0.20261006062114-48c3ffe6e6a7+dirty", "dev"}, // a git checkout: a pseudo-version is not a version
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"},
		{"dev", "v0.3.0-rc.1", "dev"}, // a pre-release is not a clean release tag
	} {
		if got := pickVersion(c.stamped, c.info); got != c.want {
			t.Errorf("pickVersion(%q, %q) = %q, want %q", c.stamped, c.info, got, c.want)
		}
	}
}
