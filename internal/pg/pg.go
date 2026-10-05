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
	Version int // major version, e.g. 16
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
	return s, nil
}

func (s *Stats) Close(ctx context.Context) { _ = s.conn.Close(ctx) }

// Rows returns the planner's live-row estimate (n_live_tup, falling back to
// reltuples) without scanning the table.
func (s *Stats) Rows(schema, table string) (int64, bool) {
	key := schema + "." + table
	if n, ok := s.cache[key]; ok {
		return n, true
	}
	var n int64
	err := s.query(context.Background(), `
		SELECT GREATEST(COALESCE(st.n_live_tup, 0), c.reltuples::bigint, 0)
		FROM pg_class c
		JOIN pg_namespace ns ON ns.oid = c.relnamespace
		LEFT JOIN pg_stat_user_tables st ON st.relid = c.oid
		WHERE ns.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p')`,
		[]any{schema, table}, &n)
	if err != nil {
		return 0, false
	}
	s.cache[key] = n
	return n, true
}

func (s *Stats) query(ctx context.Context, sql string, args []any, dest any) error {
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return tx.QueryRow(ctx, sql, args...).Scan(dest)
}

func redact(msg, dsn, password string) string {
	for _, secret := range []string{dsn, password} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	return msg
}
