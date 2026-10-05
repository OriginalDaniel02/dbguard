package pg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// schemaFilter excludes system schemas, which are never part of a snapshot.
const schemaFilter = `n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname !~ '^pg_(toast|temp)'`

// Snapshot reads the current schema (tables, columns, indexes, constraints)
// using catalog queries only, inside a READ ONLY transaction. No table data is read.
func (s *Stats) Snapshot(ctx context.Context) (*snapshot.Schema, error) {
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	out := &snapshot.Schema{Engine: "postgres", ServerMajor: s.Version}
	type key struct{ schema, name string }
	cols := map[key][]snapshot.Column{}
	idxs := map[key][]snapshot.Index{}
	cons := map[key][]snapshot.Constraint{}
	var order []key
	seen := map[key]bool{}
	touch := func(k key) {
		if !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT n.nspname, c.relname, a.attname, format_type(a.atttypid, a.atttypmod),
		       a.attnotnull, COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
		       a.attidentity::text, a.attgenerated::text
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped
		  AND `+schemaFilter+`
		ORDER BY n.nspname, c.relname, a.attnum`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		var c snapshot.Column
		if err := rows.Scan(&k.schema, &k.name, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.Identity, &c.Generated); err != nil {
			rows.Close()
			return nil, err
		}
		touch(k)
		cols[k] = append(cols[k], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `
		SELECT n.nspname, t.relname, i.relname, pg_get_indexdef(i.oid), x.indisunique, x.indisprimary
		FROM pg_index x
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_class t ON t.oid = x.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relkind IN ('r', 'p') AND `+schemaFilter)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		var ix snapshot.Index
		if err := rows.Scan(&k.schema, &k.name, &ix.Name, &ix.Def, &ix.Unique, &ix.Primary); err != nil {
			rows.Close()
			return nil, err
		}
		idxs[k] = append(idxs[k], ix)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `
		SELECT n.nspname, t.relname, con.conname, con.contype::text,
		       pg_get_constraintdef(con.oid), con.convalidated
		FROM pg_constraint con
		JOIN pg_class t ON t.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relkind IN ('r', 'p') AND con.contype IN ('p', 'u', 'f', 'c', 'x')
		  AND `+schemaFilter)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		var c snapshot.Constraint
		if err := rows.Scan(&k.schema, &k.name, &c.Name, &c.Type, &c.Def, &c.Validated); err != nil {
			rows.Close()
			return nil, err
		}
		cons[k] = append(cons[k], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out.Tables = nil
	for _, k := range order {
		out.Tables = append(out.Tables, snapshot.Table{
			Schema: k.schema, Name: k.name,
			Columns: cols[k], Indexes: idxs[k], Constraints: cons[k],
		})
	}
	out.Normalize()
	return out, nil
}
