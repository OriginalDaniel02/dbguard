package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/OriginalDaniel02/dbguard/internal/report"
)

func myMigration(t *testing.T, sql string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "V2__change.sql")
	if err := os.WriteFile(p, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runCheck(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("DBGUARD_DSN", "")
	var o, e bytes.Buffer
	code := check(args, &o, &e)
	return code, o.String(), e.String()
}

func TestMySQLOfflineRisky(t *testing.T) {
	f := myMigration(t, "ALTER TABLE orders ADD CONSTRAINT chk CHECK (total >= 0);\nALTER TABLE orders ADD COLUMN note VARCHAR(10);\n")
	code, out, errOut := runCheck(t, "--engine", "mysql", "--rows", "orders=14000000", f)
	if code != 1 || !strings.Contains(out, "add-check-constraint") || strings.Contains(out, "note") && strings.Contains(out, "(add-column") {
		t.Fatalf("code=%d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "block writes to orders (14.0M rows)") {
		t.Errorf("must say it blocks writes, with the size:\n%s", out)
	}
}

func TestMySQLOfflineSafeAndVersionFlag(t *testing.T) {
	safe := myMigration(t, "ALTER TABLE orders ADD COLUMN note VARCHAR(10) AFTER id;\nALTER TABLE orders ADD INDEX i1 (customer_id);\n")
	// 8.0.36 default: INSTANT in any position; the index is online (low): not blocking.
	if code, out, _ := runCheck(t, "--engine", "mysql", "--rows", "orders=14000000", safe); code != 0 {
		t.Fatalf("online changes must pass: code=%d\n%s", code, out)
	}
	// MySQL 8.0.28: AFTER is not instant (INPLACE rebuild, medium) - still not blocking at medium-high...
	code, out, _ := runCheck(t, "--engine", "mysql", "--mysql-version", "8.0.28", "--rows", "orders=14000000", "--fail-on", "medium", safe)
	if code != 1 || !strings.Contains(out, "table-rebuild") {
		t.Fatalf("8.0.28 positioned ADD COLUMN rebuilds the table: code=%d\n%s", code, out)
	}
	if code, _, e := runCheck(t, "--engine", "mysql", "--mysql-version", "nonsense", safe); code != 2 || !strings.Contains(e, "mysql-version") {
		t.Errorf("bad version must be rejected: %d %s", code, e)
	}
	if code, _, _ := runCheck(t, "--engine", "oracle", safe); code != 2 {
		t.Error("unknown engine must exit 2")
	}
}

func TestMySQLLenientProblemsDoNotFailTheRun(t *testing.T) {
	f := myMigration(t, "THIS IS NOT MYSQL;\nCREATE FULLTEXT INDEX ft ON orders (body);\n")
	code, out, _ := runCheck(t, "--engine", "mysql", "--rows", "orders=14000000", "--format", "json", f)
	var files []report.File
	if err := json.Unmarshal([]byte(out), &files); err != nil {
		t.Fatal(err, out)
	}
	if code != 1 || len(files[0].Findings) != 1 || len(files[0].Problems) != 1 || !strings.Contains(files[0].Problems[0], "NOT analyzed") {
		t.Fatalf("code=%d findings=%d problems=%v", code, len(files[0].Findings), files[0].Problems)
	}
}

func TestMySQLOverrideAndPlaceholders(t *testing.T) {
	f := myMigration(t, "-- dbguard:ignore add-check-constraint reason: table is write-quiet at 3am\nALTER TABLE ${db}.orders ADD CONSTRAINT chk CHECK (a > 0);\n")
	code, out, _ := runCheck(t, "--engine", "mysql", "--placeholder", "db=shop", "--rows", "shop.orders=14000000", f)
	if code != 0 || !strings.Contains(out, "ACKNOWLEDGED") || !strings.Contains(out, "shop.orders") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

func TestEngineAndDSNMustAgree(t *testing.T) {
	f := myMigration(t, "ALTER TABLE t ADD COLUMN a INT;\n")
	t.Setenv("DBGUARD_DSN", "postgres://u:p@localhost/db")
	var o, e bytes.Buffer
	if code := check([]string{"--engine", "mysql", f}, &o, &e); code != 2 || !strings.Contains(e.String(), "not a mysql connection string") {
		t.Fatalf("code=%d %s", code, e.String())
	}
	e.Reset()
	t.Setenv("DBGUARD_DSN", "mysql://u:p@localhost/db")
	if code := check([]string{"--engine", "postgres", f}, &o, &e); code != 2 || strings.Contains(e.String(), "p@") {
		t.Fatalf("code=%d %s", code, e.String())
	}
}

// End to end against a real MySQL: DB Guard reads the live column definition, so a
// nullability-only MODIFY is told apart from a type change.
func TestMySQLEndToEndAgainstRealServer(t *testing.T) {
	dsn := os.Getenv("DBGUARD_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("DBGUARD_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", mustDriverDSN(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		"SET SESSION cte_max_recursion_depth = 1000000",
		"DROP TABLE IF EXISTS dbg_e2e",
		"CREATE TABLE dbg_e2e (id INT PRIMARY KEY, total INT NOT NULL, ref VARCHAR(30) NOT NULL) ENGINE=InnoDB",
		"INSERT INTO dbg_e2e WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 150000) SELECT n, n, CONCAT('r', n) FROM s",
		"ANALYZE TABLE dbg_e2e",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	defer db.Exec("DROP TABLE IF EXISTS dbg_e2e")

	t.Setenv("DBGUARD_DSN", dsn)
	cases := map[string]string{
		"ALTER TABLE dbg_e2e MODIFY total BIGINT NOT NULL;":     "alter-column-type", // really a type change: COPY
		"ALTER TABLE dbg_e2e MODIFY total INT NULL;":            "table-rebuild",     // nullability only: online
		"ALTER TABLE dbg_e2e MODIFY ref VARCHAR(40) NOT NULL;":  "",                  // widening within the bucket: in place
		"ALTER TABLE dbg_e2e MODIFY ref VARCHAR(100) NOT NULL;": "alter-column-type", // crosses the length-byte bucket
		"ALTER TABLE dbg_e2e ADD CONSTRAINT c CHECK (id > 0);":  "add-check-constraint",
	}
	for sqlText, wantRule := range cases {
		var o, e bytes.Buffer
		code := check([]string{"--format", "json", "--fail-on", "medium", myMigration(t, sqlText)}, &o, &e)
		var files []report.File
		if err := json.Unmarshal(o.Bytes(), &files); err != nil {
			t.Fatalf("%s: %v\n%s\n%s", sqlText, err, o.String(), e.String())
		}
		var got []string
		for _, f := range files[0].Findings {
			got = append(got, f.Rule)
			if f.Rows < 100000 || f.Rows > 200000 {
				t.Errorf("%s: row estimate should be ~150000 from the live server, got %d", sqlText, f.Rows)
			}
		}
		// ~150k rows is above the default 100k "large table" threshold.
		if (wantRule == "" && len(got) != 0) || (wantRule != "" && (len(got) != 1 || got[0] != wantRule)) {
			t.Errorf("%s: got %v (code %d), want %q", sqlText, got, code, wantRule)
		}
	}
}

func mustDriverDSN(dsn string) string {
	// mysql://user:pass@host:port/db -> user:pass@tcp(host:port)/db
	rest := strings.TrimPrefix(dsn, "mysql://")
	at := strings.LastIndex(rest, "@")
	slash := strings.Index(rest[at:], "/") + at
	return rest[:at] + "@tcp(" + rest[at+1:slash] + ")" + rest[slash:]
}
