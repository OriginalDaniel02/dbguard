# MySQL support

`dbguard check` also works on MySQL (8.0 and 8.4; 5.7 is approximated). Select the engine with `--engine mysql`, or
let it follow the connection string (`mysql://user:pass@host:3306/db`).

```bash
export DBGUARD_DSN='mysql://readonly:...@db.internal:3306/shop'
dbguard check db/migration                        # Flyway SQL
dbguard check --engine mysql --rows orders=14000000 V3__x.sql   # offline
dbguard check --engine mysql --mysql-version 8.0.28 --rows orders=14000000 V3__x.sql
```

## What is different from PostgreSQL

PostgreSQL's risk is *which lock a statement takes*. MySQL's is *which online-DDL algorithm it can use*:

| Algorithm | Meaning |
|---|---|
| `INSTANT` | Metadata only, no table touched |
| `INPLACE` + `LOCK=NONE` | Runs online: reads **and writes continue**. May still rebuild the table (heavy I/O, replica lag) |
| `INPLACE` + `LOCK=SHARED` | Runs in place but **blocks writes** |
| `COPY` | Copies the whole table and **blocks writes** |

## How the rules were established

The classification below was **measured, not copied from documentation**. Every statement was run on a real
MySQL **8.0.46** (`mysql:8.0`) against a table of 20,000 rows, trying `ALGORITHM=INSTANT`, then `INPLACE, LOCK=NONE`,
then `INPLACE, LOCK=SHARED`, then `COPY`; the first one the server accepts is the result. The version gates
(8.0.29) were confirmed by repeating the experiment on **8.0.28**.

| Statement | Result on 8.0.46 | DB Guard rule | Risk |
|---|---|---|---|
| `ADD COLUMN` (last, `AFTER`, `FIRST`, `NOT NULL DEFAULT`, `DEFAULT CURRENT_TIMESTAMP`, virtual generated) | INSTANT | none | safe |
| `ADD COLUMN ... DEFAULT (expression)`, even `DEFAULT (5)` or `DEFAULT (NOW())` | **COPY only** | `add-column-nonconstant-default` | high |
| `ADD COLUMN ... AS (...) STORED` | COPY | `table-copy` | high |
| `ADD COLUMN ... AUTO_INCREMENT` | INPLACE, writes **blocked** | `blocks-writes` | high |
| `DROP COLUMN` | INSTANT | `drop-column` (app-breaking) | low |
| `MODIFY` int to bigint, shrinking a `VARCHAR`, widening past the 255-byte length-prefix boundary | COPY | `alter-column-type` | high |
| `MODIFY` widening a `VARCHAR` within the same length-prefix size | INPLACE, online | none | safe |
| `MODIFY` nullability only, or moving a column | INPLACE rebuild, online | `table-rebuild` | medium |
| `CHANGE` as a pure rename, `RENAME COLUMN`, `SET DEFAULT` | INSTANT | none | safe |
| `ADD INDEX` / `ADD UNIQUE` / `CREATE INDEX` | INPLACE, online | `create-index` | low |
| `ADD FULLTEXT` (first on the table) | INPLACE, writes **blocked** | `create-index` | high |
| `ADD PRIMARY KEY` | INPLACE rebuild, online | `add-unique-or-pk` | medium |
| `DROP PRIMARY KEY` alone | COPY | `table-copy` | high |
| `DROP PRIMARY KEY, ADD PRIMARY KEY` in one statement | INPLACE rebuild, online | `add-unique-or-pk` | medium |
| `ADD FOREIGN KEY` with `foreign_key_checks=1` (default) | **COPY** | `add-foreign-key` | high |
| `ADD FOREIGN KEY` after `SET foreign_key_checks=0` | INPLACE, online (existing rows **not validated**) | `add-foreign-key` | medium |
| `ADD CHECK` | **COPY** | `add-check-constraint` | high |
| `CONVERT TO CHARACTER SET` | COPY | `table-copy` | high |
| `ENGINE=InnoDB`, `ROW_FORMAT=`, `FORCE`, `OPTIMIZE TABLE` | INPLACE rebuild, online | `table-rebuild` | medium |
| `DEFAULT CHARSET=`, `COMMENT=`, `AUTO_INCREMENT=` | in place, no rebuild | none | safe |

**Version gates.** Before MySQL **8.0.29**, `ADD COLUMN ... AFTER/FIRST` and `DROP COLUMN` are *not* instant: they
rebuild the table online (`table-rebuild`, medium). Before 8.0.12 even a plain `ADD COLUMN` rebuilds. Pass
`--mysql-version` when running offline; with a connection DB Guard reads the server version itself.

### Surprises worth knowing

- **Any parenthesized `DEFAULT` is a table copy**, even `DEFAULT (5)`. Only a bare literal, a negative number, or
  `CURRENT_TIMESTAMP` is instant.
- **`ADD CHECK` copies the table** and blocks writes.
- **`ADD FOREIGN KEY` copies the table** unless `foreign_key_checks` is off.
- A `MODIFY COLUMN` only *looks* like a type change: the statement does not say what the column was. Connected to the
  database, DB Guard reads the current definition and tells a type change (copy) from a nullability change (online).
  Offline it assumes the type changes, which is conservative, and says so.

### Explicit `ALGORITHM` / `LOCK`

`ALTER TABLE ... ALGORITHM=INPLACE, LOCK=NONE` (or `ALGORITHM=INSTANT`) makes MySQL **refuse** the statement with an
error instead of silently blocking writes. DB Guard treats a statement carrying `LOCK=NONE` or `ALGORITHM=INSTANT` as
guarded and reports nothing for it. `ALGORITHM=COPY`, `LOCK=SHARED` and `LOCK=EXCLUSIVE` are reported as blocking.
This is the safer alternative DB Guard suggests for most findings.

## Metadata locks (not detected)

Even an `INSTANT` change must briefly take a metadata lock, and it queues behind any long-running transaction on the
table; every later query then queues behind the `ALTER`. DB Guard cannot see your transactions, so run risky changes
with a low `lock_wait_timeout` (for example `SET SESSION lock_wait_timeout = 5;`) so a blocked `ALTER` fails fast
instead of stalling the table.

## Statistics

Row counts come from `information_schema.TABLES`. MySQL caches those statistics for 24 hours
(`information_schema_stats_expiry`), so a table bulk-loaded after the cache was filled would report 0 rows. DB Guard sets
`information_schema_stats_expiry = 0` for its own read-only session, and never runs `ANALYZE TABLE`. A table that has
data but no statistics is reported as unknown (assumed large), not as empty.

## Limits

- Flyway SQL migrations and Liquibase changelogs (XML, YAML, JSON, formatted SQL). For Liquibase, `addNotNullConstraint` needs `columnDataType` and `addLookupTable` needs `newColumnDataType` on MySQL (Liquibase itself requires them); a change without them is listed as not analyzed. Liquibase's `modifyDataType` emits `MODIFY col TYPE` without `NOT NULL`, exactly as Liquibase does.
- MariaDB is detected and warned about: its online DDL differs from MySQL 8.
- A statement the MySQL parser cannot read is listed as "not analyzed" instead of failing the file. The mysql
  client's `DELIMITER` directive (stored procedures, triggers) is not supported.
- Estimates are ranges derived from measurements on one machine (MySQL 8.0.46 in Docker on a laptop, 3M rows: table copies 13k-31k rows/s, in-place rebuilds ~38k-40k, index builds 62k-100k), widened for faster hardware. Calibrate the constants in `internal/estimate` for yours.
