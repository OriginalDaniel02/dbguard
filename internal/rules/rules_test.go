package rules

import (
	"strings"
	"testing"
)

type fakeStats map[string]int64

func (f fakeStats) Rows(schema, table string) (int64, bool) {
	n, ok := f[schema+"."+table]
	return n, ok
}

var big = fakeStats{"public.transactions": 14_000_000, "public.tiny": 500}

func run(t *testing.T, sql string) []Finding {
	t.Helper()
	fs, err := Check(sql, Options{PGVersion: 16, Stats: big})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestRules(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		rule     string // "" => expect no findings
		risk     Risk
		estimate bool
	}{
		{"add column no default", "ALTER TABLE transactions ADD COLUMN note text;", "", 0, false},
		{"add column constant default", "ALTER TABLE transactions ADD COLUMN n int DEFAULT 0;", "", 0, false},
		{"add column negative default", "ALTER TABLE transactions ADD COLUMN n int DEFAULT -1;", "", 0, false},
		{"add column now() default is metadata-only", "ALTER TABLE transactions ADD COLUMN at timestamptz DEFAULT now();", "", 0, false},
		{"add column current_timestamp default", "ALTER TABLE transactions ADD COLUMN at timestamptz DEFAULT CURRENT_TIMESTAMP;", "", 0, false},
		{"add column clock_timestamp() default", "ALTER TABLE transactions ADD COLUMN at timestamptz DEFAULT clock_timestamp();", AddColumnVolatile, High, true},
		{"add column random() default", "ALTER TABLE transactions ADD COLUMN r float DEFAULT random();", AddColumnVolatile, High, true},
		{"add serial column", "ALTER TABLE transactions ADD COLUMN seq serial;", AddColumnVolatile, High, true},
		{"add column uuid default", "ALTER TABLE transactions ADD COLUMN u uuid DEFAULT gen_random_uuid();", AddColumnVolatile, High, true},
		{"alter type", "ALTER TABLE transactions ALTER COLUMN amount TYPE bigint;", AlterColumnType, High, true},
		{"create index", "CREATE INDEX idx ON transactions (amount);", CreateIndex, High, true},
		{"create index concurrently", "CREATE INDEX CONCURRENTLY idx ON transactions (amount);", "", 0, false},
		{"add unique", "ALTER TABLE transactions ADD CONSTRAINT u UNIQUE (ref);", AddUniqueOrPK, High, true},
		{"add pk", "ALTER TABLE transactions ADD PRIMARY KEY (id);", AddUniqueOrPK, High, true},
		{"add unique using index", "ALTER TABLE transactions ADD CONSTRAINT u UNIQUE USING INDEX idx;", "", 0, false},
		{"set not null", "ALTER TABLE transactions ALTER COLUMN ref SET NOT NULL;", AddNotNull, MediumHigh, true},
		{"add fk", "ALTER TABLE transactions ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES accounts(id);", AddForeignKey, MediumHigh, true},
		{"add fk not valid", "ALTER TABLE transactions ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES accounts(id) NOT VALID;", "", 0, false},
		{"drop column", "ALTER TABLE transactions DROP COLUMN old;", DropColumn, Low, false},
		{"small table downgraded", "CREATE INDEX idx ON tiny (a);", CreateIndex, Low, false},
		{"new table in same migration", "CREATE TABLE fresh (a int); CREATE INDEX idx ON fresh (a);", "", 0, false},
		{"table built by CREATE TABLE AS", "CREATE TABLE kinds AS SELECT DISTINCT kind FROM transactions; ALTER TABLE kinds ADD PRIMARY KEY (kind);", "", 0, false},
		{"table built by SELECT INTO", "SELECT DISTINCT kind INTO kinds2 FROM transactions; CREATE INDEX i ON kinds2 (kind);", "", 0, false},
		{"materialized view indexed in the same migration", "CREATE MATERIALIZED VIEW mv AS SELECT 1 AS a; CREATE UNIQUE INDEX mv_a ON mv (a);", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := run(t, tc.sql)
			if tc.rule == "" {
				if len(fs) != 0 {
					t.Fatalf("want no findings, got %+v", fs)
				}
				return
			}
			if len(fs) != 1 {
				t.Fatalf("want 1 finding, got %d: %+v", len(fs), fs)
			}
			f := fs[0]
			if f.Rule != tc.rule || f.Risk != tc.risk {
				t.Errorf("got %s/%s, want %s/%s", f.Rule, f.Risk, tc.rule, tc.risk)
			}
			if (f.Estimate != nil) != tc.estimate {
				t.Errorf("estimate present=%v, want %v", f.Estimate != nil, tc.estimate)
			}
			if f.Alternative == "" {
				t.Error("missing safer alternative")
			}
		})
	}
}

func TestNotNullViaValidatedCheckIsSafe(t *testing.T) {
	sql := `ALTER TABLE transactions ADD CONSTRAINT c CHECK (ref IS NOT NULL);
ALTER TABLE transactions ALTER COLUMN ref SET NOT NULL;`
	if fs := run(t, sql); len(fs) != 0 {
		t.Fatalf("want no findings, got %+v", fs)
	}
}

func TestLineNumbersAndUnknownSize(t *testing.T) {
	sql := "SELECT 1;\n\nCREATE INDEX idx ON mystery (a);\n"
	fs, err := Check(sql, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Line != 3 || fs[0].Rows != -1 || fs[0].Risk != High {
		t.Fatalf("unexpected: %+v", fs)
	}
}

func TestEstimateMessage(t *testing.T) {
	f := run(t, "CREATE INDEX idx ON transactions (a);")[0]
	if f.Estimate == nil || f.Message == "" {
		t.Fatalf("missing estimate/message: %+v", f)
	}
	t.Log(f.Message)
}

func TestFlywayPlaceholders(t *testing.T) {
	sql := "CREATE INDEX idx ON ${schema}.transactions (a);\n"
	fs, err := Check(sql, Options{})
	if err != nil {
		t.Fatalf("placeholders must not break parsing: %v", err)
	}
	if len(fs) != 1 || fs[0].Table != "${schema}.transactions" || fs[0].Rule != CreateIndex {
		t.Fatalf("unexpected: %+v", fs)
	}
	if !strings.Contains(fs[0].Statement, "${schema}") || strings.Contains(fs[0].Statement, "dbguard_ph") {
		t.Errorf("statement not restored: %q", fs[0].Statement)
	}
	// With a value, the real table's size is used.
	fs, err = Check(sql, Options{Placeholders: map[string]string{"schema": "public"}, Stats: big})
	if err != nil || len(fs) != 1 || fs[0].Table != "public.transactions" || fs[0].Rows != 14_000_000 {
		t.Fatalf("unexpected with value: %+v %v", fs, err)
	}
}

type checkStats struct {
	fakeStats
	has bool
}

func (c checkStats) HasNotNullCheck(schema, table, col string) bool { return c.has }

func TestNotNullSafeWhenDBHasValidatedCheck(t *testing.T) {
	sql := "ALTER TABLE transactions ALTER COLUMN ref SET NOT NULL;"
	fs, _ := Check(sql, Options{PGVersion: 16, Stats: checkStats{big, true}})
	if len(fs) != 0 {
		t.Fatalf("want safe, got %+v", fs)
	}
	fs, _ = Check(sql, Options{PGVersion: 11, Stats: checkStats{big, true}})
	if len(fs) != 1 {
		t.Fatalf("PG 11 has no check-based skip; want finding, got %+v", fs)
	}
	fs, _ = Check(sql, Options{PGVersion: 16, Stats: checkStats{big, false}})
	if len(fs) != 1 {
		t.Fatalf("no check in DB; want finding, got %+v", fs)
	}
}

func TestDefaultSchema(t *testing.T) {
	st := fakeStats{"app.orders": 5_000_000}
	fs, _ := Check("CREATE INDEX i ON orders (a);", Options{Stats: st, DefaultSchema: "app"})
	if len(fs) != 1 || fs[0].Table != "app.orders" || fs[0].Rows != 5_000_000 {
		t.Fatalf("unexpected: %+v", fs)
	}
}
