# JSON output (stable contract)

`dbguard check --format json`, `dbguard drift --format json` and `dbguard changelog --format json` print JSON meant
for other tools: the VS Code extension reads it, and so can your own scripts. The shapes below are stable within a
minor version series; new fields may be added, existing ones keep their meaning. Check `dbguard version` if you depend on it.

## `dbguard check --format json`

An array with one object per migration file checked:

```json
[
  {
    "path": "db/migration/V3__add_index.sql",
    "findings": [
      {
        "rule": "create-index",
        "risk": "high",
        "table": "public.transactions",
        "rows": 14000000,
        "line": 4,
        "statement": "CREATE INDEX idx ON transactions (amount);",
        "lock": "SHARE lock: blocks writes to the table for the whole index build",
        "message": "this will lock public.transactions (14.0M rows) for approximately 1-6 min",
        "alternative": "CREATE INDEX CONCURRENTLY (must run outside a transaction; in Flyway set executeInTransaction=false)",
        "estimate": { "min_seconds": 35, "max_seconds": 350, "text": "1-6 min" },
        "blocking": true
      }
    ],
    "problems": ["line 9: could not parse this statement, so it was NOT analyzed (...)"]
  }
]
```

| Field | Meaning |
|---|---|
| `path` | The file as given to `check` |
| `findings[].rule` | Rule id; this is what `dbguard:ignore <rule>` takes |
| `findings[].risk` | `safe`, `low`, `medium`, `medium-high` or `high` |
| `findings[].table` | Schema-qualified table (PostgreSQL) or table name (MySQL) |
| `findings[].rows` | Estimated row count, `-1` when unknown (then the table is assumed large) |
| `findings[].line` | 1-based line of the statement; for Liquibase XML/YAML/JSON, the `changeSet` |
| `findings[].statement` | The statement text (for Liquibase structured changelogs, the generated SQL) |
| `findings[].lock` | What the operation does to the table |
| `findings[].message` | One-line explanation, with the size and the estimated duration when known |
| `findings[].alternative` | The safer way to do it |
| `findings[].estimate` | Present only when a duration estimate applies. A range, not a promise |
| `findings[].override` | Present when the finding was acknowledged with `dbguard:ignore`; holds the reason |
| `findings[].blocking` | Whether this finding fails the check at the current `--fail-on` (acknowledged findings never block) |
| `problems` | Non-fatal notes: statements or change types that were **not analyzed**, unused or reason-less ignores |

`findings` and `problems` are always arrays, never `null`. Exit codes: `0` nothing blocks, `1` something blocks, `2` error.

## `dbguard drift --format json`

```json
[{"env": "production", "differences": [{"kind": "column-extra", "table": "public.orders", "object": "discount", "actual": "numeric"}]}]
```

`kind` is one of the drift kinds listed in [drift.md](drift.md); `expected` and `actual` appear when relevant; `error`
is set for an environment that could not be read.

## `dbguard changelog --format json`

```json
[{"time": "2026-10-03T06:00:00Z", "prev_time": "2026-10-02T06:00:00Z", "env": "production", "table": "public.orders",
  "object": "status", "kind": "column-type-changed", "action": "changed",
  "change": "column status type changed: text -> varchar(20)", "before": "text", "after": "varchar(20)"}]
```

Newest first. `time` is when the snapshot that revealed the change was taken; the change itself happened somewhere
between `prev_time` and `time`. `action` is `added`, `dropped` or `changed`.
