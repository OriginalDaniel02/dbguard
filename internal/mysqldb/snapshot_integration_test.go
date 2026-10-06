package mysqldb

import (
	"context"
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/drift"
	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// keepMy restricts a snapshot to the objects this test created.
func keepMy(s *snapshot.Schema) *snapshot.Schema {
	out := *s
	out.Tables, out.Views = nil, nil
	for _, t := range s.Tables {
		if strings.HasPrefix(t.Name, "dbm_") {
			out.Tables = append(out.Tables, t)
		}
	}
	for _, v := range s.Views {
		if strings.HasPrefix(v.Name, "dbm_") {
			out.Views = append(out.Views, v)
		}
	}
	return &out
}

func TestMySQLDriftAgainstRealServer(t *testing.T) {
	dsn := testDSN(t)
	db := admin(t, dsn)
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cleanup := func() {
		for _, q := range []string{
			"DROP VIEW IF EXISTS dbm_view",
			"DROP TABLE IF EXISTS dbm_child",
			"DROP TABLE IF EXISTS dbm_parent",
		} {
			db.Exec(q)
		}
	}
	cleanup()
	defer cleanup()

	exec("CREATE TABLE dbm_parent (id INT NOT NULL AUTO_INCREMENT, name VARCHAR(40) NOT NULL, PRIMARY KEY (id)) ENGINE=InnoDB")
	exec(`CREATE TABLE dbm_child (
		id BIGINT NOT NULL AUTO_INCREMENT,
		parent_id INT NOT NULL,
		ref VARCHAR(30) NOT NULL,
		note VARCHAR(100) NULL DEFAULT 'n/a',
		qty INT NOT NULL DEFAULT 0,
		created DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		token VARCHAR(36) DEFAULT (UUID()),
		total2 INT AS (qty * 2) STORED,
		body TEXT,
		PRIMARY KEY (id),
		UNIQUE KEY uq_ref (ref),
		KEY idx_parent_created (parent_id, created DESC),
		KEY idx_note_prefix (note(20)),
		FULLTEXT KEY ft_body (body),
		CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES dbm_parent (id) ON DELETE CASCADE,
		CONSTRAINT chk_qty CHECK (qty >= 0)
	) ENGINE=InnoDB`)
	exec("CREATE TRIGGER dbm_trg BEFORE INSERT ON dbm_child FOR EACH ROW SET NEW.note = COALESCE(NEW.note, 'x')")
	exec("CREATE VIEW dbm_view AS SELECT id, ref FROM dbm_child")
	defer db.Exec("DROP TRIGGER IF EXISTS dbm_trg")

	s, err := Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap := func() *snapshot.Schema {
		t.Helper()
		x, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return keepMy(x)
	}

	expected := snap()
	if len(expected.Tables) != 2 || len(expected.Views) != 1 {
		t.Fatalf("extraction incomplete: %d tables, %d views", len(expected.Tables), len(expected.Views))
	}
	var child snapshot.Table
	for _, tb := range expected.Tables {
		if tb.Name == "dbm_child" {
			child = tb
		}
	}
	if len(child.Columns) != 9 || len(child.Indexes) < 5 || len(child.Triggers) != 1 {
		t.Fatalf("child: %d columns, %d indexes, %d triggers", len(child.Columns), len(child.Indexes), len(child.Triggers))
	}
	kinds := map[string]bool{}
	for _, c := range child.Constraints {
		kinds[c.Type] = true
	}
	for _, want := range []string{"p", "u", "f", "c"} {
		if !kinds[want] {
			t.Errorf("constraint type %q missing: %+v", want, child.Constraints)
		}
	}
	for _, c := range child.Columns {
		switch c.Name {
		case "id":
			if c.Identity != "auto_increment" {
				t.Errorf("auto_increment not detected: %+v", c)
			}
		case "token":
			if !strings.HasPrefix(c.Default, "(") {
				t.Errorf("an expression default must be marked: %+v", c)
			}
		case "total2":
			if c.Generated != "s" || c.Default == "" {
				t.Errorf("stored generated column: %+v", c)
			}
		}
	}
	for _, c := range child.Constraints {
		if c.Type == "f" && !strings.Contains(c.Def, "ON DELETE CASCADE") {
			t.Errorf("foreign key action missing: %s", c.Def)
		}
	}

	// No false positives: an unchanged database must report no drift.
	if d := drift.Compare(expected, snap(), drift.Options{}); len(d) != 0 {
		t.Fatalf("unchanged schema reported drift: %v", d)
	}

	// The incident: manual changes made directly on the server.
	exec("ALTER TABLE dbm_child ADD COLUMN hotfix_flag TINYINT(1) NULL")
	exec("ALTER TABLE dbm_child MODIFY qty BIGINT NOT NULL DEFAULT 0")
	exec("ALTER TABLE dbm_child ALTER COLUMN note SET DEFAULT 'changed'")
	exec("ALTER TABLE dbm_child DROP INDEX idx_note_prefix")
	exec("ALTER TABLE dbm_child DROP FOREIGN KEY fk_parent")
	exec("ALTER TABLE dbm_child DROP CHECK chk_qty")
	exec("DROP TRIGGER dbm_trg")
	exec("CREATE OR REPLACE VIEW dbm_view AS SELECT id, ref, qty FROM dbm_child")
	exec("ALTER TABLE dbm_child ADD KEY idx_extra (ref, qty)")

	got := map[string]int{}
	var diffs []drift.Difference
	for _, d := range drift.Compare(expected, snap(), drift.Options{}) {
		got[d.Kind+":"+d.Object]++
		diffs = append(diffs, d)
	}
	want := []string{
		drift.ColumnExtra + ":hotfix_flag",
		drift.ColumnTypeChanged + ":qty",
		drift.ColumnDefault + ":note",
		drift.IndexMissing + ":idx_note_prefix",
		drift.IndexExtra + ":idx_extra",
		drift.ConstraintMissing + ":fk_parent",
		drift.ConstraintMissing + ":chk_qty",
		drift.TriggerMissing + ":dbm_trg",
		drift.ViewChanged + ":",
	}
	for _, k := range want {
		if got[k] == 0 {
			t.Errorf("missing %s; got %v", k, diffs)
		}
	}
	if len(diffs) != len(want) {
		t.Errorf("want exactly %d differences, got %d: %v", len(want), len(diffs), diffs)
	}
}

func TestMySQLSnapshotRequiresDatabaseAndIsReadOnly(t *testing.T) {
	dsn := testDSN(t)
	// A connection string without a database name cannot be snapshotted.
	noDB := dsn[:strings.LastIndex(dsn, "/")]
	s, err := Connect(context.Background(), noDB)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("want a clear error about the missing database, got %v", err)
	}
}

// Staging and production usually live in differently named databases. Identical schemas
// must compare as identical: the database name is not part of a table's identity.
func TestMySQLIdenticalSchemaInDifferentDatabasesHasNoDrift(t *testing.T) {
	dsn := testDSN(t)
	db := admin(t, dsn)
	ddl := []string{
		"CREATE TABLE dbm_account (id INT NOT NULL AUTO_INCREMENT, email VARCHAR(80) NOT NULL, plan VARCHAR(10) NOT NULL DEFAULT 'free', PRIMARY KEY (id), UNIQUE KEY uq_email (email)) ENGINE=InnoDB",
		"CREATE TABLE dbm_order (id INT NOT NULL AUTO_INCREMENT, account_id INT NOT NULL, total DECIMAL(10,2) NOT NULL, KEY idx_acct (account_id), CONSTRAINT fk_order_acct FOREIGN KEY (account_id) REFERENCES dbm_account (id), CONSTRAINT chk_total CHECK (total >= 0), PRIMARY KEY (id)) ENGINE=InnoDB",
	}
	var original string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{original, "dbm_other_db"} {
		if name != original {
			db.Exec("DROP DATABASE IF EXISTS dbm_other_db")
			if _, err := db.Exec("CREATE DATABASE dbm_other_db"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec("USE " + name); err != nil {
			t.Fatal(err)
		}
		db.Exec("DROP TABLE IF EXISTS dbm_order")
		db.Exec("DROP TABLE IF EXISTS dbm_account")
		for _, q := range ddl {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s in %s: %v", q, name, err)
			}
		}
	}
	defer func() {
		db.Exec("DROP DATABASE IF EXISTS dbm_other_db")
		db.Exec("USE " + original)
		db.Exec("DROP TABLE IF EXISTS dbm_order")
		db.Exec("DROP TABLE IF EXISTS dbm_account")
	}()

	snapOf := func(dsn string) *snapshot.Schema {
		t.Helper()
		s, err := Connect(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		x, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return keepMy(x)
	}
	a := snapOf(dsn)
	b := snapOf(dsn[:strings.LastIndex(dsn, "/")] + "/dbm_other_db")
	if len(a.Tables) != 2 || len(b.Tables) != 2 {
		t.Fatalf("tables: %d vs %d", len(a.Tables), len(b.Tables))
	}
	if d := drift.Compare(a, b, drift.Options{}); len(d) != 0 {
		t.Fatalf("the same schema in two databases must not drift: %v", d)
	}
}
