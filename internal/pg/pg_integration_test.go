package pg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Run with: DBGUARD_TEST_DSN=postgres://postgres:secret@localhost:55432/postgres go test ./internal/pg
// The DSN should be a superuser/owner: the test proves dbguard still can't write with it.
func testDSN(t *testing.T) string {
	dsn := os.Getenv("DBGUARD_TEST_DSN")
	if dsn == "" {
		t.Skip("DBGUARD_TEST_DSN not set")
	}
	return dsn
}

func TestRowsAndVersion(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()

	setup, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close(ctx)
	for _, q := range []string{
		"DROP TABLE IF EXISTS dbg_rows",
		"CREATE TABLE dbg_rows AS SELECT g AS id FROM generate_series(1, 250000) g",
		"ANALYZE dbg_rows",
	} {
		if _, err := setup.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer setup.Exec(ctx, "DROP TABLE dbg_rows")

	s, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if s.Version < 12 {
		t.Errorf("version = %d", s.Version)
	}
	rows, ok := s.Rows("public", "dbg_rows")
	if !ok || rows < 200_000 || rows > 300_000 {
		t.Errorf("rows = %d ok=%v, want ~250000", rows, ok)
	}
	if _, ok := s.Rows("public", "does_not_exist"); ok {
		t.Error("unknown table should report ok=false")
	}
}

// Acceptance: no credential used by dbguard can write, even if the role could.
func TestSessionIsReadOnly(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	s, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	var dummy int
	err = s.query(ctx, "CREATE TABLE dbg_should_fail (a int)", nil, &dummy)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write must be rejected as read-only, got: %v", err)
	}
	// Even outside our transaction wrapper, the session default is read-only.
	var ro string
	if err := s.conn.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&ro); err != nil || ro != "on" {
		t.Fatalf("default_transaction_read_only = %q, err=%v", ro, err)
	}
}

func TestErrorsNeverContainCredentials(t *testing.T) {
	_, err := Connect(context.Background(), "postgres://user:hunter2secret@127.0.0.1:1/db?connect_timeout=2")
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), "hunter2secret") {
		t.Fatalf("password leaked in error: %v", err)
	}
}

// A table with data but no statistics must be "unknown", never "0 rows".
func TestUnanalyzedTableIsUnknownNotZero(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	setup, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close(ctx)
	for _, q := range []string{
		"DROP TABLE IF EXISTS dbg_fresh, dbg_empty",
		"CREATE TABLE dbg_fresh (id int) WITH (autovacuum_enabled = false)",
		"INSERT INTO dbg_fresh SELECT g FROM generate_series(1, 200000) g",
		"CREATE TABLE dbg_empty (id int)",
	} {
		if _, err := setup.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer setup.Exec(ctx, "DROP TABLE dbg_fresh, dbg_empty")
	// Simulate lost statistics (crash recovery, restore, pg_upgrade): let the
	// inserts flush, then reset all counters from a separate session.
	time.Sleep(2 * time.Second)
	other, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	if _, err := other.Exec(ctx, "SELECT pg_stat_reset()"); err != nil {
		t.Fatal(err)
	}
	// Precondition: the scenario must really be "data on disk, no statistics",
	// otherwise this test would pass without testing anything.
	var live, reltuples, size int64
	if err := other.QueryRow(ctx, `SELECT COALESCE(st.n_live_tup,0), c.reltuples::bigint, pg_relation_size(c.oid)
		FROM pg_class c LEFT JOIN pg_stat_user_tables st ON st.relid = c.oid WHERE c.relname = 'dbg_fresh'`).
		Scan(&live, &reltuples, &size); err != nil {
		t.Fatal(err)
	}
	if live != 0 || reltuples > 0 || size == 0 {
		t.Fatalf("precondition not met (n_live_tup=%d reltuples=%d bytes=%d); test would be vacuous", live, reltuples, size)
	}

	s, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if n, ok := s.Rows("public", "dbg_fresh"); ok {
		t.Errorf("data on disk with no stats must be unknown, got %d rows (0 would downgrade it to low risk)", n)
	}
	if n, ok := s.Rows("public", "dbg_empty"); !ok || n != 0 {
		t.Errorf("a genuinely empty table is 0 rows, got %d ok=%v", n, ok)
	}
}

func TestHasNotNullCheckAndSchema(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	setup, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close(ctx)
	for _, q := range []string{
		"DROP TABLE IF EXISTS dbg_chk",
		"CREATE TABLE dbg_chk (a int, \"Mixed\" int, b int)",
		"ALTER TABLE dbg_chk ADD CONSTRAINT a_nn CHECK (a IS NOT NULL)",
		"ALTER TABLE dbg_chk ADD CONSTRAINT m_nn CHECK (\"Mixed\" IS NOT NULL)",
		"ALTER TABLE dbg_chk ADD CONSTRAINT b_nn CHECK (b IS NOT NULL) NOT VALID",
	} {
		if _, err := setup.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer setup.Exec(ctx, "DROP TABLE dbg_chk")

	s, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	if !s.HasNotNullCheck("public", "dbg_chk", "a") {
		t.Error("validated check on a should be found")
	}
	if !s.HasNotNullCheck("public", "dbg_chk", "Mixed") {
		t.Error("quoted mixed-case column should be found")
	}
	if s.HasNotNullCheck("public", "dbg_chk", "b") {
		t.Error("NOT VALID check must not count: it does not skip the scan")
	}
	if s.HasNotNullCheck("public", "dbg_chk", "nope") {
		t.Error("unknown column")
	}
	if s.Schema != "public" {
		t.Errorf("schema = %q", s.Schema)
	}
}
