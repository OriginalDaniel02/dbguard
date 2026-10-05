// Package pg reads table statistics from PostgreSQL. It is strictly read-only:
// the session is forced to default_transaction_read_only=on and every query
// runs in a READ ONLY transaction. Connection strings are never logged or
// included in returned errors.
package pg

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Stats implements rules.Stats against a live (or snapshot) database.
type Stats struct {
	conn    *pgx.Conn
	cache   map[string]int64
	Version int    // major version, e.g. 16
	Schema  string // current_schema() of the connection role (first search_path entry)
}

// Connect opens a read-only session. dsn is only ever used here.
func Connect(ctx context.Context, dsn string) (*Stats, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid connection string") // never echo dsn
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("could not connect to database: " + redact(err.Error(), dsn, cfg.Password))
	}
	s := &Stats{conn: conn, cache: map[string]int64{}}
	var v int
	if err := s.query(ctx, "SELECT current_setting('server_version_num')::int", nil, &v); err == nil {
		s.Version = v / 10000
	}
	_ = s.query(ctx, "SELECT current_schema()", nil, &s.Schema)
	return s, nil
}

func (s *Stats) Close(ctx context.Context) { _ = s.conn.Close(ctx) }

// Rows returns the planner's live-row estimate without scanning the table.
// ok is false when the table is unknown OR has no usable statistics (never
// analyzed: reltuples is -1 on PG 14+, or 0 while the table already occupies
// pages). Treating that as "0 rows" would silently downgrade a huge table.
func (s *Stats) Rows(schema, table string) (int64, bool) {
	key := schema + "." + table
	if n, ok := s.cache[key]; ok {
		return n, true
	}
	var live, reltuples, bytes int64
	err := s.query(context.Background(), `
		SELECT COALESCE(st.n_live_tup, 0), c.reltuples::bigint, pg_relation_size(c.oid)
		FROM pg_class c
		JOIN pg_namespace ns ON ns.oid = c.relnamespace
		LEFT JOIN pg_stat_user_tables st ON st.relid = c.oid
		WHERE ns.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','m')`,
		[]any{schema, table}, &live, &reltuples, &bytes)
	if err != nil {
		return 0, false
	}
	n := max(live, reltuples)
	if n <= 0 && bytes > 0 {
		return 0, false // has data on disk but no statistics: size unknown
	}
	n = max(n, 0)
	s.cache[key] = n
	return n, true
}

// HasNotNullCheck reports whether a validated CHECK (col IS NOT NULL) exists,
// which lets PG 12+ SET NOT NULL without a full table scan.
func (s *Stats) HasNotNullCheck(schema, table, column string) bool {
	var ok bool
	err := s.query(context.Background(), `
		SELECT EXISTS (
		  SELECT 1 FROM pg_constraint con
		  JOIN pg_class c ON c.oid = con.conrelid
		  JOIN pg_namespace ns ON ns.oid = c.relnamespace
		  WHERE ns.nspname = $1 AND c.relname = $2
		    AND con.contype = 'c' AND con.convalidated
		    AND pg_get_constraintdef(con.oid) = 'CHECK ((' || quote_ident($3) || ' IS NOT NULL))')`,
		[]any{schema, table, column}, &ok)
	return err == nil && ok
}

func (s *Stats) query(ctx context.Context, sql string, args []any, dest ...any) error {
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return tx.QueryRow(ctx, sql, args...).Scan(dest...)
}

func redact(msg, dsn, password string) string {
	for _, secret := range []string{dsn, password} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	return msg
}
