package pg

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/OriginalDaniel02/dbguard/internal/drift"
	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// keep restricts a snapshot to the objects this test created.
func keep(s *snapshot.Schema) *snapshot.Schema {
	out := *s
	out.Tables, out.Views, out.Sequences, out.Enums = nil, nil, nil, nil
	for _, t := range s.Tables {
		if strings.HasPrefix(t.Name, "dbgo_") {
			out.Tables = append(out.Tables, t)
		}
	}
	for _, v := range s.Views {
		if strings.HasPrefix(v.Name, "dbgo_") {
			out.Views = append(out.Views, v)
		}
	}
	for _, q := range s.Sequences {
		if strings.HasPrefix(q.Name, "dbgo_") {
			out.Sequences = append(out.Sequences, q)
		}
	}
	for _, e := range s.Enums {
		if strings.HasPrefix(e.Name, "dbgo_") {
			out.Enums = append(out.Enums, e)
		}
	}
	return &out
}

func TestDriftOnViewsSequencesEnumsTriggersAgainstRealPostgres(t *testing.T) {
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
	cleanup := func() {
		for _, q := range []string{
			"DROP MATERIALIZED VIEW IF EXISTS dbgo_mv",
			"DROP VIEW IF EXISTS dbgo_view",
			"DROP TABLE IF EXISTS dbgo_orders",
			"DROP SEQUENCE IF EXISTS dbgo_seq",
			"DROP TYPE IF EXISTS dbgo_status",
			"DROP FUNCTION IF EXISTS dbgo_audit()",
		} {
			admin.Exec(ctx, q)
		}
	}
	cleanup()
	defer cleanup()

	exec("CREATE TYPE dbgo_status AS ENUM ('new', 'paid')")
	exec("CREATE SEQUENCE dbgo_seq START 1")
	exec("CREATE TABLE dbgo_orders (id bigint PRIMARY KEY, status dbgo_status NOT NULL, total int)")
	exec("CREATE VIEW dbgo_view AS SELECT id, status FROM dbgo_orders")
	exec("CREATE MATERIALIZED VIEW dbgo_mv AS SELECT id FROM dbgo_orders")
	exec("CREATE FUNCTION dbgo_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$")
	exec("CREATE TRIGGER dbgo_trg AFTER UPDATE ON dbgo_orders FOR EACH ROW EXECUTE FUNCTION dbgo_audit()")

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
		return keep(s)
	}

	expected := snap()
	if len(expected.Views) != 2 || len(expected.Enums) != 1 || len(expected.Sequences) != 1 || len(expected.Tables[0].Triggers) != 1 {
		t.Fatalf("extraction incomplete: %d views, %d enums, %d sequences, %d triggers",
			len(expected.Views), len(expected.Enums), len(expected.Sequences), len(expected.Tables[0].Triggers))
	}
	if !expected.Views[0].Materialized && !expected.Views[1].Materialized {
		t.Error("materialized view not flagged")
	}
	if got := expected.Enums[0].Labels; len(got) != 2 || got[0] != "new" || got[1] != "paid" {
		t.Errorf("enum labels/order: %v", got)
	}

	// No false positives: every pretty-printed definition must be stable.
	if d := drift.Compare(expected, snap(), drift.Options{}); len(d) != 0 {
		t.Fatalf("unchanged schema reported drift: %v", d)
	}

	// Manual changes made outside migrations.
	exec("ALTER TYPE dbgo_status ADD VALUE 'refunded'")
	exec("ALTER SEQUENCE dbgo_seq INCREMENT BY 5")
	exec("CREATE OR REPLACE VIEW dbgo_view AS SELECT id, status, total FROM dbgo_orders")
	exec("DROP MATERIALIZED VIEW dbgo_mv")
	exec("DROP TRIGGER dbgo_trg ON dbgo_orders")

	got := map[string]drift.Difference{}
	for _, d := range drift.Compare(expected, snap(), drift.Options{}) {
		got[d.Kind] = d
	}
	for _, k := range []string{drift.EnumChanged, drift.SequenceChanged, drift.ViewChanged, drift.ViewMissing, drift.TriggerMissing} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s; got %v", k, got)
		}
	}
	if e := got[drift.EnumChanged]; e.Actual != "new, paid, refunded" {
		t.Errorf("enum detail: %+v", e)
	}
	if len(got) != 5 {
		t.Errorf("want exactly 5 kinds of difference, got %d: %v", len(got), got)
	}
}
