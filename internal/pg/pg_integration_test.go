package pg

import (
	"context"
	"os"
	"strings"
	"testing"

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
