package javamig

import (
	"strings"
	"testing"
)

func values(t *testing.T, src string) []string {
	t.Helper()
	toks, problems := lex(src)
	if len(problems) != 0 {
		t.Fatalf("lex problems: %v", problems)
	}
	var out []string
	for i := 0; i < len(toks); {
		if toks[i].kind != tString {
			i++
			continue
		}
		seg, next := group(src, toks, i)
		out = append(out, seg.SQL)
		i = next
	}
	return out
}

func TestLexEscapesAndLiterals(t *testing.T) {
	got := values(t, `String a = "tab\there\nnew \"q\" \\ A \101 \s|";`)
	want := "tab\there\nnew \"q\" \\ A A  |"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCommentsAndCharLiteralsAreNotCode(t *testing.T) {
	src := `
// ctx.execute("ALTER TABLE a ADD COLUMN x INT");
/* ctx.execute("CREATE INDEX i ON t (a)");
   still a comment "quoted" */
char q = '"';
char e = '\'';
String real = "ALTER TABLE b ADD COLUMN y INT";
`
	ex := Extract(src)
	if len(ex.Segments) != 1 || !strings.Contains(ex.Segments[0].SQL, "TABLE b") {
		t.Fatalf("only the real statement counts: %+v", ex.Segments)
	}
	if ex.Segments[0].Line != 7 {
		t.Errorf("line = %d, want 7", ex.Segments[0].Line)
	}
}

func TestConcatenationAndLineMapping(t *testing.T) {
	src := `class M {
  void migrate() {
    st.execute("ALTER TABLE orders " +
               "ADD COLUMN note text, " +
               "ADD COLUMN flag boolean");
  }
}`
	ex := Extract(src)
	if len(ex.Segments) != 1 {
		t.Fatalf("%+v", ex.Segments)
	}
	s := ex.Segments[0]
	if s.SQL != "ALTER TABLE orders ADD COLUMN note text, ADD COLUMN flag boolean" || s.Line != 3 {
		t.Fatalf("%q line %d", s.SQL, s.Line)
	}
	if len(ex.Problems) != 0 {
		t.Errorf("a fully literal statement has nothing to warn about: %v", ex.Problems)
	}
}

func TestTextBlockLinesMapBackToTheJavaFile(t *testing.T) {
	src := "class M {\n" + // 1
		"  void migrate() {\n" + // 2
		"    st.execute(\"\"\"\n" + // 3: opening delimiter
		"        CREATE TABLE fresh (id int);\n" + // 4
		"\n" + // 5
		"        CREATE INDEX idx ON big (a);\n" + // 6
		"        \"\"\");\n" + // 7
		"  }\n}"
	ex := Extract(src)
	if len(ex.Segments) != 1 {
		t.Fatalf("%+v", ex.Segments)
	}
	c := ex.Combine(nil)
	// The CREATE INDEX is the third SQL line (0-based 2) and sits on Java line 6.
	lines := strings.Split(c.SQL, "\n")
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, "CREATE INDEX") {
			idx = i + 1
		}
	}
	if got := c.JavaLine(idx); got != 6 {
		t.Fatalf("CREATE INDEX maps to Java line %d, want 6\n%s", got, c.SQL)
	}
	if got := c.JavaLine(1); got != 4 {
		t.Errorf("first statement maps to line %d, want 4", got)
	}
}

func TestStringFormatAndDynamicParts(t *testing.T) {
	src := `
st.execute(String.format("ALTER TABLE %s ADD COLUMN %s INT", table, col));
st.execute("ALTER TABLE " + schema + "." + table + " ADD COLUMN x INT");
st.execute("ALTER TABLE " + getTable(1, "a") + " ADD COLUMN y INT");
`
	ex := Extract(src)
	if len(ex.Segments) != 3 {
		t.Fatalf("%+v", ex.Segments)
	}
	if ex.Segments[0].SQL != "ALTER TABLE ${arg} ADD COLUMN ${arg} INT" {
		t.Errorf("format template: %q", ex.Segments[0].SQL)
	}
	if ex.Segments[1].SQL != "ALTER TABLE ${schema}.${table} ADD COLUMN x INT" {
		t.Errorf("concatenation with names: %q", ex.Segments[1].SQL)
	}
	if ex.Segments[2].SQL != `ALTER TABLE ${getTable(1,"a")} ADD COLUMN y INT` {
		t.Errorf("method call operand: %q", ex.Segments[2].SQL)
	}
	if len(ex.Problems) != 3 {
		t.Errorf("every runtime-dependent statement must be called out: %v", ex.Problems)
	}
}

func TestNotSQLIsIgnored(t *testing.T) {
	src := `
log.info("Alter table failed, retrying");
log.info("Creating index on orders");
log.warn("create table users");
String ok = "SELECT * FROM t";
String upd = "UPDATE t SET a = 1";
`
	if ex := Extract(src); len(ex.Segments) != 0 {
		t.Fatalf("log messages and DML are not DDL to analyze: %+v", ex.Segments)
	}
}

func TestCombineIsolatesUnparseableSegments(t *testing.T) {
	src := `
a("ALTER TABLE t ADD COLUMN x INT");
b("ALTER TABLE t ADD COLUMN y BOGUS BOGUS BOGUS");
c("CREATE INDEX i ON t (x)");
`
	ex := Extract(src)
	c := ex.Combine(func(sql string) error {
		if strings.Contains(sql, "BOGUS") {
			return errTest("syntax error")
		}
		return nil
	})
	if strings.Contains(c.SQL, "BOGUS") || !strings.Contains(c.SQL, "CREATE INDEX") {
		t.Fatalf("the bad statement is dropped, the others kept:\n%s", c.SQL)
	}
	var found bool
	for _, p := range c.Problems {
		found = found || (strings.Contains(p, "line 3") && strings.Contains(p, "NOT analyzed"))
	}
	if !found {
		t.Errorf("the dropped statement must be reported with its Java line: %v", c.Problems)
	}
	if got := c.JavaLine(2); got != 4 {
		t.Errorf("CREATE INDEX maps to line %d, want 4", got)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestProblemsForMalformedSource(t *testing.T) {
	_, problems := lex("String a = \"never closed;\nint b = 1;\n/* never closed")
	if len(problems) != 2 {
		t.Fatalf("want an unterminated string and an unterminated comment: %v", problems)
	}
	if !strings.Contains(problems[0], "line 1") {
		t.Errorf("%v", problems)
	}
}

func TestSniff(t *testing.T) {
	if !Sniff("import org.flywaydb.core.api.migration.BaseJavaMigration;") || !Sniff("class V1 extends BaseJavaMigration {") {
		t.Error("Flyway migrations are recognized")
	}
	if Sniff("public class Util { }") {
		t.Error("an ordinary class is not a migration")
	}
}

func TestSQLDetection(t *testing.T) {
	yes := []string{
		"ALTER TABLE t ADD COLUMN a int", "alter table t drop column a", "ALTER TABLE ONLY public.t ALTER COLUMN a SET NOT NULL",
		"CREATE INDEX i ON t (a)", "CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)", "CREATE TABLE t (id int)", "CREATE TABLE t AS SELECT 1",
		"DROP TABLE t", "OPTIMIZE TABLE t", "SET foreign_key_checks = 0", "-- add it\nALTER TABLE t ADD COLUMN a int",
		"/* x */ CREATE INDEX i ON t (a)", "CREATE MATERIALIZED VIEW v AS SELECT 1",
	}
	no := []string{
		"Alter table failed", "ALTER TABLE failed", "Create index later", "create table users", "Creating index", "SELECT 1",
		"INSERT INTO t VALUES (1)", "set the flag", "drop it", "", "ALTERNATIVE",
	}
	for _, s := range yes {
		if !isSQL(s) {
			t.Errorf("should be SQL: %q", s)
		}
	}
	for _, s := range no {
		if isSQL(s) {
			t.Errorf("should not be SQL: %q", s)
		}
	}
}
