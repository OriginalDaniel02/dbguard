# Contributing

Thanks for helping. DB Guard's value depends on **accuracy**: a rule that raises false alarms
gets the whole tool ignored, and one that misses a real lock gives false confidence.

## Setup

```bash
git clone https://github.com/OriginalDaniel02/dbguard.git && cd dbguard
go test ./...          # unit tests; needs only Go (no C compiler)
```

Integration tests need PostgreSQL and are skipped unless `DBGUARD_TEST_DSN` is set:

```bash
docker run -d --name dbguard-pg -e POSTGRES_PASSWORD=secret -p 55432:5432 postgres:16
DBGUARD_TEST_DSN='postgres://postgres:secret@localhost:55432/postgres?sslmode=disable' go test ./...
```

## Before opening a PR

- `gofmt -l .` prints nothing, `go vet ./...` and `go test ./...` pass (CI enforces this).
- **New or changed rules** need a table-driven test in `internal/rules/rules_test.go` covering the
  risky case *and* the nearby safe case, plus evidence of the real PostgreSQL lock behavior
  (an `EXPLAIN`, `pg_locks` observation, or a `relfilenode` change for rewrites).
- Duration constants live in `internal/estimate`; if you change them, say how you measured.
- DB access must stay read-only. Anything that could log or echo a connection string will be rejected.

## Reporting false positives / negatives

Open an issue with the migration SQL, the PostgreSQL version, and what actually happened.
