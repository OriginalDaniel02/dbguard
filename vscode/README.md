# DB Guard for VS Code

Catch migrations that will lock production **while you write them**, not after the pager goes off.

DB Guard reads the Flyway or Liquibase migration you are editing, looks at how big the target tables really are,
and underlines the statements that will block writes: how long, and what to do instead.

```text
V3__add_index.sql
  3 | CREATE INDEX idx_transactions_amount ON transactions (amount);
      ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
      this will lock public.transactions (14.0M rows) for approximately 1-6 min
      SHARE lock: blocks writes to the table for the whole index build
      Safer: CREATE INDEX CONCURRENTLY (must run outside a transaction; ...)         DB Guard (create-index)
```

It is a thin wrapper around the [`dbguard`](https://github.com/OriginalDaniel02/dbguard) command line tool, so what you
see in the editor is exactly what your CI check will say. Nothing is sent anywhere, and database access (when you
configure it) is read-only.

## Requirements

The `dbguard` binary, **version 0.4.0 or newer**, on your `PATH` (or set `dbguard.path`). Download it from the
[releases page](https://github.com/OriginalDaniel02/dbguard/releases), then check:

```bash
dbguard version
```

## Features

- **Inline warnings** on open, on save, and (optionally) while you type, using the unsaved text.
- **Severity that matches your CI**: findings that would fail the check (`dbguard.failOn`) are errors; the rest are
  warnings or information. Findings you have acknowledged are hints.
- **Real sizes and lock-time estimates** when a read-only connection string is available (`dbguard.dsnEnv`), or sizes you
  list in `dbguard.tableRows`. Without either, tables are assumed large.
- **Quick fix: acknowledge a risk.** The lightbulb inserts an auditable `dbguard:ignore <rule> reason: ...` comment above
  the statement (SQL `--`, Java `//`, Liquibase XML `<!-- -->`, YAML `#`). The reason is required and stays in the file, in git
  history and in the PR comment. JSON changelogs have no comments: use the changeSet's `comment` field.
- **PostgreSQL and MySQL**; Flyway SQL, **Flyway Java migrations** and Liquibase (XML, YAML, JSON, formatted SQL).
- **Schema changelog**: *DB Guard: Show schema changelog for a table* answers "when did this column's type change?" from the
  snapshots your drift job saves.
- Statements DB Guard could not analyze are shown as information, so a skipped statement is never silent.

## Which files are checked

Flyway-named files (`V1__name.sql`, `R__views.sql`) anywhere, plus files matching `dbguard.include` (by default
`**/db/migration/**`, `**/migrations/**`, `**/changelog/**` and similar). Use **DB Guard: Check this migration** to check
any other file on demand.

## Settings

| Setting | Default | |
|---|---|---|
| `dbguard.path` | `dbguard` | Path to the binary (relative paths resolve against the workspace folder) |
| `dbguard.engine` | *(detect)* | `postgres` or `mysql`; empty follows the connection string, else PostgreSQL |
| `dbguard.dsnEnv` | `DBGUARD_DSN` | Env var holding a **read-only** connection string. VS Code must be started with it set |
| `dbguard.tableRows` | `{}` | Sizes to assume offline, e.g. `{ "transactions": 14000000 }` |
| `dbguard.largeRows` | `100000` | Tables smaller than this are low risk |
| `dbguard.failOn` | `medium-high` | Lowest risk shown as an error |
| `dbguard.mysqlVersion` | | MySQL version to assume when not connected, e.g. `8.0.28` |
| `dbguard.placeholders` | `{}` | `${placeholder}` values |
| `dbguard.checkOnOpen` / `checkOnSave` | `true` | |
| `dbguard.checkOnType` | `false` | Check while typing (debounced) |
| `dbguard.include` | see above | Globs of migration files to check automatically |
| `dbguard.snapshotsDir` | `snapshots` | Snapshots for the schema changelog command |

## Commands

- **DB Guard: Check this migration**
- **DB Guard: Acknowledge this risk** (also the quick fix)
- **DB Guard: Show schema changelog for a table**
- **DB Guard: Show output**

## Troubleshooting

- *"could not run dbguard"*: install the binary or set `dbguard.path`.
- *"needs dbguard 0.4.0 or newer"*: the extension reads dbguard's JSON output (a stable contract since 0.3.0) and needs 0.4.0 for Java migrations.
- *Nothing is flagged*: the table may be small (see `dbguard.largeRows`), the statement may be safe, or the file may not
  match `dbguard.include`. Run **Check this migration** and look at **Show output**.
- *Sizes are always "unknown"*: VS Code does not see your connection string. Start VS Code from a shell where the variable is
  set, or use `dbguard.tableRows`.

## License

Apache-2.0, same as DB Guard.
