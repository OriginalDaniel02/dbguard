# Schema drift detection

Drift is when a live database no longer matches what your migrations say it should be, usually because
someone ran a manual `ALTER TABLE` during an incident and never wrote the migration. Nothing notices until a
later migration assumes the old schema and fails.

DB Guard detects this with two commands and a scheduled job. It is independent of the pull-request check.

## How it works

```text
 migrations ──(your tool, scratch DB)──► dbguard snapshot ──► expected.json ┐
                                                                            ├─► dbguard drift ──► text / JSON / Slack
 staging ─┐                                                                 │
 production ┴─(read-only)──► dbguard snapshot (internal) ────────────────────┘
```

DB Guard is **not** a migration runner. To know what the schema *should* be, run your migrations
(Flyway, Liquibase, anything) against an empty scratch database and snapshot the result. That is exact for any
migration tool, including ones DB Guard cannot parse. Alternatively, compare environments to each other
with `--baseline-env`.

## MySQL

Use a `mysql://` connection string that names the database. The same commands work; the snapshot is read from
`information_schema`. In MySQL a schema is a database, and staging and production usually have different database
names, so tables are recorded **without** the database name: the same schema in `shop_prod` and `shop_staging`
compares as identical (verified against a real server). Ignore patterns therefore look like `orders` and
`orders.debug_*`, not `shop.orders`. A snapshot records its engine, and DB Guard refuses to compare a MySQL
environment with a PostgreSQL expected schema.

```bash
DBGUARD_DSN='mysql://readonly:...@db.internal:3306/shop' dbguard snapshot --out expected.json
dbguard drift --expected expected.json --env staging=STAGING_DSN --env production=PROD_DSN
```

## Commands

### `dbguard snapshot`

Captures a normalized snapshot (tables, columns with type/nullability/default, indexes, constraints, triggers, views and materialized views, sequences, enum types) as JSON,
from catalog queries only, in a read-only transaction. No table data is read.

```bash
DBGUARD_DSN='postgres://readonly@host/db' dbguard snapshot --out expected.json
```

### `dbguard drift`

```bash
# each environment vs. the expected schema
dbguard drift --expected expected.json \
  --env staging=STAGING_DSN --env production=PROD_DSN

# or: every environment vs. one baseline environment
dbguard drift --baseline-env staging --env staging=STAGING_DSN --env production=PROD_DSN
```

`--env name=VAR` takes the *name* of an environment variable holding that environment's DSN, so connection
strings never appear on the command line or in logs.

| Flag | Description |
|---|---|
| `--expected FILE` | Snapshot the environments should match |
| `--baseline-env NAME` | Compare all other `--env` entries against this one (instead of `--expected`) |
| `--env name=VAR` | Environment to check (repeatable) |
| `--ignore GLOB` | Ignore a table or object (repeatable), e.g. `public.feature_flags`, `public.accounts.debug_*` |
| `--ignore-file FILE` | One ignore pattern per line; `#` comments |
| `--slack-env VAR` | Env var holding a Slack incoming-webhook URL; alerts only when drift is found |
| `--state-file FILE` | Remember what was alerted. Slack is told when drift is **new or changed**, as a **reminder** if it stays unresolved, and when it is **resolved**, not on every run |
| `--realert-after DUR` | With `--state-file`: remind after this long (default `168h`; `0` = never remind) |
| `--save-dir DIR` | Keep every snapshot as `DIR/<env>/<UTC timestamp>.json` for history |
| `--format text\|json` | Output format |

Exit codes: `0` no drift, `1` drift found, `2` error (could not connect, bad flags, bad snapshot file). The exit code reflects the *current* state on every run; the state file only controls Slack noise.

Example output:

```text
staging: no drift
production: 2 difference(s)
  - column public.orders.discount was added outside migrations (numeric)
  - index public.orders.orders_total_idx is missing (expected CREATE INDEX orders_total_idx ON public.orders USING btree (total))
```

## What counts as drift

| Difference | Meaning |
|---|---|
| `table-missing` / `table-extra` | Table expected but absent, or present but not in migrations |
| `column-missing` / `column-extra` | Column expected but absent, or added outside migrations |
| `column-type-changed` | Different data type |
| `column-nullability-changed` | `NULL` / `NOT NULL` differs |
| `column-default-changed` | Default, identity, or generated expression differs |
| `index-missing` / `index-extra` / `index-changed` | Index added, dropped, or redefined |
| `constraint-missing` / `constraint-extra` / `constraint-changed` | Primary/unique/foreign/check constraint added, dropped, redefined, or left `NOT VALID` |
| `trigger-missing` / `trigger-extra` / `trigger-changed` | User-defined trigger dropped, added, or redefined (foreign-key triggers are internal and ignored) |
| `view-missing` / `view-extra` / `view-changed` | View or materialized view dropped, added, or its definition changed |
| `sequence-missing` / `sequence-extra` / `sequence-changed` | Sequence dropped, added, or its start / increment / min / max / cycle changed (the current value is data and is never read) |
| `enum-missing` / `enum-extra` / `enum-changed` | Enum type dropped, added, or its labels changed (label order counts) |

Column *order* is deliberately ignored: it changes harmlessly after a column is dropped.

## Intentional differences are not drift

Some differences are on purpose: feature-flag tables, environment-specific config tables, debug columns.
Flagging them creates noise that gets the whole feature ignored. List them once:

```text
# .dbguard-ignore
public.feature_flags
public.tenant_config
public.accounts.debug_*
```

Migration bookkeeping tables (`flyway_schema_history`, `databasechangelog`, `databasechangeloglock`) are
ignored automatically.

## Running it on a schedule

[`examples/drift-check.yml`](../examples/drift-check.yml) is a ready-to-copy GitHub Actions workflow: it builds the
expected schema from a scratch PostgreSQL, checks staging and production with read-only credentials, alerts
Slack, and uploads each run's snapshots as artifacts (the "when did this column's type actually change" history).
A daily cron flags a manual change within one run cycle.

## Security

- Read-only sessions and catalog-only queries, same as `dbguard check`.
- DSNs and the Slack webhook URL are read from environment variables, never printed, and redacted from errors.
- Snapshots contain schema structure (table and column names), which can be sensitive: treat the
  `snapshots/` artifacts like any other internal document.

## Limits (v0.1)

- PostgreSQL and MySQL (8.0+). On MySQL the compared objects are tables, columns (type, collation, nullability, default, auto_increment, generated), indexes, primary/unique/foreign-key/check constraints, triggers and views; sequences and enums do not exist there. For PostgreSQL the compared objects are: tables, columns, indexes, constraints, triggers, views, sequences, enums. Not compared yet: functions and procedures, other custom types (composite, domain), extensions, and permissions.
- View definitions are compared as PostgreSQL prints them, so comparing environments on *different major versions* can show formatting-only differences. Use the same major version for the scratch database as for production.
- The state file must be persisted between scheduled runs (the example uses `actions/cache`). Without `--state-file`, an unreconciled drift alerts on every run. An unreachable database is reported as an error (exit 2) and never counts as a resolution.
