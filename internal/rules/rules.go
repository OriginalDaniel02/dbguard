// Package rules implements the PostgreSQL migration risk rules (docs/risk-rules.md).
package rules

import (
	"fmt"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/OriginalDaniel02/dbguard/internal/estimate"
)

// Risk is ordered: a higher value is more dangerous.
type Risk int

const (
	Safe Risk = iota
	Low
	Medium
	MediumHigh
	High
)

func (r Risk) String() string {
	switch r {
	case Safe:
		return "safe"
	case Low:
		return "low"
	case Medium:
		return "medium"
	case MediumHigh:
		return "medium-high"
	case High:
		return "high"
	}
	return "unknown"
}

// ParseRisk converts a flag value to a Risk.
func ParseRisk(s string) (Risk, error) {
	for r := Safe; r <= High; r++ {
		if r.String() == strings.ToLower(s) {
			return r, nil
		}
	}
	return Safe, fmt.Errorf("unknown risk level %q (want safe|low|medium|medium-high|high)", s)
}

// Rule IDs, used in findings and in `-- dbguard:ignore <id>` overrides.
const (
	AddColumnSafe     = "add-column-safe"
	AddColumnVolatile = "add-column-nonconstant-default"
	AlterColumnType   = "alter-column-type"
	CreateIndex       = "create-index"
	AddUniqueOrPK     = "add-unique-or-pk"
	AddNotNull        = "add-not-null"
	AddForeignKey     = "add-foreign-key"
	DropColumn        = "drop-column"
)

// Finding is one rule hit on one statement.
type Finding struct {
	Rule        string
	Risk        Risk
	Table       string
	Rows        int64 // -1 when unknown
	Line        int
	Statement   string
	Lock        string
	Message     string
	Alternative string
	Estimate    *estimate.Range // nil when no estimate applies
	Override    string          // non-empty reason when acknowledged
}

// Stats supplies table sizes. Implementations must be read-only.
type Stats interface {
	// Rows returns the estimated row count; ok is false when the table is unknown.
	Rows(schema, table string) (rows int64, ok bool)
}

// NotNullChecker is an optional Stats capability: it reports whether the table
// already has a validated CHECK (col IS NOT NULL) in the database, which lets
// PG 12+ add NOT NULL without a full scan.
type NotNullChecker interface {
	HasNotNullCheck(schema, table, column string) bool
}

// Options tunes the engine.
type Options struct {
	PGVersion     int               // e.g. 16; 0 = assume modern (>= 12)
	LargeRows     int64             // tables below this are not considered "large"; default 100_000
	Stats         Stats             // may be nil (sizes unknown => assume large)
	DefaultSchema string            // schema for unqualified names; default "public"
	Placeholders  map[string]string // ${name} values; unset ones are treated as opaque identifiers
	Tool          string            // "flyway" (default) or "liquibase": tailors the suggested alternatives
}

func (o Options) schema() string {
	if o.DefaultSchema == "" {
		return "public"
	}
	return o.DefaultSchema
}

func (o Options) largeRows() int64 {
	if o.LargeRows <= 0 {
		return 100_000
	}
	return o.LargeRows
}

func (o Options) pg() int {
	if o.PGVersion <= 0 {
		return 16
	}
	return o.PGVersion
}

// Check analyses one migration's SQL and returns findings in statement order.
func Check(sql string, opts Options) ([]Finding, error) {
	// Flyway placeholders are not valid SQL; swap them for parseable stand-ins
	// (same line structure) and restore the original text in the findings.
	sql, restore := substitutePlaceholders(sql, opts.Placeholders)
	res, err := parse(sql)
	if err != nil {
		return nil, err
	}
	e := &engine{sql: sql, opts: opts, created: map[string]bool{}, checks: map[string]bool{}}
	for _, raw := range res.Stmts {
		e.stmt(raw)
	}
	for i := range e.out {
		e.out[i].Table = restore(e.out[i].Table)
		e.out[i].Statement = restore(e.out[i].Statement)
	}
	return e.out, nil
}

type engine struct {
	sql     string
	opts    Options
	created map[string]bool // tables created earlier in this migration (empty => no lock risk)
	checks  map[string]bool // "table.col" with a validated IS NOT NULL CHECK in this migration
	out     []Finding
}

func (e *engine) stmt(raw *pg_query.RawStmt) {
	loc, n := int(raw.StmtLocation), int(raw.StmtLen)
	if n == 0 || loc+n > len(e.sql) {
		n = len(e.sql) - loc
	}
	// libpg_query's statement span includes comments/whitespace before the statement.
	end := loc + n
	loc += skipLeading(e.sql[loc:end])
	text := strings.TrimSpace(e.sql[loc:end])
	line := 1 + strings.Count(e.sql[:loc], "\n")
	ctx := stmtCtx{line: line, text: text}

	switch s := raw.Stmt.Node.(type) {
	case *pg_query.Node_CreateStmt:
		e.created[e.qualify(s.CreateStmt.Relation)] = true
	case *pg_query.Node_IndexStmt:
		e.index(ctx, s.IndexStmt)
	case *pg_query.Node_AlterTableStmt:
		if s.AlterTableStmt.Objtype != pg_query.ObjectType_OBJECT_TABLE {
			return
		}
		for _, c := range s.AlterTableStmt.Cmds {
			e.alterCmd(ctx, s.AlterTableStmt.Relation, c.GetAlterTableCmd())
		}
	}
}

type stmtCtx struct {
	line int
	text string
}

func (e *engine) index(c stmtCtx, s *pg_query.IndexStmt) {
	if s.Concurrent {
		return
	}
	alt := "CREATE INDEX CONCURRENTLY (must run outside a transaction; in Flyway set executeInTransaction=false)"
	if e.opts.Tool == "liquibase" {
		alt = `CREATE INDEX CONCURRENTLY in a <sql> change with runInTransaction="false" (the createIndex change type cannot do this)`
	}
	e.add(c, s.Relation, CreateIndex, High, estimate.IndexBuild,
		"SHARE lock: blocks writes to the table for the whole index build", alt)
}

func (e *engine) alterCmd(c stmtCtx, rel *pg_query.RangeVar, cmd *pg_query.AlterTableCmd) {
	switch cmd.Subtype {
	case pg_query.AlterTableType_AT_AddColumn:
		col := cmd.Def.GetColumnDef()
		if col == nil {
			return
		}
		if hasNonConstantDefault(col) {
			e.add(c, rel, AddColumnVolatile, High, estimate.Rewrite,
				"Non-constant default forces a full table rewrite under ACCESS EXCLUSIVE",
				"Add the column nullable, backfill in batches, then SET DEFAULT")
		}
		// otherwise metadata-only on PG 11+: safe, no finding.
	case pg_query.AlterTableType_AT_AlterColumnType:
		e.add(c, rel, AlterColumnType, High, estimate.Rewrite,
			"Full table rewrite under ACCESS EXCLUSIVE for the duration",
			"Add a new column, backfill, swap, drop the old one - or confirm the type change is binary-compatible")
	case pg_query.AlterTableType_AT_AddConstraint:
		e.constraint(c, rel, cmd.Def.GetConstraint())
	case pg_query.AlterTableType_AT_SetNotNull:
		if e.opts.pg() >= 12 && (e.checks[e.qualify(rel)+"."+cmd.Name] || e.dbHasNotNullCheck(rel, cmd.Name)) {
			return // a validated CHECK (col IS NOT NULL) lets PG 12+ skip the scan
		}
		e.add(c, rel, AddNotNull, MediumHigh, estimate.Scan,
			"Full table scan under ACCESS EXCLUSIVE to verify no NULLs exist",
			"Add a NOT VALID CHECK (col IS NOT NULL), VALIDATE it separately, then SET NOT NULL (PG 12+)")
	case pg_query.AlterTableType_AT_DropColumn:
		e.add(c, rel, DropColumn, Low, nil,
			"Metadata-only, but any running code still reading the column will break",
			"Deploy code that stops reading the column first, then drop it")
	}
}

func (e *engine) constraint(c stmtCtx, rel *pg_query.RangeVar, con *pg_query.Constraint) {
	if con == nil {
		return
	}
	switch con.Contype {
	case pg_query.ConstrType_CONSTR_UNIQUE, pg_query.ConstrType_CONSTR_PRIMARY:
		if con.Indexname != "" { // ... USING INDEX: the index was built beforehand
			return
		}
		e.add(c, rel, AddUniqueOrPK, High, estimate.IndexBuild,
			"ACCESS EXCLUSIVE lock while the backing index builds",
			"CREATE UNIQUE INDEX CONCURRENTLY first, then ADD CONSTRAINT ... USING INDEX")
	case pg_query.ConstrType_CONSTR_FOREIGN:
		if con.SkipValidation { // NOT VALID
			return
		}
		e.add(c, rel, AddForeignKey, MediumHigh, estimate.Scan,
			"Locks both tables and scans the referencing table to validate existing rows",
			"Add the constraint NOT VALID, then VALIDATE CONSTRAINT in a separate step")
	case pg_query.ConstrType_CONSTR_CHECK:
		if !con.SkipValidation && isNotNullCheck(con) {
			e.checks[e.qualify(rel)+"."+notNullCol(con)] = true
		}
	}
}

// add records a finding, applying table-size gating and the time estimate.
func (e *engine) add(c stmtCtx, rel *pg_query.RangeVar, rule string, base Risk, kind *estimate.Kind, lock, alt string) {
	table := e.qualify(rel)
	if e.created[table] {
		return // table created in this same migration: empty, nothing to lock against
	}
	f := Finding{
		Rule: rule, Risk: base, Table: table, Rows: -1, Line: c.line, Statement: c.text,
		Lock: lock, Alternative: alt,
	}
	// Size-dependent rules are only "large table" risks; DROP COLUMN is size-independent.
	if rule != DropColumn && e.opts.Stats != nil {
		schema, name := splitQualified(table)
		if rows, ok := e.opts.Stats.Rows(schema, name); ok {
			f.Rows = rows
			if rows < e.opts.largeRows() {
				f.Risk = Low
				f.Message = fmt.Sprintf("%s has ~%d rows, below the large-table threshold (%d); low risk", table, rows, e.opts.largeRows())
				e.out = append(e.out, f)
				return
			}
		}
	}
	if kind != nil && f.Rows >= 0 {
		r := estimate.For(kind, f.Rows)
		f.Estimate = &r
		f.Message = fmt.Sprintf("this will lock %s (%s rows) for approximately %s", table, estimate.HumanRows(f.Rows), r)
	} else if f.Rows < 0 && rule != DropColumn {
		f.Message = fmt.Sprintf("table size of %s is unknown (no connection, or the table has no statistics - run ANALYZE); assuming it is large", table)
	} else {
		f.Message = lock
	}
	e.out = append(e.out, f)
}

func hasNonConstantDefault(col *pg_query.ColumnDef) bool {
	if col.TypeName != nil { // serial types expand to a nextval() default later
		for _, n := range col.TypeName.Names {
			switch n.GetString_().GetSval() {
			case "serial", "bigserial", "smallserial", "serial2", "serial4", "serial8":
				return true
			}
		}
	}
	for _, n := range col.Constraints {
		con := n.GetConstraint()
		if con == nil {
			continue
		}
		switch con.Contype {
		case pg_query.ConstrType_CONSTR_DEFAULT:
			if con.RawExpr != nil && !isNonVolatile(con.RawExpr) {
				return true
			}
		case pg_query.ConstrType_CONSTR_GENERATED, pg_query.ConstrType_CONSTR_IDENTITY: // STORED generated / identity: always rewrites
			return true
		}
	}
	return false
}

// isNonVolatile reports whether a column default is evaluated once at ALTER
// time (PG 11+ stores it as metadata, no rewrite): literals, casts, negation,
// SQL value functions (CURRENT_TIMESTAMP...) and known STABLE/IMMUTABLE
// functions such as now(). Anything else (random(), clock_timestamp(),
// nextval(), unknown functions) is treated as volatile and forces a rewrite.
func isNonVolatile(n *pg_query.Node) bool {
	switch v := n.Node.(type) {
	case *pg_query.Node_AConst, *pg_query.Node_SqlvalueFunction:
		return true
	case *pg_query.Node_TypeCast:
		return isNonVolatile(v.TypeCast.Arg)
	case *pg_query.Node_AExpr: // e.g. -1
		if v.AExpr.Lexpr == nil && v.AExpr.Rexpr != nil {
			return isNonVolatile(v.AExpr.Rexpr)
		}
	case *pg_query.Node_FuncCall:
		fn := v.FuncCall.Funcname
		if len(fn) == 0 || !stableFuncs[fn[len(fn)-1].GetString_().GetSval()] {
			return false
		}
		for _, a := range v.FuncCall.Args {
			if !isNonVolatile(a) {
				return false
			}
		}
		return true
	}
	return false
}

var stableFuncs = map[string]bool{
	"now": true, "transaction_timestamp": true, "statement_timestamp": true,
	"current_setting": true, "lower": true, "upper": true, "concat": true,
	"to_timestamp": true, "make_date": true, "date_trunc": true,
	"jsonb_build_object": true, "jsonb_build_array": true, "array": true,
}

func isNotNullCheck(con *pg_query.Constraint) bool {
	return con.RawExpr != nil && con.RawExpr.GetNullTest() != nil &&
		con.RawExpr.GetNullTest().Nulltesttype == pg_query.NullTestType_IS_NOT_NULL
}

func notNullCol(con *pg_query.Constraint) string {
	ref := con.RawExpr.GetNullTest().Arg.GetColumnRef()
	if ref == nil || len(ref.Fields) == 0 {
		return ""
	}
	return ref.Fields[len(ref.Fields)-1].GetString_().GetSval()
}

func (e *engine) qualify(r *pg_query.RangeVar) string {
	if r.Schemaname == "" {
		return e.opts.schema() + "." + r.Relname
	}
	return r.Schemaname + "." + r.Relname
}

func (e *engine) dbHasNotNullCheck(rel *pg_query.RangeVar, col string) bool {
	c, ok := e.opts.Stats.(NotNullChecker)
	if !ok {
		return false
	}
	schema, name := splitQualified(e.qualify(rel))
	return c.HasNotNullCheck(schema, name, col)
}

func splitQualified(q string) (schema, table string) {
	i := strings.IndexByte(q, '.')
	return q[:i], q[i+1:]
}

// skipLeading returns the offset of the first byte that is not whitespace or a comment.
func skipLeading(s string) int {
	i := 0
	for i < len(s) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(s[i])):
			i++
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return len(s)
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i:], "*/")
			if j < 0 {
				return len(s)
			}
			i += j + 2
		default:
			return i
		}
	}
	return i
}

func parse(sql string) (*pg_query.ParseResult, error) {
	return pgParse(sql)
}
