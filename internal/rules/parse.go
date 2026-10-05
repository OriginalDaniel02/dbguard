package rules

import (
	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
)

// pgParse uses the WASM build of libpg_query, so no cgo/C toolchain is needed.
func pgParse(sql string) (*pg_query.ParseResult, error) {
	return pgquery.Parse(sql)
}

// Parses reports whether sql is valid PostgreSQL syntax (Flyway-style ${placeholders}
// are tolerated). Used to isolate statements that cannot be analyzed.
func Parses(sql string) error {
	sql, _ = substitutePlaceholders(sql, nil)
	_, err := pgParse(sql)
	return err
}
