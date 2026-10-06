# Flyway Java migrations

Flyway lets you write a migration as a Java class (`V2__Add_index.java`, extending `BaseJavaMigration`) that runs SQL through
JDBC. DB Guard checks these too, with the same rules, engines and output as SQL migrations.

```bash
dbguard check src/main/java/db/migration/
dbguard check --rows transactions=14000000 src/main/java/db/migration/V3__AddTransactionIndex.java
```

```text
V3__AddTransactionIndex.java:11: [HIGH] public.transactions (create-index)
    this will lock public.transactions (14.0M rows) for approximately 1-6 min
    safer: CREATE INDEX CONCURRENTLY (it cannot run inside a transaction: override canExecuteInTransaction() to return false in this migration)
```

## How it works

DB Guard **never runs your Java**. It reads the source with a real Java lexer, finds the SQL strings, and analyzes them:

- plain strings, with every escape sequence, and **text blocks** (`"""`), evaluated exactly as Java evaluates them. This is
  tested against the real JDK: a differential test compiles tricky literals with `javac` and requires byte-identical results;
- **concatenation** across lines (`"ALTER TABLE orders " + "ADD COLUMN note text"`);
- `String.format("ALTER TABLE %s ...", table)` templates, and parts built from variables or method calls
  (`"ALTER TABLE " + table + " ..."`): the unknown part becomes a placeholder, the statement is still analyzed, and the table is
  assumed large. Every such statement is listed as a caveat so you can see it;
- all the statements in one migration are analyzed **together**, so a table created at the top is known to be new when an index
  is created on it further down.

Only DDL that DB Guard has rules for is considered (`ALTER TABLE`, `CREATE INDEX`/`TABLE`/`VIEW`, `DROP`, `OPTIMIZE TABLE`,
`SET foreign_key_checks`). Log messages, comments, commented-out code and DML (`INSERT`, `UPDATE`) are not analyzed. Findings are
reported on the **Java line** of the statement, including statements inside a text block.

## Which files are checked

Java files named like Flyway migrations (`V2__Name.java`, `R__Name.java`), or any class that references Flyway's migration API
(`BaseJavaMigration`, `JavaMigration`). An ordinary helper class in the same folder is skipped. Pass files explicitly or a
folder; the GitHub Action and GitLab template include `.java` files under your migrations path.

## Resolving dynamic SQL

A part built at runtime is named after its Java expression. Give it a value with `--placeholder`, and the statement is analyzed
against the real table:

```java
String table = "transactions";
st.execute("ALTER TABLE " + table + " ADD CONSTRAINT ...");   // the placeholder is named: table
```

```bash
dbguard check --placeholder table=transactions --rows transactions=14000000 V7__X.java
```

`String.format` arguments are not named (`%s` becomes `arg`), so they cannot be resolved this way.

## Accepting a risk

Put a `//` comment directly above the statement:

```java
// dbguard:ignore create-index reason: transactions is write-quiet during the 03:00 deploy window
st.execute("CREATE INDEX idx_transactions_ref ON transactions (ref)");
```

A reason is mandatory, exactly as for SQL. Comment lines may sit between the directive and the statement, but code may not.

## CREATE INDEX CONCURRENTLY in a Java migration

PostgreSQL cannot build an index concurrently inside a transaction, and Flyway runs a Java migration in one by default. Override
`canExecuteInTransaction()` in that migration:

```java
@Override
public boolean canExecuteInTransaction() { return false; }
```

## Limits

- **SQL that is not in the source is not seen**: read from a resource file, built by a `StringBuilder` across several statements,
  or produced by a helper method. A migration with no visible SQL is reported as such ("no SQL statements were found"), never
  as clean.
- A string that starts with a dynamic part (`prefix + " ADD COLUMN x"`) is not recognized as SQL.
- Spring `JdbcTemplate`, plain JDBC and `Statement.execute` all work, because only the string literals matter. SQL built through a
  query-builder DSL does not.
- Kotlin and Scala migrations are not read.

## CI paths

Java migrations normally live under `src/main/java/db/migration`, not `db/migration`. Point CI at the folder that holds yours:

```yaml
# GitHub Action
- uses: OriginalDaniel02/dbguard/action@v0.4.0
  with:
    migrations-path: src/main/java/db/migration
```

```yaml
# GitLab: set the CI/CD variable
DBGUARD_MIGRATIONS_PATH: src/main/java/db/migration
```
