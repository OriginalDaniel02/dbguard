package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/report"
	"github.com/OriginalDaniel02/dbguard/internal/rules"
)

func javaFile(name string) string { return filepath.Join("..", "..", "testdata", "java", name) }

// lineOf returns the 1-based line of the first line of a fixture containing text.
func lineOf(t *testing.T, file, text string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, text) {
			return i + 1
		}
	}
	t.Fatalf("%q not found in %s", text, file)
	return 0
}

func runJava(t *testing.T, args ...string) (int, []report.File, string) {
	t.Helper()
	t.Setenv("DBGUARD_DSN", "")
	var o, e bytes.Buffer
	code := check(append([]string{"--format", "json"}, args...), &o, &e)
	var files []report.File
	if o.Len() > 0 {
		if err := json.Unmarshal(o.Bytes(), &files); err != nil {
			t.Fatalf("%v\n%s\n%s", err, o.String(), e.String())
		}
	}
	return code, files, e.String()
}

func TestJavaMigrationIsFlaggedAtTheJavaLine(t *testing.T) {
	f := javaFile("V3__AddTransactionIndex.java")
	code, files, _ := runJava(t, "--rows", "transactions=14000000", f)
	if code != 1 || len(files) != 1 || len(files[0].Findings) != 1 {
		t.Fatalf("code=%d files=%+v", code, files)
	}
	x := files[0].Findings[0]
	if x.Rule != "create-index" || x.Table != "public.transactions" || x.Rows != 14_000_000 || x.Estimate == nil {
		t.Errorf("%+v", x)
	}
	if want := lineOf(t, f, "CREATE INDEX"); x.Line != want {
		t.Errorf("reported at line %d, the statement is on line %d", x.Line, want)
	}
	if !strings.Contains(x.Alternative, "canExecuteInTransaction") || strings.Contains(x.Alternative, "executeInTransaction=false") {
		t.Errorf("Java migrations get Java advice, not the SQL-file setting: %q", x.Alternative)
	}
}

func TestMixedJavaSourcesOnlyRealStatementsCount(t *testing.T) {
	f := javaFile("V4__Mixed.java")
	code, files, _ := runJava(t, "--rows", "transactions=14000000", "--rows", "accounts=14000000", f)
	got := files[0].Findings
	// Commented-out DDL and log text are ignored; the table created in the text block is new
	// (so its index is fine); ADD COLUMN note is safe; only the volatile default is a risk.
	if code != 1 || len(got) != 1 || got[0].Rule != "add-column-nonconstant-default" {
		t.Fatalf("code=%d findings=%+v problems=%v", code, got, files[0].Problems)
	}
	if want := lineOf(t, f, "clock_timestamp"); got[0].Line != want {
		t.Errorf("line %d, want %d", got[0].Line, want)
	}
	if len(files[0].Problems) != 0 {
		t.Errorf("a file with only literal SQL has nothing to caveat: %v", files[0].Problems)
	}
}

func TestJavaTextBlockStatementsMapToTheirOwnLines(t *testing.T) {
	// The index is inside a text block, on its own Java line.
	dir := t.TempDir()
	src := "import org.flywaydb.core.api.migration.BaseJavaMigration;\n" + // 1
		"class V9__Block extends BaseJavaMigration {\n" + // 2
		"  void migrate() {\n" + // 3
		"    st.execute(\"\"\"\n" + // 4
		"        ALTER TABLE accounts ADD COLUMN a int;\n" + // 5
		"\n" + // 6
		"        CREATE INDEX idx_t ON transactions (a);\n" + // 7
		"        \"\"\");\n" + // 8
		"  }\n}\n"
	f := filepath.Join(dir, "V9__Block.java")
	os.WriteFile(f, []byte(src), 0o644)
	_, files, _ := runJava(t, "--rows", "transactions=14000000", f)
	if len(files[0].Findings) != 1 || files[0].Findings[0].Line != 7 {
		t.Fatalf("%+v", files[0].Findings)
	}
}

func TestAcknowledgingAJavaFindingWithASlashSlashComment(t *testing.T) {
	code, files, _ := runJava(t, "--rows", "transactions=14000000", javaFile("V5__Acknowledged.java"))
	x := files[0].Findings[0]
	if code != 0 || x.Override != "transactions is write-quiet during the 03:00 deploy window" || report.Blocking(x, rules.MediumHigh) {
		t.Fatalf("code=%d %+v", code, x)
	}
	if len(files[0].Problems) != 0 {
		t.Errorf("the ignore was used: %v", files[0].Problems)
	}
}

func TestJavaMigrationThatReadsSQLFromAFileIsReportedNotSilent(t *testing.T) {
	code, files, _ := runJava(t, javaFile("V6__ReadsSqlFile.java"))
	if code != 0 || len(files[0].Findings) != 0 {
		t.Fatalf("code=%d %+v", code, files[0])
	}
	if len(files[0].Problems) != 1 || !strings.Contains(files[0].Problems[0], "no SQL statements were found") {
		t.Fatalf("a migration with no visible SQL must say so, not look clean: %v", files[0].Problems)
	}
}

func TestDynamicSQLIsAnalyzedConservativelyAndCalledOut(t *testing.T) {
	f := javaFile("V7__DynamicTable.java")
	code, files, _ := runJava(t, f)
	if code != 1 {
		t.Fatalf("code=%d %+v", code, files[0])
	}
	var ruleIDs []string
	for _, x := range files[0].Findings {
		ruleIDs = append(ruleIDs, x.Rule)
		if x.Rows != -1 {
			t.Errorf("an unknown table has unknown size: %+v", x)
		}
	}
	sort.Strings(ruleIDs)
	if strings.Join(ruleIDs, ",") != "add-check-constraint,create-index" && strings.Join(ruleIDs, ",") != "create-index" {
		t.Errorf("rules: %v", ruleIDs)
	}
	if len(files[0].Problems) != 2 || !strings.Contains(files[0].Problems[0], "only known at runtime") {
		t.Errorf("both runtime-dependent statements must be called out: %v", files[0].Problems)
	}

	// Naming the Java expression with --placeholder resolves it to a real table.
	_, files, _ = runJava(t, "--placeholder", "table=transactions", "--placeholder", "arg=transactions", "--rows", "transactions=14000000", f)
	for _, x := range files[0].Findings {
		if x.Table != "public.transactions" || x.Rows != 14_000_000 {
			t.Errorf("--placeholder must resolve dynamic parts: %+v", x)
		}
	}
}

func TestJavaMigrationOnMySQL(t *testing.T) {
	code, files, _ := runJava(t, "--engine", "mysql", "--rows", "orders=14000000", javaFile("V8__MySqlCheck.java"))
	if code != 1 || len(files[0].Findings) != 1 || files[0].Findings[0].Rule != "add-check-constraint" {
		t.Fatalf("code=%d %+v", code, files[0])
	}
	if want := lineOf(t, javaFile("V8__MySqlCheck.java"), "chk_total"); files[0].Findings[0].Line != want {
		t.Errorf("line %d, want %d", files[0].Findings[0].Line, want)
	}
}

func TestDirectoryDiscoveryFindsMigrationsAndSkipsOrdinaryClasses(t *testing.T) {
	files, err := collect([]string{filepath.Join("..", "..", "testdata", "java")})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	sort.Strings(names)
	want := "V3__AddTransactionIndex.java,V4__Mixed.java,V5__Acknowledged.java,V6__ReadsSqlFile.java,V7__DynamicTable.java,V8__MySqlCheck.java"
	if strings.Join(names, ",") != want {
		t.Fatalf("got %v", names)
	}
	// An explicit ordinary Java file is skipped too (CI passes changed files explicitly).
	explicit, _ := collect([]string{javaFile("Helper.java"), javaFile("V3__AddTransactionIndex.java")})
	if len(explicit) != 1 {
		t.Errorf("an ordinary class must not be analyzed: %v", explicit)
	}
	// A migration that does not follow the file name convention is found by its content.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AddLedger.java"), []byte("import org.flywaydb.core.api.migration.BaseJavaMigration;\nclass AddLedger extends BaseJavaMigration {}\n"), 0o644)
	if got, _ := collect([]string{dir}); len(got) != 1 {
		t.Errorf("content sniffing: %v", got)
	}
}

// The fixtures must be valid Java, so the tests describe something real. Compiled with the
// JDK against stubs of the two Flyway classes they use.
func TestFixturesAreValidJava(t *testing.T) {
	javac, err := exec.LookPath("javac")
	if err != nil {
		t.Skip("javac not found")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "org", "flywaydb", "core", "api", "migration")
	os.MkdirAll(stub, 0o755)
	os.WriteFile(filepath.Join(stub, "Context.java"), []byte("package org.flywaydb.core.api.migration;\npublic interface Context { java.sql.Connection getConnection(); }\n"), 0o644)
	os.WriteFile(filepath.Join(stub, "BaseJavaMigration.java"), []byte("package org.flywaydb.core.api.migration;\npublic abstract class BaseJavaMigration { public abstract void migrate(Context c) throws Exception; }\n"), 0o644)
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "java", "*.java"))
	args := []string{"-encoding", "UTF-8", "-d", filepath.Join(dir, "out"), filepath.Join(stub, "Context.java"), filepath.Join(stub, "BaseJavaMigration.java")}
	if out, err := exec.Command(javac, append(args, files...)...).CombinedOutput(); err != nil {
		t.Fatalf("a fixture is not valid Java:\n%s", out)
	}
}
