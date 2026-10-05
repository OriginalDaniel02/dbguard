package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/OriginalDaniel02/dbguard/internal/drift"
	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// only keeps just the table under test, so other tests' tables don't add noise.
func only(s *snapshot.Schema, name string) *snapshot.Schema {
	out := *s
	out.Tables = nil
	for _, t := range s.Tables {
		if t.Name == name {
			out.Tables = append(out.Tables, t)
		}
	}
	return &out
}

// Acceptance criterion: a manually-introduced schema change is flagged.
func TestDriftDetectsManualAlterAgainstRealPostgres(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	exec := func(q string) {
		t.Helper()
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("DROP TABLE IF EXISTS dbg_drift")
	exec(`CREATE TABLE dbg_drift (
		id bigint PRIMARY KEY,
		email text NOT NULL,
		plan text DEFAULT 'free',
		qty int)`)
	exec("CREATE INDEX dbg_drift_email_idx ON dbg_drift (email)")
	exec("ALTER TABLE dbg_drift ADD CONSTRAINT dbg_drift_qty_pos CHECK (qty > 0)")
	defer admin.Exec(ctx, "DROP TABLE IF EXISTS dbg_drift")

	st, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)

	snap := func() *snapshot.Schema {
		t.Helper()
		s, err := st.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return only(s, "dbg_drift")
	}

	expected := snap()
	tbl := expected.Tables[0]
	if len(tbl.Columns) != 4 || len(tbl.Indexes) != 2 || len(tbl.Constraints) != 2 { // pkey + email idx; pkey + check
		t.Fatalf("snapshot incomplete: %d cols, %d idx, %d cons", len(tbl.Columns), len(tbl.Indexes), len(tbl.Constraints))
	}

	// Stability: an unchanged database must never report drift (no false positives).
	if d := drift.Compare(expected, snap(), drift.Options{}); len(d) != 0 {
		t.Fatalf("unchanged schema reported drift: %v", d)
	}

	// The incident from the ticket: someone fixes prod by hand and forgets a migration.
	exec("ALTER TABLE dbg_drift ADD COLUMN hotfix_flag boolean")
	exec("ALTER TABLE dbg_drift ALTER COLUMN qty TYPE bigint")
	exec("ALTER TABLE dbg_drift ALTER COLUMN plan SET DEFAULT 'pro'")
	exec("ALTER TABLE dbg_drift ALTER COLUMN email DROP NOT NULL")
	exec("DROP INDEX dbg_drift_email_idx")
	exec("ALTER TABLE dbg_drift DROP CONSTRAINT dbg_drift_qty_pos")

	diffs := drift.Compare(expected, snap(), drift.Options{})
	got := map[string]drift.Difference{}
	for _, d := range diffs {
		got[d.Kind+":"+d.Object] = d
	}
	want := []string{
		drift.ColumnExtra + ":hotfix_flag",
		drift.ColumnTypeChanged + ":qty",
		drift.ColumnDefault + ":plan",
		drift.ColumnNullability + ":email",
		drift.IndexMissing + ":dbg_drift_email_idx",
		drift.ConstraintMissing + ":dbg_drift_qty_pos",
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s; got %v", k, diffs)
		}
	}
	if d := got[drift.ColumnTypeChanged+":qty"]; d.Expected != "integer" || d.Actual != "bigint" {
		t.Errorf("type change detail: %+v", d)
	}
	if !strings.Contains(got[drift.ColumnExtra+":hotfix_flag"].String(), "dbg_drift.hotfix_flag") {
		t.Errorf("message must name the table and column: %s", got[drift.ColumnExtra+":hotfix_flag"])
	}
	if len(diffs) != len(want) {
		t.Errorf("want exactly %d differences, got %d: %v", len(want), len(diffs), diffs)
	}
}

// The snapshot connection is read-only and reads catalogs only.
func TestSnapshotExcludesSystemSchemasAndBookkeeping(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	st, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)
	s, err := st.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range s.Tables {
		if tb.Schema == "pg_catalog" || tb.Schema == "information_schema" || strings.HasPrefix(tb.Schema, "pg_toast") {
			t.Fatalf("system table leaked into snapshot: %s", tb.QName())
		}
	}
}
