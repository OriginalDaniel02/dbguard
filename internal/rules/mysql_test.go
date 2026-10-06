package rules

import (
	"strings"
	"testing"
)

// myStats is a fake MySQL: table sizes plus existing column definitions.
type myStats struct {
	rows map[string]int64
	cols map[string]ColumnInfo // "table.column"
}

func (m myStats) Rows(schema, table string) (int64, bool) {
	n, ok := m.rows[table]
	return n, ok
}

func (m myStats) Column(schema, table, column string) (ColumnInfo, bool) {
	c, ok := m.cols[table+"."+column]
	return c, ok
}

var bigMy = myStats{
	rows: map[string]int64{"orders": 14_000_000, "tiny": 100},
	cols: map[string]ColumnInfo{
		"orders.total":   {Type: "int", NotNull: true},
		"orders.ref":     {Type: "varchar(30)", NotNull: true, Charset: "utf8mb4"},
		"orders.created": {Type: "datetime", NotNull: false},
		"orders.note":    {Type: "varchar(300)", NotNull: true, Charset: "utf8mb4"},
	},
}

func runMy(t *testing.T, sql string, opts Options) []Finding {
	t.Helper()
	if opts.Stats == nil {
		opts.Stats = bigMy
	}
	fs, err := CheckMySQL(sql, opts)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return fs
}

// Every expectation below was observed on MySQL 8.0.46 by asking the server which
// ALGORITHM/LOCK it accepts (see docs/mysql.md).
func TestMySQLRules(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string // "rule:risk", in order; nil => no findings
	}{
		// ADD COLUMN is INSTANT in every position on 8.0.29+.
		{"add column", "ALTER TABLE orders ADD COLUMN x INT DEFAULT 0;", nil},
		{"add column AFTER", "ALTER TABLE orders ADD COLUMN x INT AFTER id;", nil},
		{"add column FIRST", "ALTER TABLE orders ADD COLUMN x INT FIRST;", nil},
		{"add NOT NULL with default", "ALTER TABLE orders ADD COLUMN x INT NOT NULL DEFAULT 5;", nil},
		{"add CURRENT_TIMESTAMP default", "ALTER TABLE orders ADD COLUMN x DATETIME DEFAULT CURRENT_TIMESTAMP;", nil},
		{"add VIRTUAL generated", "ALTER TABLE orders ADD COLUMN g INT AS (total*2) VIRTUAL;", nil},
		// Any parenthesized default is an expression default: COPY, even when it looks harmless.
		{"add expression default (arithmetic)", "ALTER TABLE orders ADD COLUMN x INT DEFAULT (1+1);", []string{"add-column-nonconstant-default:high"}},
		{"add expression default (parenthesized literal)", "ALTER TABLE orders ADD COLUMN x INT DEFAULT (5);", []string{"add-column-nonconstant-default:high"}},
		{"add expression default (NOW)", "ALTER TABLE orders ADD COLUMN x DATETIME DEFAULT (NOW());", []string{"add-column-nonconstant-default:high"}},
		{"add expression default (function)", "ALTER TABLE orders ADD COLUMN x VARCHAR(40) DEFAULT (UUID());", []string{"add-column-nonconstant-default:high"}},
		{"add JSON default", "ALTER TABLE orders ADD COLUMN x JSON DEFAULT (JSON_ARRAY());", []string{"add-column-nonconstant-default:high"}},
		{"add negative default", "ALTER TABLE orders ADD COLUMN x INT DEFAULT -1;", nil},
		{"add string default", "ALTER TABLE orders ADD COLUMN x VARCHAR(5) DEFAULT 'a';", nil},
		{"string containing DEFAULT (", "ALTER TABLE orders ADD COLUMN x VARCHAR(50) DEFAULT 'DEFAULT (x)';", nil},
		{"add STORED generated", "ALTER TABLE orders ADD COLUMN g INT AS (total*2) STORED;", []string{"table-copy:high"}},
		{"add AUTO_INCREMENT", "ALTER TABLE orders ADD COLUMN ai INT NOT NULL AUTO_INCREMENT, ADD UNIQUE KEY ai_u (ai);", []string{"blocks-writes:high", "create-index:low"}},

		{"drop column", "ALTER TABLE orders DROP COLUMN note;", []string{"drop-column:low"}},

		// MODIFY with the existing column known (from the database).
		{"int to bigint", "ALTER TABLE orders MODIFY total BIGINT NOT NULL;", []string{"alter-column-type:high"}},
		{"nullability only", "ALTER TABLE orders MODIFY total INT NULL;", []string{"table-rebuild:medium"}},
		{"NULL to NOT NULL", "ALTER TABLE orders MODIFY created DATETIME NOT NULL;", []string{"table-rebuild:medium"}},
		{"same definition (rename via CHANGE)", "ALTER TABLE orders CHANGE total total3 INT NOT NULL;", nil},
		{"same type, moved", "ALTER TABLE orders MODIFY total INT NOT NULL AFTER id;", []string{"table-rebuild:medium"}},
		{"varchar widen within bucket", "ALTER TABLE orders MODIFY ref VARCHAR(40) NOT NULL;", nil},
		{"varchar widen across bucket", "ALTER TABLE orders MODIFY ref VARCHAR(100) NOT NULL;", []string{"alter-column-type:high"}},
		{"varchar shrink", "ALTER TABLE orders MODIFY ref VARCHAR(10) NOT NULL;", []string{"alter-column-type:high"}},
		{"varchar already past bucket, widen", "ALTER TABLE orders MODIFY note VARCHAR(400) NOT NULL;", nil},
		{"rename column", "ALTER TABLE orders RENAME COLUMN total TO total2;", nil},
		{"set default", "ALTER TABLE orders ALTER COLUMN total SET DEFAULT 7;", nil},

		// Indexes: online, except FULLTEXT.
		{"add index", "ALTER TABLE orders ADD INDEX i1 (customer_id);", []string{"create-index:low"}},
		{"create index", "CREATE INDEX i1 ON orders (customer_id);", []string{"create-index:low"}},
		{"add unique", "ALTER TABLE orders ADD UNIQUE KEY u1 (ref);", []string{"create-index:low"}},
		{"create unique", "CREATE UNIQUE INDEX u1 ON orders (ref);", []string{"create-index:low"}},
		{"add fulltext", "ALTER TABLE orders ADD FULLTEXT KEY ft (body);", []string{"create-index:high"}},
		{"create fulltext", "CREATE FULLTEXT INDEX ft ON orders (body);", []string{"create-index:high"}},
		{"drop index", "DROP INDEX i1 ON orders;", nil},

		// Keys and constraints.
		{"add primary key", "ALTER TABLE orders ADD PRIMARY KEY (id);", []string{"add-unique-or-pk:medium"}},
		{"drop primary key alone", "ALTER TABLE orders DROP PRIMARY KEY;", []string{"table-copy:high"}},
		{"drop + add primary key", "ALTER TABLE orders DROP PRIMARY KEY, ADD PRIMARY KEY (id, customer_id);", []string{"add-unique-or-pk:medium"}},
		{"add foreign key (checks on)", "ALTER TABLE orders ADD CONSTRAINT fk FOREIGN KEY (customer_id) REFERENCES customers (id);", []string{"add-foreign-key:high"}},
		{"add foreign key (checks off)", "SET foreign_key_checks = 0;\nALTER TABLE orders ADD CONSTRAINT fk FOREIGN KEY (customer_id) REFERENCES customers (id);", []string{"add-foreign-key:medium"}},
		{"checks off then back on", "SET foreign_key_checks=0; SET foreign_key_checks=1;\nALTER TABLE orders ADD CONSTRAINT fk FOREIGN KEY (customer_id) REFERENCES customers (id);", []string{"add-foreign-key:high"}},
		{"add check", "ALTER TABLE orders ADD CONSTRAINT chk CHECK (total >= 0);", []string{"add-check-constraint:high"}},

		// Table options.
		{"convert charset", "ALTER TABLE orders CONVERT TO CHARACTER SET latin1;", []string{"table-copy:high"}},
		{"default charset is metadata", "ALTER TABLE orders DEFAULT CHARSET=utf8mb4;", nil},
		{"engine rebuild", "ALTER TABLE orders ENGINE=InnoDB;", []string{"table-rebuild:medium"}},
		{"row format", "ALTER TABLE orders ROW_FORMAT=COMPACT;", []string{"table-rebuild:medium"}},
		{"comment", "ALTER TABLE orders COMMENT='x';", nil},
		{"auto_increment value", "ALTER TABLE orders AUTO_INCREMENT=1000;", nil},
		{"force", "ALTER TABLE orders FORCE;", []string{"table-rebuild:medium"}},
		{"optimize", "OPTIMIZE TABLE orders;", []string{"table-rebuild:medium"}},

		// Explicit ALGORITHM / LOCK: MySQL refuses instead of blocking.
		{"explicit COPY", "ALTER TABLE orders ADD COLUMN x INT, ALGORITHM=COPY;", []string{"table-copy:high"}},
		{"LOCK=NONE guards a copy-type change", "ALTER TABLE orders MODIFY total BIGINT NOT NULL, ALGORITHM=INPLACE, LOCK=NONE;", nil},
		{"LOCK=NONE alone", "ALTER TABLE orders MODIFY total BIGINT NOT NULL, LOCK=NONE;", nil},
		{"ALGORITHM=INSTANT", "ALTER TABLE orders ADD COLUMN x INT, ALGORITHM=INSTANT;", nil},
		{"INPLACE alone does not guarantee", "ALTER TABLE orders ADD FULLTEXT KEY ft (body), ALGORITHM=INPLACE;", []string{"create-index:high"}},
		{"explicit LOCK=SHARED", "ALTER TABLE orders ADD INDEX i (a), LOCK=SHARED;", []string{"blocks-writes:high"}},
		{"explicit LOCK=EXCLUSIVE", "ALTER TABLE orders ADD INDEX i (a), LOCK=EXCLUSIVE;", []string{"blocks-writes:high"}},
		{"CREATE INDEX with LOCK=NONE", "CREATE FULLTEXT INDEX ft ON orders (body) ALGORITHM=INPLACE LOCK=NONE;", nil},

		// Not about big existing tables.
		{"new table in same migration", "CREATE TABLE fresh (id INT PRIMARY KEY, a INT);\nALTER TABLE fresh ADD CONSTRAINT chk CHECK (a > 0);", nil},
		{"small table downgraded", "ALTER TABLE tiny ADD CONSTRAINT chk CHECK (a > 0);", []string{"add-check-constraint:low"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := runMy(t, tc.sql, Options{})
			var got []string
			for _, f := range fs {
				got = append(got, f.Rule+":"+f.Risk.String())
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v\n%+v", got, tc.want, fs)
			}
			for _, f := range fs {
				if f.Alternative == "" {
					t.Errorf("%s: missing safer alternative", f.Rule)
				}
			}
		})
	}
}

func TestMySQLUnknownColumnIsConservative(t *testing.T) {
	// Offline (no column info): MODIFY is assumed to change the type.
	fs := runMy(t, "ALTER TABLE orders MODIFY COLUMN status VARCHAR(20) NOT NULL;", Options{Stats: myStats{rows: map[string]int64{"orders": 14_000_000}}})
	if len(fs) != 1 || fs[0].Rule != AlterColumnType || fs[0].Risk != High || !strings.Contains(fs[0].Lock, "assumed") {
		t.Fatalf("%+v", fs)
	}
}

func TestMySQLVersionGates(t *testing.T) {
	// MySQL < 8.0.29: ADD COLUMN is only INSTANT for the last position; DROP COLUMN rebuilds.
	v28 := Options{MySQLVersion: 80028}
	if fs := runMy(t, "ALTER TABLE orders ADD COLUMN x INT;", v28); len(fs) != 0 {
		t.Errorf("8.0.28 plain ADD COLUMN is still INSTANT: %+v", fs)
	}
	if fs := runMy(t, "ALTER TABLE orders ADD COLUMN x INT AFTER id;", v28); len(fs) != 1 || fs[0].Rule != TableRebuild {
		t.Errorf("8.0.28 positioned ADD COLUMN rebuilds: %+v", fs)
	}
	if fs := runMy(t, "ALTER TABLE orders DROP COLUMN note;", v28); len(fs) != 2 || fs[0].Rule != TableRebuild || fs[1].Rule != DropColumn {
		t.Errorf("8.0.28 DROP COLUMN rebuilds: %+v", fs)
	}
	if fs := runMy(t, "ALTER TABLE orders ADD COLUMN x INT;", Options{MySQLVersion: 50700}); len(fs) != 1 || fs[0].Rule != TableRebuild {
		t.Errorf("5.7 ADD COLUMN rebuilds: %+v", fs)
	}
	if fs := runMy(t, "ALTER TABLE orders ADD COLUMN x INT AFTER id;", Options{MySQLVersion: 80029}); len(fs) != 0 {
		t.Errorf("8.0.29+ is INSTANT in any position: %+v", fs)
	}
}

func TestMySQLEstimatesAndMessages(t *testing.T) {
	fs := runMy(t, "ALTER TABLE orders ADD CONSTRAINT chk CHECK (total >= 0);", Options{})
	f := fs[0]
	if f.Estimate == nil || !strings.Contains(f.Message, "block writes to orders (14.0M rows)") {
		t.Fatalf("a blocking operation must say it blocks writes, with a duration: %+v", f)
	}
	fs = runMy(t, "ALTER TABLE orders ADD PRIMARY KEY (id);", Options{})
	if !strings.Contains(fs[0].Message, "run online") || !strings.Contains(fs[0].Message, "writes continue") {
		t.Fatalf("an online operation must not claim to block: %q", fs[0].Message)
	}
}

func TestMySQLLinesPlaceholdersAndComments(t *testing.T) {
	sql := "-- header comment\n\n-- another\nCREATE INDEX i ON ${schema}.orders (a);\n\nALTER TABLE orders ADD CONSTRAINT chk CHECK (a > 0);\n"
	fs, err := CheckMySQL(sql, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].Line != 4 || fs[1].Line != 6 {
		t.Fatalf("lines: %+v", fs)
	}
	if fs[0].Table != "${schema}.orders" || strings.Contains(fs[0].Statement, "dbguard_ph") {
		t.Errorf("placeholders must be restored: %q / %q", fs[0].Table, fs[0].Statement)
	}
	if _, err := CheckMySQL("ALTER TABL oops;", Options{}); err == nil {
		t.Error("syntax errors must be reported")
	}
}

func TestMySQLDefaultSchemaAndQualifiedNames(t *testing.T) {
	st := myStats{rows: map[string]int64{"orders": 5_000_000}}
	fs, _ := CheckMySQL("ALTER TABLE shop.orders ADD CONSTRAINT c CHECK (a > 0);", Options{Stats: st})
	if len(fs) != 1 || fs[0].Table != "shop.orders" {
		t.Fatalf("%+v", fs)
	}
	fs, _ = CheckMySQL("ALTER TABLE orders ADD CONSTRAINT c CHECK (a > 0);", Options{Stats: st, DefaultSchema: "shop"})
	if len(fs) != 1 || fs[0].Table != "shop.orders" {
		t.Fatalf("%+v", fs)
	}
}

func TestMySQLNormType(t *testing.T) {
	for in, want := range map[string]string{
		"int(11)": "int", "BIGINT(20) unsigned": "bigint unsigned", "varchar(30)": "varchar(30)",
		"decimal(10,2)": "decimal(10,2)", "  Int  ": "int", "tinyint(1)": "tinyint",
	} {
		if got := normType(in); got != want {
			t.Errorf("normType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMySQLLenientReportsUnparseableStatementsInsteadOfFailing(t *testing.T) {
	sql := "ALTER TABLE orders ADD CONSTRAINT chk CHECK (a > 0);\nTHIS IS NOT MYSQL AT ALL;\nCREATE FULLTEXT INDEX ft ON orders (body);\n"
	if _, err := CheckMySQL(sql, Options{Stats: bigMy}); err == nil {
		t.Error("strict mode must reject a file with a syntax error")
	}
	fs, problems := CheckMySQLLenient(sql, Options{Stats: bigMy})
	if len(fs) != 2 || fs[0].Rule != AddCheck || fs[1].Rule != CreateIndex {
		t.Fatalf("the statements around the bad one must still be analyzed: %+v", fs)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "line 2") || !strings.Contains(problems[0], "NOT analyzed") {
		t.Fatalf("problems: %v", problems)
	}
	if fs[1].Line != 3 {
		t.Errorf("line numbers must survive the fallback: %d", fs[1].Line)
	}
}

func TestMarkExpressionDefaults(t *testing.T) {
	cases := map[string]int{
		"ALTER TABLE t ADD c INT DEFAULT (1+1)":                          1,
		"ALTER TABLE t ADD c INT DEFAULT   (a+1), ADD d INT DEFAULT (2)": 2,
		"ALTER TABLE t ADD c INT DEFAULT 5":                              0,
		"ALTER TABLE t ADD c INT DEFAULT -1":                             0,
		"ALTER TABLE t ADD c VARCHAR(9) DEFAULT 'DEFAULT (x)'":           0,
		"-- DEFAULT (x)\nALTER TABLE t ADD c INT":                        0,
		"ALTER TABLE t ADD c INT /* DEFAULT (x) */":                      0,
		"ALTER TABLE t ADD c INT DEFAULT ((1+2)*3)":                      1,
		"ALTER TABLE t ADD defaulted INT":                                0,
		"ALTER TABLE t ADD c INT DEFAULT ('(')":                          1,
	}
	for sql, want := range cases {
		out, restore := markExpressionDefaults(sql)
		got := strings.Count(out, "dbguard_expr_")
		if got != want {
			t.Errorf("%q: %d markers, want %d (%q)", sql, got, want, out)
		}
		if restore(out) != sql {
			t.Errorf("restore must round-trip: %q -> %q", sql, restore(out))
		}
	}
}
