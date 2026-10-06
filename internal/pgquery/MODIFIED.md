# Local modification

This directory is a copy of [github.com/wasilibs/go-pgquery](https://github.com/wasilibs/go-pgquery)
(MIT License, see LICENSE and NOTICE.txt) at version `v0.0.0-20260915022521-81f99195012b`.

The only functional change is in `parser/parser_wazero.go`: the WebAssembly runtime uses wazero's
**interpreter** by default instead of its optimizing compiler. The compiler spends about 20 seconds
compiling libpg_query on every process start because the library has no compilation cache, while
the interpreter starts in well under a second and is fast enough for migration files.
`DBGUARD_WASM_COMPILER=1` restores the compiler. The C sources (`internal/cparser`) and the cgo
build (`parser_cgo.go`) were removed because DB Guard builds without cgo.
