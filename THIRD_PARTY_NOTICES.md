# Third-party notices

DB Guard is licensed under the [Apache License 2.0](LICENSE). The `dbguard` binary statically links the open source
components below, each under its own license. Their full license texts ship with their source (in your Go module cache, or
in the linked repositories); the table gives the license of each version used.

## Code embedded in this repository

| Component | License | Notes |
|---|---|---|
| [`internal/pgquery`](internal/pgquery) | MIT, with the BSD-3-Clause notice of pganalyze/libpg_query | A modified copy of [wasilibs/go-pgquery](https://github.com/wasilibs/go-pgquery) (WebAssembly build of PostgreSQL's parser). The only functional change is described in [`internal/pgquery/MODIFIED.md`](internal/pgquery/MODIFIED.md). Its `LICENSE` and `NOTICE.txt` are kept alongside it. The embedded `libpg_query.so` is the WebAssembly build of [libpg_query](https://github.com/pganalyze/libpg_query) (BSD-3-Clause), which includes code derived from PostgreSQL (PostgreSQL License). |

## Go dependencies linked into the binary

| Module | License |
|---|---|
| github.com/pganalyze/pg_query_go/v6 | BSD-3-Clause |
| github.com/jackc/pgx/v5, pgpassfile, pgservicefile | MIT |
| github.com/go-sql-driver/mysql | **MPL-2.0** |
| github.com/pingcap/tidb/pkg/parser (the MySQL parser) | Apache-2.0 |
| github.com/pingcap/errors | BSD-2-Clause |
| github.com/pingcap/failpoint, pingcap/log | Apache-2.0 |
| github.com/tetratelabs/wazero | Apache-2.0 |
| github.com/wasilibs/wazero-helpers | MIT |
| gopkg.in/yaml.v3 | Apache-2.0 (and MIT) |
| github.com/coreos/go-semver | Apache-2.0 |
| go.uber.org/zap, atomic, multierr | MIT |
| gopkg.in/natefinch/lumberjack.v2 | MIT |
| google.golang.org/protobuf | BSD-3-Clause |
| golang.org/x/sys, golang.org/x/text, filippo.io/edwards25519 | BSD-3-Clause |

**MPL-2.0 (`go-sql-driver/mysql`).** It is a file-level copyleft license: it applies to the files of that library, not to DB Guard.
DB Guard uses it unmodified. Its source is available at https://github.com/go-sql-driver/mysql.

## VS Code extension

The extension in [`vscode`](vscode) has **no runtime dependencies**: the packaged `.vsix` contains only DB Guard's own compiled
code. Its development dependencies (TypeScript, mocha, `@vscode/test-electron`, `@vscode/vsce`) are used to build and test it
and are not distributed.

## Regenerating this list

```bash
go list -deps -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' ./cmd/dbguard | sort -u
```
