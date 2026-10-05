# DB Guard

Pre-flight checker for database migrations (and, later, a schema drift detector).
One CLI engine (`dbguard`); CI is the enforcement point, everything else wraps it.

## Phase 1 scope (v1 ticket)
- Go CLI core, PostgreSQL only
- Full migration risk rules table (see docs/risk-rules.md)
- Flyway migration format support
- GitHub Action that posts a PR comment (table, estimated duration, safer alternative)
- Read-only DB access; auditable override for false positives

## Later
- Phase 2: Liquibase, MySQL, drift detector (scheduled job + Slack), GitLab CI
- Phase 3: VS Code extension, per-table schema changelog

## Layout
- cmd/dbguard        CLI entrypoint (`dbguard check`)
- internal/flyway    Flyway migration parsing
- internal/rules     Risk rules engine
- internal/pg        Read-only Postgres stats (pg_stat_user_tables, row counts)
- internal/estimate  Lock duration range estimates
- internal/report    PR comment / output formatting
- internal/override  Explicit, auditable override handling
- action/            GitHub Action wrapper
- testdata/          Sample migrations for each rule

## Usage
```
# live stats from a read-only connection (never logged or echoed)
export DBGUARD_DSN='postgres://readonly:...@host/db'
dbguard check db/migration                       # text; exit 1 on blocking findings
dbguard check --format markdown V3__idx.sql      # PR-comment body
dbguard check --rows transactions=14000000 V3__idx.sql   # offline, no DB
```
Flags: `--fail-on` (default `medium-high`), `--large-rows` (default 100000), `--pg-version`, `--format text|markdown|json`.

### Accepting a risk (auditable, in the migration file)
```sql
-- dbguard:ignore create-index reason: table is write-quiet during the 3am deploy window
CREATE INDEX idx ON transactions (amount);
```
A reason is mandatory. Rule ids: add-column-nonconstant-default, alter-column-type, create-index,
add-unique-or-pk, add-not-null, add-foreign-key, drop-column.

### GitHub Action
See `.github/workflows/dbguard.yml` and `action/action.yml`. Store the read-only DSN as a secret.

## Rule notes
- Non-constant ADD COLUMN defaults only rewrite the table when *volatile* (random(), clock_timestamp(),
  nextval/serial, identity, stored generated). `now()`/CURRENT_TIMESTAMP are metadata-only on PG 11+
  (verified against PG 16), so they are NOT flagged. Unknown functions are assumed volatile.
- Duration constants (internal/estimate) are rough defaults; calibrate against your hardware.

## Tests
`go test ./...`. Integration tests need a database:
`DBGUARD_TEST_DSN=postgres://postgres:secret@localhost:55432/postgres?sslmode=disable go test ./internal/pg`

## License
Apache License 2.0 - see [LICENSE](LICENSE).
