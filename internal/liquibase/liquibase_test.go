package liquibase

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, name string) (*Changelog, []byte) {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "liquibase", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(p, b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return c, b
}

func TestAllFormatsTranslateIdentically(t *testing.T) {
	var ref string
	for _, f := range []string{"changelog.xml", "changelog.yaml", "changelog.json"} {
		c, _ := load(t, f)
		got := c.Combine(nil).SQL
		if ref == "" {
			ref = got
			continue
		}
		if got != ref {
			t.Errorf("%s translates differently from the XML form:\n--- xml\n%s\n--- %s\n%s", f, ref, f, got)
		}
	}
}

func TestTranslation(t *testing.T) {
	c, _ := load(t, "changelog.xml")
	sql := c.Combine(nil).SQL
	for _, want := range []string{
		`CREATE TABLE "widgets" ("id" bigint, "name" text);`,
		`CREATE INDEX "idx_widgets_name" ON "widgets" ("name");`,
		`ALTER TABLE "transactions" ADD COLUMN "note" text;`,
		"ALTER TABLE \"transactions\" ADD COLUMN \"created_at\" timestamptz DEFAULT ${now};",
		`ALTER TABLE "transactions" ADD COLUMN "score" float8 DEFAULT random();`,
		`CREATE INDEX "idx_tx_amount" ON "transactions" ("amount");`,
		`ALTER TABLE "transactions" ALTER COLUMN "ref" SET NOT NULL;`,
		`ALTER TABLE "transactions" ALTER COLUMN "amount" TYPE bigint;`,
		`ALTER TABLE "transactions" ADD CONSTRAINT "uq_tx_ref" UNIQUE ("ref");`,
		`ALTER TABLE "transactions" ADD CONSTRAINT "fk_tx_acct" FOREIGN KEY ("account_id") REFERENCES "accounts" ("id") NOT VALID;`,
		`ALTER TABLE "transactions" DROP COLUMN "legacy";`,
		`CREATE INDEX CONCURRENTLY idx_tx_ref ON transactions (ref);`,
		// addLookupTable, as real Liquibase generates it:
		`CREATE TABLE "kinds" AS SELECT DISTINCT "kind" AS "kind" FROM "transactions" WHERE "kind" IS NOT NULL;`,
		`ALTER TABLE "kinds" ADD PRIMARY KEY ("kind");`,
		`ALTER TABLE "transactions" ADD CONSTRAINT "FK_TRANSACTIONS_KINDS" FOREIGN KEY ("kind") REFERENCES "kinds" ("kind");`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing statement %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "oracle_only") {
		t.Error("a changeSet restricted to another dbms must be skipped")
	}
	if c.Properties["now"] != "now()" {
		t.Errorf("the postgresql property must win over the oracle one: %q", c.Properties["now"])
	}
	if len(c.Changesets) != 10 {
		t.Errorf("want 10 changesets (the oracle-only one excluded), got %d", len(c.Changesets))
	}
}

func TestUnsupportedChangeTypesAreReportedNotSilentlySkipped(t *testing.T) {
	c, _ := load(t, "changelog.xml")
	var found bool
	for _, p := range c.Combine(nil).Problems {
		if strings.Contains(p, "mergeColumns") && strings.Contains(p, "not analyzed") {
			found = true
		}
	}
	if !found {
		t.Fatal("an unanalyzed change type must be surfaced as a problem")
	}
}

func TestLineNumbersPointAtChangeSets(t *testing.T) {
	c, raw := load(t, "changelog.xml")
	lines := strings.Split(string(raw), "\n")
	for _, cs := range c.Changesets {
		if !strings.Contains(lines[cs.Line-1], `<changeSet id="`+cs.ID+`"`) {
			t.Errorf("changeSet %s: line %d is %q", cs.ID, cs.Line, lines[cs.Line-1])
		}
	}
	y, rawY := load(t, "changelog.yaml")
	ylines := strings.Split(string(rawY), "\n")
	for _, cs := range y.Changesets {
		if !strings.Contains(ylines[cs.Line-1], "- changeSet:") || !strings.Contains(ylines[cs.Line], `id: "`+cs.ID+`"`) {
			t.Errorf("yaml changeSet %s: line %d is %q", cs.ID, cs.Line, ylines[cs.Line-1])
		}
	}
}

func TestSpanMapsBackToChangesets(t *testing.T) {
	c, _ := load(t, "changelog.xml")
	comb := c.Combine(nil)
	lines := strings.Split(comb.SQL, "\n")
	for i, l := range lines {
		if strings.Contains(l, "idx_tx_amount") {
			idx := comb.SpanAt(i + 1)
			if idx < 0 || c.Changesets[idx].ID != "4" {
				t.Fatalf("line %d should map to changeSet 4, got %d", i+1, idx)
			}
		}
	}
}

func TestInvalidStatementsAreIsolatedNotFatal(t *testing.T) {
	c := &Changelog{Changesets: []Changeset{
		{ID: "a", Line: 3, SQL: []string{"CREATE INDEX i ON t (x)"}},
		{ID: "b", Line: 9, SQL: []string{"THIS IS NOT SQL"}},
	}}
	comb := c.Combine(func(s string) error {
		if strings.Contains(s, "NOT SQL") {
			return os.ErrInvalid
		}
		return nil
	})
	if !strings.Contains(comb.SQL, "CREATE INDEX") || strings.Contains(comb.SQL, "NOT SQL") {
		t.Fatalf("bad statement should be dropped: %q", comb.SQL)
	}
	if len(comb.Problems) != 1 || !strings.Contains(comb.Problems[0], "line 9") {
		t.Fatalf("problems: %v", comb.Problems)
	}
}

func TestSqlFileIsReadRelativeToChangelog(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "idx.sql"), []byte("CREATE INDEX i ON big (a);"), 0o644)
	xml := `<databaseChangeLog><changeSet id="1" author="a"><sqlFile path="idx.sql" relativeToChangelogFile="true"/></changeSet>
<changeSet id="2" author="a"><sqlFile path="missing.sql"/></changeSet></databaseChangeLog>`
	c, err := Parse(filepath.Join(d, "c.xml"), []byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	comb := c.Combine(nil)
	if !strings.Contains(comb.SQL, "CREATE INDEX i ON big") {
		t.Errorf("sqlFile not read: %q", comb.SQL)
	}
	if len(comb.Problems) != 1 || !strings.Contains(comb.Problems[0], "missing.sql") {
		t.Errorf("missing sqlFile must be a problem: %v", comb.Problems)
	}
}

func TestDbmsAttribute(t *testing.T) {
	for attr, want := range map[string]bool{
		"": true, "all": true, "postgresql": true, "PostgreSQL": true, "oracle,postgresql": true,
		"oracle": false, "mysql,oracle": false, "!oracle": true, "!postgresql": false, "!oracle,!mysql": true,
	} {
		if got := dbmsMatches(attr, EnginePostgres); got != want {
			t.Errorf("dbms=%q: got %v, want %v", attr, got, want)
		}
	}
}

func TestSniffAndRejectNonChangelogs(t *testing.T) {
	if !Sniff("a.xml", []byte("<databaseChangeLog>")) || Sniff("pom.xml", []byte("<project>")) {
		t.Error("xml sniff")
	}
	if !Sniff("a.sql", []byte("--liquibase formatted sql\n")) || Sniff("V1__x.sql", []byte("select 1;")) {
		t.Error("sql sniff")
	}
	if _, err := Parse("a.xml", []byte("<project/>")); err == nil {
		t.Error("an XML file that is not a changelog must be rejected")
	}
	if _, err := Parse("a.yaml", []byte("foo: bar")); err == nil {
		t.Error("a YAML file without databaseChangeLog must be rejected")
	}
	if _, err := Parse("a.txt", nil); err == nil {
		t.Error("unknown extension")
	}
}

func loadFor(t *testing.T, name, engine string) *Changelog {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "liquibase", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseFor(p, b, engine)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return c
}

// Expected forms are what Liquibase 4.29 itself generates for MySQL (offline update-sql).
func TestMySQLTranslationMatchesRealLiquibase(t *testing.T) {
	c := loadFor(t, "mysql-changelog.xml", EngineMySQL)
	sql := c.Combine(nil).SQL
	for _, want := range []string{
		"CREATE TABLE `widgets` (`id` bigint, `name` varchar(40));",
		"CREATE INDEX `idx_widgets_name` ON `widgets` (`name`);",
		// multi-column addColumn is ONE statement (MySQL chooses one algorithm for it)
		"ALTER TABLE `orders` ADD COLUMN `note` varchar(10), ADD COLUMN `qty` int DEFAULT 0, ADD COLUMN `created_at` datetime DEFAULT ${now};",
		"ALTER TABLE `orders` ADD COLUMN `token` varchar(36) DEFAULT (UUID());",
		"ALTER TABLE `orders` MODIFY `total` bigint;",
		"ALTER TABLE `orders` MODIFY `ref` varchar(30) NOT NULL;",
		"ALTER TABLE `orders` ADD CONSTRAINT `fk_orders_customer` FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`);",
		"ALTER TABLE `orders` ADD CONSTRAINT `uq_orders_ref` UNIQUE (`ref`);",
		"ALTER TABLE `widgets` ADD PRIMARY KEY (`id`);", // MySQL ignores the primary key's name
		"ALTER TABLE `orders` DROP COLUMN `legacy`;",
		"ALTER TABLE orders ADD COLUMN flag TINYINT, ALGORITHM=INSTANT;",
		"CREATE TABLE `kinds` AS SELECT DISTINCT `kind` AS `kind` FROM `orders` WHERE `kind` IS NOT NULL;",
		"ALTER TABLE `kinds` MODIFY `kind` varchar(20) NOT NULL;",
		"ALTER TABLE `orders` ADD CONSTRAINT `FK_ORDERS_KINDS` FOREIGN KEY (`kind`) REFERENCES `kinds` (`kind`);",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "pg_only") {
		t.Error("a postgresql-only changeSet must be skipped when targeting MySQL")
	}
	if strings.Contains(sql, `"`) {
		t.Errorf("MySQL identifiers must use backticks, not double quotes:\n%s", sql)
	}
	if c.Properties["now"] != "CURRENT_TIMESTAMP" {
		t.Errorf("the mysql property must win: %q", c.Properties["now"])
	}
}

func TestPostgresChangelogSkipsMySQLOnlyChangesets(t *testing.T) {
	xml := `<databaseChangeLog><changeSet id="1" author="a" dbms="mysql"><createIndex tableName="t" indexName="i"><column name="a"/></createIndex></changeSet>
<changeSet id="2" author="a"><createIndex tableName="t" indexName="j"><column name="a"/></createIndex></changeSet></databaseChangeLog>`
	pg, _ := ParseFor("c.xml", []byte(xml), EnginePostgres)
	my, _ := ParseFor("c.xml", []byte(xml), EngineMySQL)
	if len(pg.Changesets) != 1 || pg.Changesets[0].ID != "2" || len(my.Changesets) != 2 {
		t.Fatalf("pg=%d my=%d", len(pg.Changesets), len(my.Changesets))
	}
}

func TestMySQLNeedsTypeForNotNullAndLookup(t *testing.T) {
	xml := `<databaseChangeLog><changeSet id="1" author="a"><addNotNullConstraint tableName="t" columnName="c"/></changeSet></databaseChangeLog>`
	c, _ := ParseFor("c.xml", []byte(xml), EngineMySQL)
	if p := c.Combine(nil).Problems; len(p) != 1 || !strings.Contains(p[0], "columnDataType") {
		t.Fatalf("a MySQL addNotNullConstraint without columnDataType cannot be translated: %v", p)
	}
}
