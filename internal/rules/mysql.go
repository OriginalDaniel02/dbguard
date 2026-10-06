package rules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver" // value expression implementation for the parser

	"github.com/OriginalDaniel02/dbguard/internal/estimate"
)

// MySQL rules. Each classification was verified against a real MySQL 8.0.46 by
// asking the server which ALGORITHM / LOCK combination it accepts for the statement
// (see docs/mysql.md): INSTANT and INPLACE+LOCK=NONE never block writes; COPY and
// INPLACE+LOCK=SHARED do.

// CheckMySQL analyses one MySQL migration's SQL and returns findings in statement
// order. A statement the parser cannot read is an error.
func CheckMySQL(sql string, opts Options) ([]Finding, error) {
	fs, problems := checkMySQL(sql, opts, false)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", problems[0])
	}
	return fs, nil
}

// CheckMySQLLenient is CheckMySQL for real-world files: a statement the parser
// cannot read is reported in problems (and not analyzed) instead of failing the
// whole file, so one exotic statement does not hide the risks in the others.
func CheckMySQLLenient(sql string, opts Options) (findings []Finding, problems []string) {
	return checkMySQL(sql, opts, true)
}

func checkMySQL(sql string, opts Options, lenient bool) ([]Finding, []string) {
	sql, restorePH := substitutePlaceholders(sql, opts.Placeholders)
	sql, restoreExpr := markExpressionDefaults(sql)
	restore := func(s string) string { return restorePH(restoreExpr(s)) }

	type located struct {
		st    ast.StmtNode
		start int // byte offset of the statement's text (before leading comments)
		text  string
	}
	var stmts []located
	var problems []string

	if parsed, _, err := parser.New().Parse(sql, "", ""); err == nil {
		cursor := 0
		for _, st := range parsed {
			text := strings.TrimSpace(st.Text())
			idx := strings.Index(sql[cursor:], text)
			if idx < 0 {
				idx = 0
			}
			start := cursor + idx
			cursor = start + len(text)
			stmts = append(stmts, located{st, start, text})
		}
	} else if !lenient {
		return nil, []string{fmt.Sprintf("MySQL syntax error: %v", err)}
	} else {
		// Fall back to statement-by-statement parsing.
		for _, seg := range splitSQL(sql) {
			parsed, _, err := parser.New().Parse(seg.text, "", "")
			if err != nil {
				line := 1 + strings.Count(sql[:seg.start+skipLeading(seg.text)], "\n")
				problems = append(problems, fmt.Sprintf("line %d: could not parse this statement, so it was NOT analyzed (%s)", line, firstLine(restore(err.Error()))))
				continue
			}
			cursor := 0
			for _, st := range parsed {
				text := strings.TrimSpace(st.Text())
				idx := strings.Index(seg.text[cursor:], text)
				if idx < 0 {
					idx = 0
				}
				start := cursor + idx
				cursor = start + len(text)
				stmts = append(stmts, located{st, seg.start + start, text})
			}
		}
	}

	e := &myEngine{sql: sql, opts: opts, created: map[string]bool{}}
	for _, l := range stmts {
		// Text() of a statement includes comments written before it.
		skip := skipLeading(l.text)
		start := l.start + skip
		text := strings.TrimSpace(l.text[skip:])
		e.stmt(l.st, stmtCtx{line: 1 + strings.Count(sql[:start], "\n"), text: text})
	}
	for i := range e.out {
		e.out[i].Table = restore(e.out[i].Table)
		e.out[i].Statement = restore(e.out[i].Statement)
	}
	return e.out, problems
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// MySQLParses reports whether sql is valid MySQL syntax (as the parser reads it).
func MySQLParses(sql string) error {
	sql, _ = substitutePlaceholders(sql, nil)
	sql, _ = markExpressionDefaults(sql)
	_, _, err := parser.New().Parse(sql, "", "")
	return err
}

type myEngine struct {
	sql     string
	opts    Options
	created map[string]bool
	fkOff   bool // SET foreign_key_checks = 0 seen earlier in this migration
	out     []Finding
}

var fkChecksOff = regexp.MustCompile(`(?i)foreign_key_checks\s*(?::=|=)\s*(0|off|false)\b`)
var fkChecksOn = regexp.MustCompile(`(?i)foreign_key_checks\s*(?::=|=)\s*(1|on|true)\b`)

func (e *myEngine) stmt(st ast.StmtNode, c stmtCtx) {
	switch n := st.(type) {
	case *ast.SetStmt:
		switch {
		case fkChecksOff.MatchString(c.text):
			e.fkOff = true
		case fkChecksOn.MatchString(c.text):
			e.fkOff = false
		}
	case *ast.CreateTableStmt:
		if n.Table != nil {
			e.created[e.qname(n.Table)] = true
		}
	case *ast.CreateIndexStmt:
		e.createIndex(c, n)
	case *ast.OptimizeTableStmt:
		for _, t := range n.Tables {
			e.add(c, t, TableRebuild, Medium, estimate.MySQLRebuild, false,
				"OPTIMIZE TABLE rebuilds the table (online DDL: writes continue, but it is heavy)",
				"Run it off-peak; watch replica lag; for very large tables use gh-ost or pt-online-schema-change")
		}
	case *ast.AlterTableStmt:
		e.alter(c, n)
	}
}

func (e *myEngine) qname(t *ast.TableName) string {
	schema := t.Schema.O
	if schema == "" {
		schema = e.opts.DefaultSchema
	}
	if schema == "" {
		return t.Name.O
	}
	return schema + "." + t.Name.O
}

func (e *myEngine) createIndex(c stmtCtx, n *ast.CreateIndexStmt) {
	var alg ast.AlgorithmType
	var lock ast.LockType
	if n.LockAlg != nil {
		alg, lock = n.LockAlg.AlgorithmTp, n.LockAlg.LockTp
	}
	if e.explicit(c, n.Table, alg, lock) {
		return
	}
	e.indexFinding(c, n.Table, n.KeyType == ast.IndexKeyTypeFulltext || n.KeyType == ast.IndexKeyTypeSpatial)
}

func (e *myEngine) indexFinding(c stmtCtx, t *ast.TableName, blocking bool) {
	if blocking {
		e.add(c, t, CreateIndex, High, estimate.MySQLCopy, true,
			"the first FULLTEXT / SPATIAL index is built INPLACE but does not allow concurrent writes",
			"Add ALGORITHM=INPLACE, LOCK=NONE so MySQL fails instead of blocking; or build it with gh-ost / pt-online-schema-change")
		return
	}
	e.add(c, t, CreateIndex, Low, estimate.MySQLIndex, false,
		"online (INPLACE, LOCK=NONE): writes continue, but a big index build is I/O-heavy and can lag replicas",
		"Add ALGORITHM=INPLACE, LOCK=NONE so MySQL fails instead of blocking if it cannot run online")
}

// explicit handles statements that state ALGORITHM / LOCK. It reports true when
// that fully decides the outcome (no further findings needed).
func (e *myEngine) explicit(c stmtCtx, t *ast.TableName, alg ast.AlgorithmType, lock ast.LockType) bool {
	switch {
	case alg == ast.AlgorithmTypeCopy:
		e.add(c, t, TableCopy, High, estimate.MySQLCopy, true,
			"ALGORITHM=COPY copies the whole table and blocks writes for the duration",
			"Use ALGORITHM=INPLACE or INSTANT with LOCK=NONE, or run the change with gh-ost / pt-online-schema-change")
		return true
	case lock == ast.LockTypeShared || lock == ast.LockTypeExclusive:
		what := "blocks writes"
		if lock == ast.LockTypeExclusive {
			what = "blocks all reads and writes"
		}
		e.add(c, t, BlocksWrites, High, estimate.MySQLCopy, true,
			"an explicit LOCK clause "+what+" while the change runs",
			"Use LOCK=NONE (MySQL then fails instead of blocking if it cannot run online)")
		return true
	case lock == ast.LockTypeNone || alg == ast.AlgorithmTypeInstant:
		// MySQL refuses the statement rather than blocking writes: nothing to warn about.
		return true
	}
	return false
}

func (e *myEngine) alter(c stmtCtx, n *ast.AlterTableStmt) {
	var alg ast.AlgorithmType
	var lock ast.LockType
	hasAddPK := false
	for _, sp := range n.Specs {
		switch sp.Tp {
		case ast.AlterTableAlgorithm:
			alg = sp.Algorithm
		case ast.AlterTableLock:
			lock = sp.LockType
		case ast.AlterTableAddConstraint:
			if sp.Constraint != nil && sp.Constraint.Tp == ast.ConstraintPrimaryKey {
				hasAddPK = true
			}
		}
	}
	guaranteed := e.explicit(c, n.Table, alg, lock)

	for _, sp := range n.Specs {
		switch sp.Tp {
		case ast.AlterTableAddColumns:
			for _, col := range sp.NewColumns {
				e.addColumn(c, n.Table, col, sp.Position, guaranteed)
			}
		case ast.AlterTableDropColumn:
			if !guaranteed && e.opts.my() < 80029 {
				e.add(c, n.Table, TableRebuild, Medium, estimate.MySQLRebuild, false,
					"before MySQL 8.0.29 DROP COLUMN rebuilds the table (online: writes continue, but it is heavy)",
					"Upgrade to 8.0.29+ where DROP COLUMN is INSTANT, or add ALGORITHM=INSTANT to fail fast")
			}
			e.add(c, n.Table, DropColumn, Low, nil, false,
				"Metadata-only on MySQL 8.0.29+, but any running code still reading the column will break",
				"Deploy code that stops reading the column first, then drop it")
		case ast.AlterTableModifyColumn, ast.AlterTableChangeColumn:
			if guaranteed {
				continue
			}
			for _, col := range sp.NewColumns {
				old := sp.OldColumnName
				name := col.Name.Name.O
				if old != nil {
					name = old.Name.O
				}
				e.modifyColumn(c, n.Table, name, col, sp.Position)
			}
		case ast.AlterTableAddConstraint:
			if !guaranteed {
				e.addConstraint(c, n.Table, sp.Constraint)
			}
		case ast.AlterTableDropPrimaryKey:
			if !guaranteed && !hasAddPK {
				e.add(c, n.Table, TableCopy, High, estimate.MySQLCopy, true,
					"DROP PRIMARY KEY without adding a new one in the same statement copies the table and blocks writes",
					"Drop and add the primary key in a single ALTER TABLE (INPLACE, writes continue), or use gh-ost / pt-online-schema-change")
			}
		case ast.AlterTableOption:
			if !guaranteed {
				e.tableOptions(c, n.Table, sp)
			}
		case ast.AlterTableForce:
			if !guaranteed {
				e.add(c, n.Table, TableRebuild, Medium, estimate.MySQLRebuild, false,
					"ALTER TABLE ... FORCE rebuilds the table (online: writes continue, but it is heavy)",
					"Run it off-peak and watch replica lag")
			}
		}
	}
}

func (e *myEngine) addColumn(c stmtCtx, t *ast.TableName, col *ast.ColumnDef, pos *ast.ColumnPosition, guaranteed bool) {
	if guaranteed {
		return
	}
	for _, o := range col.Options {
		switch o.Tp {
		case ast.ColumnOptionGenerated:
			if o.Stored {
				e.add(c, t, TableCopy, High, estimate.MySQLCopy, true,
					"adding a STORED generated column copies the table and blocks writes",
					"Add a VIRTUAL generated column (INSTANT), or add a plain column and backfill in batches")
				return
			}
		case ast.ColumnOptionAutoIncrement:
			e.add(c, t, BlocksWrites, High, estimate.MySQLCopy, true,
				"adding an AUTO_INCREMENT column is built INPLACE but does not allow concurrent writes",
				"Avoid adding AUTO_INCREMENT to a large table in place; use gh-ost / pt-online-schema-change")
			return
		case ast.ColumnOptionDefaultValue:
			if !constantDefault(o.Expr) {
				e.add(c, t, AddColumnVolatile, High, estimate.MySQLCopy, true,
					"a DEFAULT (expression) forces a table copy (INSTANT and INPLACE are not supported) and blocks writes",
					"Add the column with a constant default (INSTANT), then backfill the expression values in batches")
				return
			}
		}
	}
	// A plain ADD COLUMN is INSTANT on 8.0.29+ (any position) and 8.0.12+ (last column only).
	v := e.opts.my()
	positioned := pos != nil && pos.Tp != ast.ColumnPositionNone
	if v >= 80029 || (v >= 80012 && !positioned) {
		return
	}
	e.add(c, t, TableRebuild, Medium, estimate.MySQLRebuild, false,
		"on this MySQL version ADD COLUMN rebuilds the table (online: writes continue, but it is heavy)",
		"Upgrade to 8.0.29+ where ADD COLUMN is INSTANT in any position, or add ALGORITHM=INSTANT to fail fast")
}

// constantDefault reports whether a DEFAULT is a literal or CURRENT_TIMESTAMP (instant),
// as opposed to a general expression (table copy).
func constantDefault(expr ast.ExprNode) bool {
	switch v := expr.(type) {
	case nil:
		return true
	case ast.ValueExpr:
		return true
	case *ast.UnaryOperationExpr:
		return constantDefault(v.V)
	case *ast.FuncCallExpr:
		switch v.FnName.L {
		case "current_timestamp", "now", "localtime", "localtimestamp":
			return true
		}
	}
	return false
}

func (e *myEngine) modifyColumn(c stmtCtx, t *ast.TableName, oldName string, col *ast.ColumnDef, pos *ast.ColumnPosition) {
	newType := normType(col.Tp.CompactStr())
	newNotNull := false
	for _, o := range col.Options {
		if o.Tp == ast.ColumnOptionNotNull {
			newNotNull = true
		}
	}
	moved := pos != nil && pos.Tp != ast.ColumnPositionNone

	var info ColumnInfo
	known := false
	if ct, ok := e.opts.Stats.(ColumnTyper); ok && e.opts.Stats != nil {
		schema, table := e.schemaTable(t)
		info, known = ct.Column(schema, table, oldName)
	}

	if !known {
		e.add(c, t, AlterColumnType, High, estimate.MySQLCopy, true,
			"MODIFY/CHANGE COLUMN is assumed to change the data type, which copies the table and blocks writes (connect to the database to check whether only nullability changes)",
			"Add a new column, backfill in batches, swap and drop the old one; or use gh-ost / pt-online-schema-change; add LOCK=NONE so MySQL fails instead of blocking")
		return
	}
	old := normType(info.Type)
	switch {
	case old == newType:
		if info.NotNull != newNotNull || moved {
			e.add(c, t, TableRebuild, Medium, estimate.MySQLRebuild, false,
				"changing nullability or column order rebuilds the table (online: writes continue, but it is heavy)",
				"Run it off-peak and watch replica lag; for NULL to NOT NULL, backfill the NULLs first")
		}
		// Same type, same nullability (rename / default / comment): INSTANT or metadata-only.
	case varcharWidenInPlace(old, newType, info.Charset):
		// Extending a VARCHAR within the same length-byte size is INPLACE with writes allowed.
	default:
		e.add(c, t, AlterColumnType, High, estimate.MySQLCopy, true,
			fmt.Sprintf("changing %s to %s copies the table and blocks writes", old, newType),
			"Add a new column, backfill in batches, swap and drop the old one; or use gh-ost / pt-online-schema-change; add LOCK=NONE so MySQL fails instead of blocking")
	}
}

func (e *myEngine) schemaTable(t *ast.TableName) (string, string) {
	schema := t.Schema.O
	if schema == "" {
		schema = e.opts.DefaultSchema
	}
	return schema, t.Name.O
}

func (e *myEngine) addConstraint(c stmtCtx, t *ast.TableName, con *ast.Constraint) {
	if con == nil {
		return
	}
	switch con.Tp {
	case ast.ConstraintPrimaryKey:
		e.add(c, t, AddUniqueOrPK, Medium, estimate.MySQLRebuild, false,
			"ADD PRIMARY KEY rebuilds the table INPLACE (writes continue, but it is heavy)",
			"Run it off-peak with ALGORITHM=INPLACE, LOCK=NONE; watch replica lag")
	case ast.ConstraintKey, ast.ConstraintIndex, ast.ConstraintUniq, ast.ConstraintUniqKey, ast.ConstraintUniqIndex:
		e.indexFinding(c, t, false)
	case ast.ConstraintFulltext:
		e.indexFinding(c, t, true)
	case ast.ConstraintForeignKey:
		if e.fkOff {
			e.add(c, t, AddForeignKey, Medium, estimate.MySQLRebuild, false,
				"with foreign_key_checks=0 ADD FOREIGN KEY runs INPLACE (writes continue), but existing rows are NOT validated",
				"Validate the existing rows separately (e.g. an anti-join query) before relying on the constraint")
			return
		}
		e.add(c, t, AddForeignKey, High, estimate.MySQLCopy, true,
			"with foreign_key_checks=1 (the default) ADD FOREIGN KEY copies the table and blocks writes",
			"Run SET foreign_key_checks=0 first so it runs INPLACE (then validate existing rows separately), or use gh-ost / pt-online-schema-change")
	case ast.ConstraintCheck:
		e.add(c, t, AddCheck, High, estimate.MySQLCopy, true,
			"ADD CHECK copies the whole table and blocks writes (it cannot run INPLACE)",
			"Add the CHECK to the CREATE TABLE of a new table, or enforce it in the application; use gh-ost / pt-online-schema-change for existing big tables")
	}
}

func (e *myEngine) tableOptions(c stmtCtx, t *ast.TableName, sp *ast.AlterTableSpec) {
	for _, o := range sp.Options {
		switch o.Tp {
		case ast.TableOptionCharset:
			if o.UintValue == ast.TableOptionCharsetWithConvertTo {
				e.add(c, t, TableCopy, High, estimate.MySQLCopy, true,
					"CONVERT TO CHARACTER SET copies the whole table and blocks writes",
					"Use gh-ost / pt-online-schema-change, or convert column by column on a copy")
			}
		case ast.TableOptionEngine, ast.TableOptionRowFormat:
			e.add(c, t, TableRebuild, Medium, estimate.MySQLRebuild, false,
				"changing the engine / row format rebuilds the table (online: writes continue, but it is heavy)",
				"Run it off-peak and watch replica lag")
		}
	}
}

// add records a finding with table-size gating and a duration estimate.
func (e *myEngine) add(c stmtCtx, t *ast.TableName, rule string, base Risk, kind *estimate.Kind, blocks bool, lock, alt string) {
	table := e.qname(t)
	if e.created[table] {
		return // created in this migration's transaction: invisible to other sessions
	}
	f := Finding{Rule: rule, Risk: base, Table: table, Rows: -1, Line: c.line, Statement: c.text, Lock: lock, Alternative: alt}
	if rule != DropColumn && e.opts.Stats != nil {
		schema, name := e.schemaTable(t)
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
	switch {
	case kind != nil && f.Rows >= 0:
		r := estimate.For(kind, f.Rows)
		f.Estimate = &r
		if blocks {
			f.Message = fmt.Sprintf("this will block writes to %s (%s rows) for approximately %s", table, estimate.HumanRows(f.Rows), r)
		} else {
			f.Message = fmt.Sprintf("this will run online on %s (%s rows) for approximately %s: writes continue, but expect heavy I/O and replica lag", table, estimate.HumanRows(f.Rows), r)
		}
	case f.Rows < 0 && rule != DropColumn:
		f.Message = fmt.Sprintf("table size of %s is unknown (no connection, or no statistics - run ANALYZE TABLE); assuming it is large", table)
	default:
		f.Message = lock
	}
	e.out = append(e.out, f)
}

var intWidth = regexp.MustCompile(`\b(tinyint|smallint|mediumint|int|bigint)\(\d+\)`)

// normType makes a parsed type and information_schema's COLUMN_TYPE comparable:
// lower case, integer display widths removed.
func normType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = intWidth.ReplaceAllString(s, "$1")
	return strings.Join(strings.Fields(s), " ")
}

var varcharRe = regexp.MustCompile(`^varchar\((\d+)\)$`)

// varcharWidenInPlace: widening a VARCHAR stays INPLACE only while the length
// prefix stays 1 byte (max byte length <= 255); crossing to 2 bytes copies the table.
func varcharWidenInPlace(oldT, newT, charset string) bool {
	om, nm := varcharRe.FindStringSubmatch(oldT), varcharRe.FindStringSubmatch(newT)
	if om == nil || nm == nil {
		return false
	}
	ol, _ := strconv.Atoi(om[1])
	nl, _ := strconv.Atoi(nm[1])
	if nl <= ol {
		return false
	}
	per := bytesPerChar(charset)
	return ol*per <= 255 && nl*per <= 255 || ol*per > 255 && nl*per > 255
}

func bytesPerChar(charset string) int {
	switch strings.ToLower(charset) {
	case "latin1", "ascii", "binary":
		return 1
	case "utf8", "utf8mb3":
		return 3
	}
	return 4 // utf8mb4 (the MySQL 8 default)
}
