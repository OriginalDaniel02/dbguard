package mysqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// Snapshot reads the schema of the connection's database (tables, columns, indexes,
// constraints, triggers, views) from information_schema, inside a READ ONLY
// transaction. Table data is never read. In MySQL a "schema" is a database, and
// the same application usually lives in differently named databases per environment,
// so tables are recorded without a schema qualifier (names look like "orders").
func (s *Stats) Snapshot(ctx context.Context) (*snapshot.Schema, error) {
	if s.Schema == "" {
		return nil, errors.New("the connection string must name a database (mysql://user:pass@host:3306/dbname)")
	}
	tx, err := s.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	out := &snapshot.Schema{Engine: "mysql", ServerMajor: s.Version / 10000}
	db := s.Schema
	tables := map[string]*snapshot.Table{}
	var order []string

	// Tables.
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'", db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		tables[n] = &snapshot.Table{Name: n}
		order = append(order, n)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	// Columns.
	rows, err = tx.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA,
		       GENERATION_EXPRESSION, COLLATION_NAME
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`, db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, name, typ, nullable, extra string
		var def, gen, coll sql.NullString
		if err := rows.Scan(&table, &name, &typ, &nullable, &def, &extra, &gen, &coll); err != nil {
			rows.Close()
			return nil, err
		}
		t := tables[table]
		if t == nil {
			continue // a view's column
		}
		c := snapshot.Column{Name: name, Type: typ, NotNull: nullable == "NO"}
		if coll.Valid && coll.String != "" {
			c.Type += " " + coll.String // a collation change is drift
		}
		low := strings.ToLower(extra)
		switch {
		case strings.Contains(low, "stored generated"):
			c.Generated, c.Default = "s", gen.String
		case strings.Contains(low, "virtual generated"):
			c.Generated, c.Default = "v", gen.String
		case def.Valid:
			c.Default = def.String
			if strings.Contains(low, "default_generated") {
				c.Default = "(" + def.String + ")" // an expression default
			}
		}
		if strings.Contains(low, "auto_increment") {
			c.Identity = "auto_increment"
		}
		t.Columns = append(t.Columns, c)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	// Indexes, one row per column; assembled in index order.
	type idxKey struct{ table, index string }
	type idxParts struct {
		parts   []string
		unique  bool
		typ     string
		primary bool
	}
	idx := map[idxKey]*idxParts{}
	var idxOrder []idxKey
	rows, err = tx.QueryContext(ctx, `
		SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SUB_PART, COLLATION, INDEX_TYPE
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, index, typ string
		var nonUnique int
		var col, collation sql.NullString
		var sub sql.NullInt64
		if err := rows.Scan(&table, &index, &nonUnique, &col, &sub, &collation, &typ); err != nil {
			rows.Close()
			return nil, err
		}
		k := idxKey{table, index}
		p := idx[k]
		if p == nil {
			p = &idxParts{unique: nonUnique == 0, typ: typ, primary: index == "PRIMARY"}
			idx[k] = p
			idxOrder = append(idxOrder, k)
		}
		part := col.String
		if !col.Valid {
			part = "(expression)"
		}
		if sub.Valid {
			part += fmt.Sprintf("(%d)", sub.Int64)
		}
		if collation.Valid && collation.String == "D" {
			part += " DESC"
		}
		p.parts = append(p.parts, part)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	for _, k := range idxOrder {
		t := tables[k.table]
		if t == nil {
			continue
		}
		p := idx[k]
		def := p.typ + " (" + strings.Join(p.parts, ", ") + ")"
		if p.unique {
			def = "UNIQUE " + def
		}
		t.Indexes = append(t.Indexes, snapshot.Index{Name: k.index, Def: def, Unique: p.unique, Primary: p.primary})
	}

	// Constraints: PRIMARY KEY, UNIQUE, FOREIGN KEY (with actions), CHECK.
	enforced := map[idxKey]bool{}
	ctype := map[idxKey]string{}
	rows, err = tx.QueryContext(ctx, `
		SELECT TABLE_NAME, CONSTRAINT_NAME, CONSTRAINT_TYPE, ENFORCED
		FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = ?`, db)
	if err != nil {
		return nil, err
	}
	var conOrder []idxKey
	for rows.Next() {
		var table, name, typ string
		var enf sql.NullString
		if err := rows.Scan(&table, &name, &typ, &enf); err != nil {
			rows.Close()
			return nil, err
		}
		k := idxKey{table, name}
		ctype[k] = typ
		enforced[k] = !enf.Valid || enf.String != "NO"
		conOrder = append(conOrder, k)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	cols := map[idxKey][]string{}
	refs := map[idxKey][]string{}
	refTable := map[idxKey]string{}
	rows, err = tx.QueryContext(ctx, `
		SELECT TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`, db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, name, col string
		var rt, rc sql.NullString
		if err := rows.Scan(&table, &name, &col, &rt, &rc); err != nil {
			rows.Close()
			return nil, err
		}
		k := idxKey{table, name}
		cols[k] = append(cols[k], col)
		if rt.Valid {
			refTable[k] = rt.String
			refs[k] = append(refs[k], rc.String)
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	actions := map[idxKey]string{}
	rows, err = tx.QueryContext(ctx, `
		SELECT TABLE_NAME, CONSTRAINT_NAME, UPDATE_RULE, DELETE_RULE
		FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = ?`, db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, name, upd, del string
		if err := rows.Scan(&table, &name, &upd, &del); err != nil {
			rows.Close()
			return nil, err
		}
		actions[idxKey{table, name}] = " ON DELETE " + del + " ON UPDATE " + upd
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	checks := map[string]string{} // constraint name -> clause (names are unique per schema)
	if rows, err = tx.QueryContext(ctx, "SELECT CONSTRAINT_NAME, CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = ?", db); err == nil {
		for rows.Next() {
			var name, clause string
			if err := rows.Scan(&name, &clause); err != nil {
				rows.Close()
				return nil, err
			}
			checks[name] = clause
		}
		if err := closeRows(rows); err != nil {
			return nil, err
		}
	} // older servers (5.7) have no CHECK_CONSTRAINTS: skip silently

	for _, k := range conOrder {
		t := tables[k.table]
		if t == nil {
			continue
		}
		con := snapshot.Constraint{Name: k.index, Validated: enforced[k]}
		switch ctype[k] {
		case "PRIMARY KEY":
			con.Type, con.Def = "p", "PRIMARY KEY ("+strings.Join(cols[k], ", ")+")"
		case "UNIQUE":
			con.Type, con.Def = "u", "UNIQUE ("+strings.Join(cols[k], ", ")+")"
		case "FOREIGN KEY":
			con.Type = "f"
			con.Def = "FOREIGN KEY (" + strings.Join(cols[k], ", ") + ") REFERENCES " + refTable[k] + " (" + strings.Join(refs[k], ", ") + ")" + actions[k]
		case "CHECK":
			con.Type, con.Def = "c", "CHECK "+checks[k.index]
		default:
			continue
		}
		t.Constraints = append(t.Constraints, con)
	}

	// Triggers.
	rows, err = tx.QueryContext(ctx, `
		SELECT EVENT_OBJECT_TABLE, TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, ACTION_STATEMENT
		FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?`, db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, name, timing, event, stmt string
		if err := rows.Scan(&table, &name, &timing, &event, &stmt); err != nil {
			rows.Close()
			return nil, err
		}
		if t := tables[table]; t != nil {
			t.Triggers = append(t.Triggers, snapshot.Trigger{Name: name, Def: timing + " " + event + " FOR EACH ROW " + stmt})
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	// Views.
	rows, err = tx.QueryContext(ctx, "SELECT TABLE_NAME, VIEW_DEFINITION FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ?", db)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		var def sql.NullString
		if err := rows.Scan(&name, &def); err != nil {
			rows.Close()
			return nil, err
		}
		out.Views = append(out.Views, snapshot.View{Name: name, Def: def.String})
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	sort.Strings(order)
	for _, n := range order {
		out.Tables = append(out.Tables, *tables[n])
	}
	out.Normalize()
	return out, nil
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	rows.Close()
	return err
}
