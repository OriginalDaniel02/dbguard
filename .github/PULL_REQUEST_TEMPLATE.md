## What and why

<!-- What does this change, and what problem does it solve? Link the issue if there is one. -->

## Checklist

- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `go test ./...` pass
- [ ] **New or changed rule:** a test covers the risky case *and* the nearby safe case, and the PR says how the behavior was verified on a real database (an `EXPLAIN`, `pg_locks`, the MySQL `ALGORITHM` the server accepts, a `relfilenode` change)
- [ ] Database access stays read-only, and nothing can log or echo a connection string
- [ ] Docs are updated if behavior or flags changed
