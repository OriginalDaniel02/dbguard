package mysqldb

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]struct {
		v      int
		flavor string
	}{
		"8.0.36":                  {80036, "MySQL"},
		"8.0.36-0ubuntu0.22.04.1": {80036, "MySQL"},
		"8.4.0":                   {80400, "MySQL"},
		"5.7.44-log":              {50744, "MySQL"},
		"10.11.6-MariaDB-1:10.11": {101106, "MariaDB"},
		"garbage":                 {0, "MySQL"},
	} {
		if v, f := ParseVersion(in); v != want.v || f != want.flavor {
			t.Errorf("%q: got %d %s, want %d %s", in, v, f, want.v, want.flavor)
		}
	}
}

func TestIsMySQLDSN(t *testing.T) {
	for dsn, want := range map[string]bool{
		"mysql://u:p@host:3306/db":              true,
		"u:p@tcp(host:3306)/db":                 true,
		"postgres://u:p@host/db":                false,
		"postgresql://u:p@host/db?sslmode=none": false,
	} {
		if IsMySQLDSN(dsn) != want {
			t.Errorf("%q: want %v", dsn, want)
		}
	}
}

func TestURLToDriverDSN(t *testing.T) {
	got, err := toDriverDSN("mysql://reader:s3cr%40t@db.internal:3307/shop?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reader:s3cr@t@tcp(db.internal:3307)/shop", "parseTime=true"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing %q", got, want)
		}
	}
	if got, _ := toDriverDSN("mysql://u:p@h/db"); !strings.Contains(got, "tcp(h:3306)") {
		t.Errorf("default port: %q", got)
	}
}

// Integration tests: DBGUARD_TEST_MYSQL_DSN=mysql://root:secret@localhost:53306/shop
func testDSN(t *testing.T) string {
	dsn := os.Getenv("DBGUARD_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("DBGUARD_TEST_MYSQL_DSN not set")
	}
	return dsn
}

func admin(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	d, err := toDriverDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", d)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // one session, so SET SESSION below applies to later statements
	if _, err := db.Exec("SET SESSION cte_max_recursion_depth = 1000000"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConnectVersionSchemaAndRows(t *testing.T) {
	dsn := testDSN(t)
	db := admin(t, dsn)
	for _, q := range []string{
		"DROP TABLE IF EXISTS dbg_rows",
		"CREATE TABLE dbg_rows (id INT PRIMARY KEY, ref VARCHAR(30) NOT NULL, note VARCHAR(10) NULL) ENGINE=InnoDB",
		"INSERT INTO dbg_rows WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 50000) SELECT n, CONCAT('r', n), NULL FROM s",
		"ANALYZE TABLE dbg_rows",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	defer db.Exec("DROP TABLE IF EXISTS dbg_rows")

	s, err := Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Version < 50700 || s.Flavor != "MySQL" || s.Schema == "" {
		t.Errorf("version=%d flavor=%s schema=%q", s.Version, s.Flavor, s.Schema)
	}
	n, ok := s.Rows("", "dbg_rows")
	if !ok || n < 40000 || n > 60000 {
		t.Errorf("rows = %d ok=%v, want ~50000", n, ok)
	}
	if _, ok := s.Rows("", "no_such_table"); ok {
		t.Error("unknown table must report ok=false")
	}

	c, ok := s.Column("", "dbg_rows", "ref")
	if !ok || c.Type != "varchar(30)" || !c.NotNull || c.Charset == "" {
		t.Errorf("column info: %+v ok=%v", c, ok)
	}
	if c, _ := s.Column("", "dbg_rows", "note"); c.NotNull {
		t.Errorf("note is nullable: %+v", c)
	}
	if _, ok := s.Column("", "dbg_rows", "nope"); ok {
		t.Error("unknown column")
	}
}

// Acceptance: no credential used by DB Guard can write.
func TestSessionIsReadOnly(t *testing.T) {
	dsn := testDSN(t)
	s, err := Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{"CREATE TABLE dbg_should_fail (a INT)", "INSERT INTO dbg_nope VALUES (1)"} {
		var dummy int
		err := s.queryRow(context.Background(), q, nil, &dummy)
		if err == nil {
			t.Fatalf("write must be rejected: %s", q)
		}
	}
	var ro int
	if err := s.queryRow(context.Background(), "SELECT @@transaction_read_only", nil, &ro); err != nil {
		t.Fatal(err)
	}
	// Inside our READ ONLY transaction the session reports read-only.
	if ro != 1 {
		t.Errorf("@@transaction_read_only = %d, want 1", ro)
	}
	// And nothing was created.
	db := admin(t, dsn)
	var n int
	db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_NAME = 'dbg_should_fail'").Scan(&n)
	if n != 0 {
		t.Error("a write got through")
	}
}

// information_schema caches statistics (information_schema_stats_expiry, default 24h), so a
// table bulk-loaded after the cache was filled reports 0 rows. DB Guard reads with the cache
// disabled so a big table is never mistaken for an empty one.
func TestFreshStatsAreReadDespiteInformationSchemaCache(t *testing.T) {
	dsn := testDSN(t)
	db := admin(t, dsn)
	for _, q := range []string{
		"DROP TABLE IF EXISTS dbg_fresh",
		"CREATE TABLE dbg_fresh (id INT PRIMARY KEY, pad VARCHAR(200)) ENGINE=InnoDB",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	defer db.Exec("DROP TABLE IF EXISTS dbg_fresh")

	// Fill the cache while the table is empty, with the default 24h expiry.
	if _, err := db.Exec("SET SESSION information_schema_stats_expiry = 86400"); err != nil {
		t.Skip("server has no information_schema_stats_expiry (MySQL 5.7)")
	}
	var cached int64
	db.QueryRow("SELECT TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dbg_fresh'").Scan(&cached)
	if _, err := db.Exec("INSERT INTO dbg_fresh WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 60000) SELECT n, REPEAT('x', 100) FROM s"); err != nil {
		t.Fatal(err)
	}
	// Precondition: the stale cache really does hide the data, otherwise this test proves nothing.
	var stale int64
	db.QueryRow("SELECT TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dbg_fresh'").Scan(&stale)
	if stale > 1000 {
		t.Fatalf("precondition failed: the 24h cache should still say ~0 rows, got %d", stale)
	}

	s, err := Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, ok := s.Rows("", "dbg_fresh")
	if !ok || n < 30000 {
		t.Fatalf("DB Guard must see the real size despite the stale cache: rows=%d ok=%v (cache said %d)", n, ok, stale)
	}
}

func TestErrorsNeverContainCredentials(t *testing.T) {
	_, err := Connect(context.Background(), "mysql://user:hunter2secret@127.0.0.1:1/db?timeout=2s")
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), "hunter2secret") {
		t.Fatalf("password leaked in error: %v", err)
	}
}
