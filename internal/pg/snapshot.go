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
	trigs := map[key][]snapshot.Trigger{}
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

	// Triggers (user-defined only; constraint triggers backing foreign keys are internal).
	rows, err = tx.Query(ctx, `
		SELECT n.nspname, c.relname, tg.tgname, pg_get_triggerdef(tg.oid)
		FROM pg_trigger tg
		JOIN pg_class c ON c.oid = tg.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT tg.tgisinternal AND c.relkind IN ('r', 'p') AND `+schemaFilter)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		var tr snapshot.Trigger
		if err := rows.Scan(&k.schema, &k.name, &tr.Name, &tr.Def); err != nil {
			rows.Close()
			return nil, err
		}
		trigs[k] = append(trigs[k], tr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Views and materialized views.
	rows, err = tx.Query(ctx, `
		SELECT n.nspname, c.relname, pg_get_viewdef(c.oid, true), c.relkind = 'm'
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('v', 'm') AND `+schemaFilter)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v snapshot.View
		if err := rows.Scan(&v.Schema, &v.Name, &v.Def, &v.Materialized); err != nil {
			rows.Close()
			return nil, err
		}
		out.Views = append(out.Views, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sequences (definition only; the current value is data and is not read).
	rows, err = tx.Query(ctx, `
		SELECT schemaname, sequencename, data_type::text, start_value, min_value, max_value, increment_by, cycle
		FROM pg_sequences s
		JOIN pg_namespace n ON n.nspname = s.schemaname
		WHERE `+schemaFilter)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q snapshot.Sequence
		if err := rows.Scan(&q.Schema, &q.Name, &q.Type, &q.Start, &q.Min, &q.Max, &q.Increment, &q.Cycle); err != nil {
			rows.Close()
			return nil, err
		}
		out.Sequences = append(out.Sequences, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Enum types, labels in their defined order.
	rows, err = tx.Query(ctx, `
		SELECT n.nspname, t.typname, e.enumlabel
		FROM pg_type t
		JOIN pg_enum e ON e.enumtypid = t.oid
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE `+schemaFilter+`
		ORDER BY n.nspname, t.typname, e.enumsortorder`)
	if err != nil {
		return nil, err
	}
	enums := map[string]*snapshot.Enum{}
	for rows.Next() {
		var schema, name, label string
		if err := rows.Scan(&schema, &name, &label); err != nil {
			rows.Close()
			return nil, err
		}
		k := schema + "." + name
		if enums[k] == nil {
			enums[k] = &snapshot.Enum{Schema: schema, Name: name}
		}
		enums[k].Labels = append(enums[k].Labels, label)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, e := range enums {
		out.Enums = append(out.Enums, *e)
	}

	out.Tables = nil
	for _, k := range order {
		out.Tables = append(out.Tables, snapshot.Table{
			Schema: k.schema, Name: k.name,
			Columns: cols[k], Indexes: idxs[k], Constraints: cons[k], Triggers: trigs[k],
		})
	}
	out.Normalize()
	return out, nil
}
