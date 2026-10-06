// Package mysqldb reads table statistics and schema snapshots from MySQL. It is
// read-only by construction: the session is set READ ONLY and every query runs in a
// READ ONLY transaction. Connection strings are never logged or echoed in errors.
package mysqldb

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/OriginalDaniel02/dbguard/internal/rules"
)

// Stats implements rules.Stats and rules.ColumnTyper against MySQL.
type Stats struct {
	db      *sql.DB
	conn    *sql.Conn // the single session every query uses
	Version int       // major*10000 + minor*100 + patch, e.g. 80036
	Flavor  string    // "MySQL" or "MariaDB"
	Schema  string    // DATABASE() of the connection
	cache   map[string]int64
}

// IsMySQLDSN reports whether a connection string is for MySQL: a mysql:// URL, or
// the go-sql-driver form user:pass@tcp(host:3306)/db.
func IsMySQLDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "mysql://") || strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(")
}

// toDriverDSN converts mysql://user:pass@host:3306/db?x=y into the driver's format.
func toDriverDSN(dsn string) (string, error) {
	if !strings.HasPrefix(dsn, "mysql://") {
		return dsn, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("invalid connection string")
	}
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":3306"
	}
	cfg.Addr = host
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	cfg.Params = map[string]string{}
	for k, v := range u.Query() {
		cfg.Params[k] = v[0]
	}
	return cfg.FormatDSN(), nil
}

// Connect opens a read-only session.
func Connect(ctx context.Context, dsn string) (*Stats, error) {
	d, err := toDriverDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg, err := mysql.ParseDSN(d)
	if err != nil {
		return nil, errors.New("invalid connection string")
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	// Applied to every connection in the pool: new transactions default to READ ONLY.
	cfg.Params["transaction_read_only"] = "1"
	password := cfg.Passwd
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, errors.New("invalid connection string")
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, errors.New("could not connect to database: " + redact(err.Error(), dsn, d, password))
	}
	// information_schema caches table statistics for 24h by default, so a table that was
	// just bulk-loaded can report 0 rows. Reading fresh estimates is a session setting, not
	// a write (and does not run ANALYZE). MySQL 5.7 has no such variable: ignore the error.
	_, _ = conn.ExecContext(ctx, "SET SESSION information_schema_stats_expiry = 0")
	s := &Stats{db: db, conn: conn, cache: map[string]int64{}, Flavor: "MySQL"}
	var version string
	if err := s.queryRow(ctx, "SELECT VERSION()", nil, &version); err == nil {
		s.Version, s.Flavor = ParseVersion(version)
	}
	var schema sql.NullString
	if err := s.queryRow(ctx, "SELECT DATABASE()", nil, &schema); err == nil {
		s.Schema = schema.String
	}
	return s, nil
}

func (s *Stats) Close() {
	_ = s.conn.Close()
	_ = s.db.Close()
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// ParseVersion turns "8.0.36-0ubuntu0.22.04.1" or "10.11.6-MariaDB" into 80036 / 101106.
func ParseVersion(v string) (int, string) {
	flavor := "MySQL"
	if strings.Contains(strings.ToLower(v), "mariadb") {
		flavor = "MariaDB"
	}
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return 0, flavor
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	return a*10000 + b*100 + c, flavor
}

// Rows returns InnoDB's row estimate from information_schema without scanning the table.
// ok is false when the table is unknown OR looks populated but has no statistics (an
// empty InnoDB table already occupies one 16 KiB page, so a larger table with
// TABLE_ROWS = 0 has simply never been analyzed).
func (s *Stats) Rows(schema, table string) (int64, bool) {
	if schema == "" {
		schema = s.Schema
	}
	key := schema + "." + table
	if n, ok := s.cache[key]; ok {
		return n, true
	}
	var rows, dataLen sql.NullInt64
	err := s.queryRow(context.Background(),
		"SELECT TABLE_ROWS, DATA_LENGTH FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND TABLE_TYPE = 'BASE TABLE'",
		[]any{schema, table}, &rows, &dataLen)
	if err != nil || !rows.Valid {
		return 0, false
	}
	n := rows.Int64
	if n == 0 && dataLen.Int64 > 16384 {
		return 0, false
	}
	s.cache[key] = n
	return n, true
}

// Column implements rules.ColumnTyper.
func (s *Stats) Column(schema, table, column string) (rules.ColumnInfo, bool) {
	if schema == "" {
		schema = s.Schema
	}
	var typ, nullable string
	var charset sql.NullString
	err := s.queryRow(context.Background(),
		"SELECT COLUMN_TYPE, IS_NULLABLE, CHARACTER_SET_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		[]any{schema, table, column}, &typ, &nullable, &charset)
	if err != nil {
		return rules.ColumnInfo{}, false
	}
	return rules.ColumnInfo{Type: typ, NotNull: nullable == "NO", Charset: charset.String}, true
}

func (s *Stats) queryRow(ctx context.Context, q string, args []any, dest ...any) error {
	tx, err := s.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	return tx.QueryRowContext(ctx, q, args...).Scan(dest...)
}

func redact(msg string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return msg
}
