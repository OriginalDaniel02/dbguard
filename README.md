<div align="center">

# DB Guard

**Catch migrations that will lock production — before they merge.**

Two tools in one binary. A **pre-flight checker** reads your Flyway or Liquibase migration, looks at how big the target tables really are, and tells you which statements will block writes, for roughly how long, and what to do instead. A **schema drift detector** catches when staging and production have quietly diverged from what your migrations say.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![CI](https://github.com/OriginalDaniel02/dbguard/actions/workflows/ci.yml/badge.svg)](https://github.com/OriginalDaniel02/dbguard/actions/workflows/ci.yml)
![Status](https://img.shields.io/badge/status-v0.1%20pre--release-orange)
![Go](https://img.shields.io/badge/built%20with-Go-00ADD8)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-12%2B-336791)
![MySQL](https://img.shields.io/badge/MySQL-8.0%2B-4479A1)

</div>

---

## Why DB Guard

An engineer adds an index to a table. Locally and in staging it is instant, because those tables are small. In production the same statement holds a lock for minutes, every write to that table queues behind it, and the first signal is the on-call pager.

Knowing which operations are safe at scale usually lives in one senior engineer's head. DB Guard puts that knowledge into your CI pipeline, where a risky migration can't slip through quietly.

```text
db/migration/V3__add_index.sql:4: [HIGH] public.transactions (create-index)
    this will lock public.transactions (14.0M rows) for approximately 1-6 min
    safer: CREATE INDEX CONCURRENTLY (must run outside a transaction; in Flyway set executeInTransaction=false)
dbguard: 1 blocking finding(s)
```

## Features

- **Knows what the SQL really does.** Statements are parsed with the actual PostgreSQL parser (`libpg_query`), not regular expressions, so rules are based on the operation, not on how the SQL happens to be written.
- **Uses real table sizes.** It reads row-count estimates from `pg_stat_user_tables` / `pg_class` to turn a flat warning into "14.0M rows, about 1–6 minutes".
- **Suggests the safer path.** Every finding includes the alternative (`CREATE INDEX CONCURRENTLY`, `NOT VALID` constraints, add-backfill-swap, and so on).
- **Fewer false alarms.** Small tables are downgraded, tables created in the same migration are ignored, and only *volatile* column defaults are treated as rewrites.
- **CI is the enforcement point.** A GitHub Action comments on the pull request and fails the check. The editor is optional; the pipeline is not.
- **Auditable overrides.** Accept a risk with a comment in the migration file itself, so the decision lives in git history and code review.
- **Read-only by construction.** Every session is forced read-only. DB Guard never needs, and cannot use, write access.
- **PostgreSQL and MySQL.** Rules for each engine's real locking behavior, measured against real servers.
- **Zero-dependency install.** One static binary. No JVM, no runtime, no C toolchain.

## Quick start

```bash
# 1. Get the binary (see "Installation" for all options)
go install github.com/OriginalDaniel02/dbguard/cmd/dbguard@latest

# 2. Check a migration against a live database (read-only connection string)
export DBGUARD_DSN='postgres://readonly_user:***@db.internal:5432/app'
dbguard check db/migration

# ...or with no database, by telling it table sizes
dbguard check --rows transactions=14000000 db/migration/V3__add_index.sql
```

DB Guard exits `0` when nothing blocks, `1` when it finds blocking risks, and `2` on errors (bad SQL, connection failure, bad flags).

## How it works

```text
 Flyway .sql file ──► parse (libpg_query) ──► rules engine ──► estimate ──► report
                                                  ▲
                              table sizes + server version
                              (read-only connection)
```

1. **Collect.** Explicit files are checked as given; directories are searched for Flyway-named files (`V<version>__<description>.sql`, `R__<description>.sql`).
2. **Parse.** The SQL is parsed into PostgreSQL's own syntax tree.
3. **Evaluate.** Each statement is matched against the risk rules, using the table's estimated size and the server's major version.
4. **Estimate.** For size-dependent operations, a duration *range* is computed from the row count.
5. **Report.** Output as text, JSON, or a Markdown pull-request comment.

## Risk rules

| Operation | What PostgreSQL does | Risk | Suggested alternative |
|---|---|---|---|
| `ADD COLUMN` with no default or a constant default | Metadata-only change (PG 11+) | Safe — not reported | — |
| `ADD COLUMN` with a **volatile** default (`random()`, `clock_timestamp()`, `gen_random_uuid()`, `serial`, identity, stored generated) | Full table rewrite under `ACCESS EXCLUSIVE` | High | Add nullable, backfill in batches, then set the default |
| `ALTER COLUMN ... TYPE` | Full table rewrite under `ACCESS EXCLUSIVE` | High | Add a new column, backfill, swap, drop the old one |
| `CREATE INDEX` (not `CONCURRENTLY`) | `SHARE` lock blocks writes for the whole build | High | `CREATE INDEX CONCURRENTLY` |
| `ADD UNIQUE` / `ADD PRIMARY KEY` (without `USING INDEX`) | `ACCESS EXCLUSIVE` while the index builds | High | Build the index `CONCURRENTLY`, then `ADD CONSTRAINT ... USING INDEX` |
| `SET NOT NULL` | Full scan under `ACCESS EXCLUSIVE` (not flagged on PG 12+ when a validated `CHECK (col IS NOT NULL)` already exists in the database or was added earlier in the same migration) | Medium-high | `NOT VALID` check, `VALIDATE` separately, then `SET NOT NULL` |
| `ADD FOREIGN KEY` (not `NOT VALID`) | Locks both tables and scans the referencing table | Medium-high | Add `NOT VALID`, then `VALIDATE CONSTRAINT` in a separate step |
| `DROP COLUMN` | Metadata-only, but running code reading the column breaks | Low | Deploy code that stops reading the column first |

### What is deliberately *not* flagged

- **`DEFAULT now()` and `CURRENT_TIMESTAMP`.** These are evaluated once at `ALTER` time and stored as metadata on PG 11+, so there is no rewrite. This was verified empirically against PostgreSQL 16; `clock_timestamp()` and `random()` do rewrite and are flagged. Unknown functions are assumed volatile.
- **Small tables.** Tables with fewer estimated rows than `--large-rows` (default 100,000) are downgraded to *low*.
- **Tables created in the same migration.** They are empty, so there is nothing to lock against.

### Duration estimates are ranges, not promises

Actual lock time depends on load, concurrent queries, hardware, `maintenance_work_mem`, and column types. DB Guard reports a range derived from throughput measured on PostgreSQL 16 (about 3M rows on a laptop for the slow end, widened for server-class hardware at the fast end). Treat them as an order-of-magnitude warning, and calibrate the constants in [`internal/estimate`](internal/estimate/estimate.go) for your own environment.

## MySQL

`dbguard check` also analyzes MySQL migrations. MySQL's question is not *which lock* but *which online-DDL algorithm*: `INSTANT` and `INPLACE`/`LOCK=NONE` keep writes flowing, while `COPY` blocks them. DB Guard classifies each statement accordingly:

```bash
export DBGUARD_DSN='mysql://readonly:...@db.internal:3306/shop'   # engine is detected from the URL
dbguard check db/migration
dbguard check --engine mysql --rows orders=14000000 V3__add_check.sql   # offline
```

```text
V3__add_check.sql:1: [HIGH] orders (add-check-constraint)
    this will block writes to orders (14.0M rows) for approximately 2-20 min
    safer: Add the CHECK to the CREATE TABLE of a new table, or enforce it in the application; use gh-ost / pt-online-schema-change for existing big tables
```

The classification was **measured on a real MySQL 8.0.46** rather than copied from the manual, and some results are surprising: `ADD CHECK` and `ADD FOREIGN KEY` copy the whole table, and any parenthesized `DEFAULT (...)`, even `DEFAULT (5)`, forces a copy. Connected to the database, DB Guard reads the live column definition to tell a type change (copy) from a nullability change (online). See [docs/mysql.md](docs/mysql.md) for the full table, the version gates (8.0.29) and the caveats (metadata locks).

## Schema drift detection

During an incident someone runs a manual `ALTER TABLE` on production and forgets the migration. Weeks later a new migration assumes the old schema and fails, and nobody knows why the environments don't match. `dbguard drift` compares each environment's live schema with the schema your migrations should produce and says exactly what differs:

```bash
# expected.json: snapshot of a scratch DB after running your migrations
dbguard drift --expected expected.json   --env staging=STAGING_DSN --env production=PROD_DSN   --ignore-file .dbguard-ignore --slack-env SLACK_WEBHOOK
```

```text
staging: no drift
production: 2 difference(s)
  - column public.orders.discount was added outside migrations (numeric)
  - index public.orders.orders_total_idx is missing (expected CREATE INDEX orders_total_idx ON ...)
```

It runs as a scheduled job (a ready-made GitHub Actions workflow is in [`examples/drift-check.yml`](examples/drift-check.yml)), is read-only, supports ignore patterns for intentional differences, and posts to Slack when it finds drift. See [docs/drift.md](docs/drift.md).

## Installation

### Prebuilt binary

Tagged releases publish static binaries (Linux, macOS, Windows; amd64 and arm64 where applicable) with a `checksums.txt` on the [Releases](https://github.com/OriginalDaniel02/dbguard/releases) page.

```bash
sha256sum -c --ignore-missing checksums.txt
```

### With Go

```bash
go install github.com/OriginalDaniel02/dbguard/cmd/dbguard@latest
```

### From source

```bash
git clone https://github.com/OriginalDaniel02/dbguard.git
cd dbguard
CGO_ENABLED=0 go build -o dbguard ./cmd/dbguard
```

No C compiler is required; the PostgreSQL parser is compiled to WebAssembly and embedded.

## Usage

```text
dbguard check [flags] <migration-file-or-dir>...
```

| Flag | Default | Description |
|---|---|---|
| `--format` | `text` | Output format: `text`, `markdown` (PR comment body), or `json` |
| `--fail-on` | `medium-high` | Lowest risk that fails the check: `low`, `medium`, `medium-high`, `high` |
| `--large-rows` | `100000` | Tables below this estimated row count are treated as low risk |
| `--dsn-env` | `DBGUARD_DSN` | Name of the environment variable holding the read-only connection string |
| `--rows` | — | Offline table size as `table=rows` (repeatable); used when no DSN is set |
| `--pg-version` | `16` | Assumed PostgreSQL major version when not connected |
| `--schema` | *(connection's `current_schema()`, else `public`)* | Schema for unqualified table names |
| `--placeholder` | — | Flyway placeholder value as `name=value` (repeatable) |

If neither a connection string nor `--rows` is provided, table sizes are unknown and DB Guard conservatively assumes tables are large. The same applies to a table that has data but **no statistics** (never analyzed): it is reported as unknown, never as "0 rows", so a freshly loaded big table can't slip through as small. Run `ANALYZE` on it, or pass `--rows`.

### Examples

```bash
# Everything under a directory, JSON output for other tooling
dbguard check --format json db/migration

# Only fail on the most dangerous operations
dbguard check --fail-on high db/migration

# Fully offline, specifying sizes for two tables (schema defaults to public)
dbguard check --rows transactions=14000000 --rows audit.events=900000000 V5__x.sql

# Migration uses Flyway placeholders: ${schema}.orders
dbguard check --placeholder schema=app --rows app.orders=5000000 V6__x.sql
```

## Liquibase

DB Guard checks Liquibase changelogs in **XML, YAML, JSON and formatted SQL** with the same risk rules, so no second tool or config is needed:

```bash
dbguard check db/changelog/                      # finds changelogs by content
dbguard check db/changelog/2026-10-add-index.xml
```

```text
db/changelog/2026-10-add-index.xml:33: [HIGH] public.transactions (create-index)
    this will lock public.transactions (14.0M rows) for approximately 1-6 min
    safer: CREATE INDEX CONCURRENTLY in a <sql> change with runInTransaction="false" (the createIndex change type cannot do this)
```

How it works: each `<changeSet>` is translated into the equivalent PostgreSQL statements and analyzed together (so an index on a table created earlier in the same changelog is not flagged). Findings are reported at the line of the `changeSet`. `<property>` values act as `${placeholders}` (respecting `dbms=`), and `dbms=` restrictions on changeSets are honored (a changeSet that only runs on Oracle is skipped).

**Supported change types:** `createTable`, `addColumn` (incl. `defaultValue*`, `autoIncrement`, `nullable`), `dropColumn`, `modifyDataType`, `addNotNullConstraint`, `addUniqueConstraint`, `addPrimaryKey`, `addForeignKeyConstraint` (`validate="false"` is treated as `NOT VALID`), `createIndex`, `addLookupTable`, `sql` and `sqlFile`. Metadata-only or data-only changes (renames, defaults, views, `insert`, ...) are skipped silently. **Any other change type is listed as "not analyzed" in the output** so you can see what was not checked.

**Accepting a risk** works in every format, because the decision belongs in the file:

```xml
<!-- dbguard:ignore add-not-null reason: backfilled and enforced by the app since v41 -->
<changeSet id="5" author="dev"> ... </changeSet>
```

```yaml
  # dbguard:ignore add-not-null reason: backfilled and enforced by the app since v41
  - changeSet:
```

or, in any format including JSON (which has no comments), in the changeSet's own comment:

```yaml
      comment: "dbguard:ignore create-index reason: write-quiet during the maintenance window"
```

## GitHub Actions

Add a workflow that runs on pull requests:

```yaml
name: DB Guard
on:
  pull_request:
permissions:
  contents: read
  pull-requests: write
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: OriginalDaniel02/dbguard/action@v0.1.0
        with:
          version: v0.1.0
          dsn: ${{ secrets.DBGUARD_READONLY_DSN }}
          migrations-path: db/migration
```

| Input | Default | Description |
|---|---|---|
| `dsn` | — | Read-only PostgreSQL connection string. Pass it from a secret. |
| `migrations-path` | `db/migration` | Only changed `.sql` files under this path are checked |
| `fail-on` | `medium-high` | Lowest risk that fails the check |
| `version` | *(empty)* | Release to install (checksum-verified). Empty builds from the action's checkout |

Do not add a `paths:` filter to this workflow. The action only looks at changed migration files and does nothing otherwise, whereas a path filter skips the run entirely: a stale warning stays on the PR after the risky migration is removed, and if you mark the check as *required*, PRs that don't touch migrations would wait forever for a check that never starts.

On each run the action posts **one** comment on the pull request and keeps it up to date: it is edited in place as the PR changes, and updated (never left stale) if the risky migration is later removed. A failing check blocks the merge once you make the check required in branch protection.

## GitLab CI

The same check runs on GitLab merge requests. Include the template in your `.gitlab-ci.yml`:

```yaml
include:
  - remote: https://raw.githubusercontent.com/OriginalDaniel02/dbguard/v0.2.0/gitlab/dbguard.gitlab-ci.yml
```

Then add these CI/CD variables (Settings > CI/CD > Variables, masked):

| Variable | Description |
|---|---|
| `DBGUARD_GITLAB_TOKEN` | Project or group access token with the `api` scope and the Developer role. `CI_JOB_TOKEN` cannot post merge request comments. |
| `DBGUARD_DSN` | *(optional)* read-only PostgreSQL connection string, for real table sizes |

Optional overrides: `DBGUARD_VERSION`, `DBGUARD_MIGRATIONS_PATH` (default `db/migration`), `DBGUARD_FAIL_ON`.

The job posts **one** merge request comment, edits it in place on every push, resolves it when the risky migration is removed, and fails when there are blocking findings (make the pipeline required to block the merge). Under the hood it uses `dbguard gitlab-comment`, which you can also call yourself: `dbguard check --format markdown ... | dbguard gitlab-comment`.

## Accepting a risk

Sometimes a flagged change is acceptable: the table is idle during your deploy window, or the warning is a false positive. Acknowledge it **in the migration file**, directly above the statement:

```sql
-- dbguard:ignore create-index reason: table is write-quiet during the 03:00 deploy window
CREATE INDEX idx_transactions_amount ON transactions (amount);
```

- A `reason:` is **mandatory**. An ignore without one is rejected and reported.
- The comment must match the rule id and sit above (or inside) the statement it covers.
- An ignore that matches nothing is reported, so stale overrides don't accumulate.
- Because the override lives in the file, it appears in `git blame`, code review, and the PR comment.

Rule ids: `add-column-nonconstant-default`, `alter-column-type`, `create-index`, `add-unique-or-pk`, `add-not-null`, `add-foreign-key`, `drop-column`.

## Security

DB Guard is designed to be safe to hand a credential to.

- **Read-only enforced twice.** Sessions are opened with `default_transaction_read_only=on`, and every query runs inside a `READ ONLY` transaction. This is covered by an integration test that proves a `CREATE TABLE` is rejected even when the role itself could write.
- **No data reads.** Only schema metadata and planner row estimates are queried (`pg_class`, `pg_stat_user_tables`, the server version). Table contents are never read.
- **Credentials are never echoed.** The connection string is read from an environment variable and redacted from error messages; it is never written to logs or PR comments. Covered by a test.
- **Still use a dedicated, least-privilege role.** Even a read-only credential exposes schema and size information if leaked. Create a role that can only connect and read the catalogs, store it as a CI secret, and rotate it like any other.

```sql
CREATE ROLE dbguard LOGIN PASSWORD '...';
GRANT CONNECT ON DATABASE app TO dbguard;
-- pg_class / pg_stat_user_tables are readable by any role; no table grants needed.
```

Please report vulnerabilities privately through GitHub's *Security → Report a vulnerability* rather than in a public issue.

## Current scope and limitations

DB Guard is at **v0.1 (Phase 1)**. Being clear about what it does not do yet:

- **PostgreSQL and MySQL** (8.0/8.4; 5.7 approximated). MariaDB is detected and warned about: its online DDL differs. The schema drift detector is PostgreSQL only for now.
- **Flyway SQL and Liquibase changelogs only.** Java-based Flyway migrations and Liquibase custom change classes are not read. Liquibase `include`/`includeAll` are not followed; changed files are checked individually, which is what CI passes.
- **Flyway placeholders** (`${name}`) are understood: supply values with `--placeholder name=value`, otherwise they are treated as opaque names (so a table behind an unset `${schema}` has unknown size and is assumed large). A placeholder in a *value* position (e.g. `DEFAULT ${x}`) is treated conservatively.
- **`ALTER TABLE` / `CREATE INDEX` coverage.** The rules in the table above are what is detected today. Other statements are ignored, not validated.
- **Estimates are heuristics.** See [Duration estimates](#duration-estimates-are-ranges-not-promises).
- **Unqualified names** use the connection's `current_schema()` (or `--schema`, else `public`); a `SET search_path` inside the migration is not followed.
- **Estimates don't consider column type or width.** A text-column index build is slower than an integer one; the range is deliberately wide to cover both.
- DB Guard **warns and suggests**; it never rewrites your migration files.

## Roadmap

| Phase | Scope | Status |
|---|---|---|
| **1** | Go CLI for PostgreSQL, full risk-rule table, Flyway SQL, GitHub Action with PR comments, auditable overrides | Implemented |
| **2** | **Schema drift detection** (snapshots, comparison, Slack alerts, scheduled job) | Implemented |
| | GitLab CI | Implemented |
| | Liquibase | Implemented |
| | MySQL (`check`) | Implemented |
| **3** | VS Code extension for inline feedback, per-table schema changelog | Planned |

Schema drift detection answers a different question — *has someone changed production by hand?* — and runs as a separate scheduled job, independent of the pull-request check. The drift detector currently covers PostgreSQL tables, columns, indexes and constraints (see [docs/drift.md](docs/drift.md) for limits).

## Development

```bash
go test ./...        # unit tests (CI also runs the integration tests against PostgreSQL 16)
go vet ./...
```

The integration tests require a PostgreSQL instance and are skipped unless a connection string is provided:

```bash
docker run -d --name dbguard-pg -e POSTGRES_PASSWORD=secret -p 55432:5432 postgres:16
DBGUARD_TEST_DSN='postgres://postgres:secret@localhost:55432/postgres?sslmode=disable' \
  go test ./internal/pg
```

### Project layout

```text
cmd/dbguard        CLI entry point (check, snapshot, drift, gitlab-comment)
internal/flyway    Flyway migration discovery
internal/liquibase Liquibase changelog parsing and translation
internal/rules     Risk rules engine (parses SQL, produces findings)
internal/estimate  Lock-duration range estimates
internal/pg        Read-only PostgreSQL statistics and schema snapshots
internal/mysqldb   Read-only MySQL statistics
internal/override  Auditable in-file overrides
internal/snapshot  Normalized schema snapshot model (JSON)
internal/drift     Snapshot comparison and ignore rules
internal/notify    Slack alerts
internal/report    Text / Markdown / JSON output
action/            GitHub Action
gitlab/            GitLab CI template
internal/gitlab    GitLab merge request comments
docs/              Rule reference, drift guide, acceptance criteria
examples/          Ready-to-copy workflows (scheduled drift check)
testdata/          Sample migrations (Flyway, Liquibase XML/YAML/JSON/SQL)
```

## Contributing

Issues and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). Security reports: [SECURITY.md](SECURITY.md). The thing that matters most for this tool is **accuracy**: a rule that cries wolf gets the whole tool ignored. If you open a false-positive or false-negative report, include the migration SQL, the PostgreSQL version, and what actually happened in your database. New rules should come with a test and, ideally, evidence of the real lock behavior.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
