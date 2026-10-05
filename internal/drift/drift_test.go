package drift

import (
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

func base() *snapshot.Schema {
	s := &snapshot.Schema{Engine: "postgres", Tables: []snapshot.Table{{
		Schema: "public", Name: "accounts",
		Columns: []snapshot.Column{
			{Name: "id", Type: "bigint", NotNull: true},
			{Name: "email", Type: "text", NotNull: true},
			{Name: "plan", Type: "text", Default: "'free'::text"},
		},
		Indexes:     []snapshot.Index{{Name: "accounts_email_idx", Def: "CREATE INDEX accounts_email_idx ON public.accounts USING btree (email)"}},
		Constraints: []snapshot.Constraint{{Name: "accounts_pkey", Type: "p", Def: "PRIMARY KEY (id)", Validated: true}},
	}}}
	s.Normalize()
	return s
}

func clone(s *snapshot.Schema) *snapshot.Schema {
	c := *s
	c.Tables = nil
	for _, t := range s.Tables {
		t.Columns = append([]snapshot.Column(nil), t.Columns...)
		t.Indexes = append([]snapshot.Index(nil), t.Indexes...)
		t.Constraints = append([]snapshot.Constraint(nil), t.Constraints...)
		c.Tables = append(c.Tables, t)
	}
	return &c
}

func hasKind(ds []Difference, k string) bool {
	for _, d := range ds {
		if d.Kind == k {
			return true
		}
	}
	return false
}

func hasTable(ds []Difference, table string) bool {
	for _, d := range ds {
		if d.Table == table {
			return true
		}
	}
	return false
}

func TestIdenticalSchemasHaveNoDrift(t *testing.T) {
	if d := Compare(base(), clone(base()), Options{}); len(d) != 0 {
		t.Fatalf("unexpected drift: %v", d)
	}
}

// The ticket's scenario: someone runs a manual ALTER TABLE on production.
func TestManualAlterIsDetectedAndNamed(t *testing.T) {
	act := clone(base())
	act.Tables[0].Columns = append(act.Tables[0].Columns, snapshot.Column{Name: "hotfix_flag", Type: "boolean"})
	act.Tables[0].Indexes = nil
	d := Compare(base(), act, Options{})
	got := map[string]Difference{}
	for _, x := range d {
		got[x.Kind] = x
	}
	if x := got[ColumnExtra]; x.Table != "public.accounts" || x.Object != "hotfix_flag" || x.Actual != "boolean" {
		t.Errorf("column-extra: %+v", x)
	}
	if x := got[IndexMissing]; x.Object != "accounts_email_idx" {
		t.Errorf("index-missing: %+v", x)
	}
	if !strings.Contains(got[ColumnExtra].String(), "added outside migrations") {
		t.Errorf("message: %s", got[ColumnExtra])
	}
}

func TestChangeKinds(t *testing.T) {
	act := clone(base())
	c := act.Tables[0].Columns
	for i := range c {
		switch c[i].Name {
		case "email":
			c[i].Type = "character varying(100)"
		case "id":
			c[i].NotNull = false
		case "plan":
			c[i].Default = "'pro'::text"
		}
	}
	act.Tables[0].Constraints[0].Validated = false
	act.Tables = append(act.Tables, snapshot.Table{Schema: "public", Name: "scratch"})
	d := Compare(base(), act, Options{})
	for _, k := range []string{ColumnTypeChanged, ColumnNullability, ColumnDefault, ConstraintChanged, TableExtra} {
		if !hasKind(d, k) {
			t.Errorf("missing %s in %v", k, d)
		}
	}
	if d := Compare(act, base(), Options{}); !hasKind(d, TableMissing) {
		t.Errorf("table-missing not detected: %v", d)
	}
}

func TestIgnoreIntentionalDifferences(t *testing.T) {
	act := clone(base())
	act.Tables[0].Columns = append(act.Tables[0].Columns, snapshot.Column{Name: "debug_flag", Type: "boolean"})
	act.Tables = append(act.Tables,
		snapshot.Table{Schema: "public", Name: "feature_flags"},
		snapshot.Table{Schema: "public", Name: "flyway_schema_history"})

	d := Compare(base(), act, Options{})
	if !hasTable(d, "public.feature_flags") {
		t.Fatalf("setup: feature_flags should drift without an ignore: %v", d)
	}
	if hasTable(d, "public.flyway_schema_history") {
		t.Error("flyway bookkeeping table must be ignored by default")
	}
	d = Compare(base(), act, Options{Ignore: []string{"public.feature_flags", "public.accounts.debug_*"}})
	if len(d) != 0 {
		t.Fatalf("ignores should suppress everything, got %v", d)
	}
}

func TestDeterministicOrder(t *testing.T) {
	act := clone(base())
	act.Tables = append(act.Tables, snapshot.Table{Schema: "public", Name: "z"}, snapshot.Table{Schema: "public", Name: "b"})
	a, b := Compare(base(), act, Options{}), Compare(base(), act, Options{})
	if len(a) != 2 || a[0].Table != "public.b" || a[1].Table != "public.z" || a[0] != b[0] {
		t.Fatalf("order: %v", a)
	}
}
