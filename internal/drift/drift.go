// Package drift compares two schema snapshots and reports the differences.
package drift

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// Kinds of difference.
const (
	TableMissing      = "table-missing" // in expected, absent from actual
	TableExtra        = "table-extra"   // in actual, not expected
	ColumnMissing     = "column-missing"
	ColumnExtra       = "column-extra"
	ColumnTypeChanged = "column-type-changed"
	ColumnNullability = "column-nullability-changed"
	ColumnDefault     = "column-default-changed"
	IndexMissing      = "index-missing"
	IndexExtra        = "index-extra"
	IndexChanged      = "index-changed"
	ConstraintMissing = "constraint-missing"
	ConstraintExtra   = "constraint-extra"
	ConstraintChanged = "constraint-changed"
	TriggerMissing    = "trigger-missing"
	TriggerExtra      = "trigger-extra"
	TriggerChanged    = "trigger-changed"
	ViewMissing       = "view-missing"
	ViewExtra         = "view-extra"
	ViewChanged       = "view-changed"
	SequenceMissing   = "sequence-missing"
	SequenceExtra     = "sequence-extra"
	SequenceChanged   = "sequence-changed"
	EnumMissing       = "enum-missing"
	EnumExtra         = "enum-extra"
	EnumChanged       = "enum-changed"
)

// Difference is one concrete divergence, named precisely enough to act on.
type Difference struct {
	Kind     string `json:"kind"`
	Table    string `json:"table"`
	Object   string `json:"object,omitempty"` // column / index / constraint name
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

func (d Difference) String() string {
	obj := d.Table
	if d.Object != "" {
		obj += "." + d.Object
	}
	switch d.Kind {
	case TableMissing, ViewMissing, SequenceMissing, EnumMissing:
		return fmt.Sprintf("%s %s is missing (expected by migrations)", strings.TrimSuffix(d.Kind, "-missing"), d.Table)
	case TableExtra, ViewExtra, SequenceExtra, EnumExtra:
		return fmt.Sprintf("%s %s exists but is not in the migrations", strings.TrimSuffix(d.Kind, "-extra"), d.Table)
	case ColumnMissing, IndexMissing, ConstraintMissing, TriggerMissing:
		return fmt.Sprintf("%s %s is missing (expected %s)", strings.TrimSuffix(d.Kind, "-missing"), obj, d.Expected)
	case ColumnExtra, IndexExtra, ConstraintExtra, TriggerExtra:
		return fmt.Sprintf("%s %s was added outside migrations (%s)", strings.TrimSuffix(d.Kind, "-extra"), obj, d.Actual)
	}
	return fmt.Sprintf("%s: %s, expected %q, found %q", obj, strings.ReplaceAll(d.Kind, "-", " "), d.Expected, d.Actual)
}

// Options controls what is compared.
type Options struct {
	// Ignore patterns (path.Match globs) matched against "schema.table" and
	// "schema.table.object". Intentional per-environment differences go here.
	Ignore []string
}

// DefaultIgnore skips migration-tool bookkeeping tables, which legitimately differ.
var DefaultIgnore = []string{"*.flyway_schema_history", "*.databasechangelog", "*.databasechangeloglock"}

func (o Options) ignored(name string) bool {
	for _, p := range DefaultIgnore {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	for _, p := range o.Ignore {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// Compare returns the differences between expected and actual, sorted.
func Compare(expected, actual *snapshot.Schema, opts Options) []Difference {
	var out []Difference
	exp, act := tableMap(expected), tableMap(actual)

	for name, et := range exp {
		if opts.ignored(name) {
			continue
		}
		at, ok := act[name]
		if !ok {
			out = append(out, Difference{Kind: TableMissing, Table: name})
			continue
		}
		out = append(out, compareTable(et, at, opts)...)
	}
	for name := range act {
		if _, ok := exp[name]; !ok && !opts.ignored(name) {
			out = append(out, Difference{Kind: TableExtra, Table: name})
		}
	}
	out = append(out, compareViews(expected, actual, opts)...)
	out = append(out, compareSequences(expected, actual, opts)...)
	out = append(out, compareEnums(expected, actual, opts)...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Object < b.Object
	})
	return out
}

func compareTable(e, a snapshot.Table, opts Options) []Difference {
	var out []Difference
	t := e.QName()
	skip := func(obj string) bool { return opts.ignored(t + "." + obj) }

	ec, ac := cols(e), cols(a)
	for n, c := range ec {
		if skip(n) {
			continue
		}
		o, ok := ac[n]
		switch {
		case !ok:
			out = append(out, Difference{Kind: ColumnMissing, Table: t, Object: n, Expected: c.Type})
		case c.Type != o.Type:
			out = append(out, Difference{Kind: ColumnTypeChanged, Table: t, Object: n, Expected: c.Type, Actual: o.Type})
		default:
			if c.NotNull != o.NotNull {
				out = append(out, Difference{Kind: ColumnNullability, Table: t, Object: n, Expected: nullStr(c.NotNull), Actual: nullStr(o.NotNull)})
			}
			if c.Default != o.Default || c.Identity != o.Identity || c.Generated != o.Generated {
				out = append(out, Difference{Kind: ColumnDefault, Table: t, Object: n, Expected: colDefault(c), Actual: colDefault(o)})
			}
		}
	}
	for n, c := range ac {
		if _, ok := ec[n]; !ok && !skip(n) {
			out = append(out, Difference{Kind: ColumnExtra, Table: t, Object: n, Actual: c.Type})
		}
	}

	ei, ai := idxs(e), idxs(a)
	for n, i := range ei {
		if skip(n) {
			continue
		}
		o, ok := ai[n]
		if !ok {
			out = append(out, Difference{Kind: IndexMissing, Table: t, Object: n, Expected: i.Def})
		} else if i.Def != o.Def {
			out = append(out, Difference{Kind: IndexChanged, Table: t, Object: n, Expected: i.Def, Actual: o.Def})
		}
	}
	for n, i := range ai {
		if _, ok := ei[n]; !ok && !skip(n) {
			out = append(out, Difference{Kind: IndexExtra, Table: t, Object: n, Actual: i.Def})
		}
	}

	ek, ak := cons(e), cons(a)
	for n, c := range ek {
		if skip(n) {
			continue
		}
		o, ok := ak[n]
		if !ok {
			out = append(out, Difference{Kind: ConstraintMissing, Table: t, Object: n, Expected: c.Def})
		} else if c.Def != o.Def || c.Validated != o.Validated {
			out = append(out, Difference{Kind: ConstraintChanged, Table: t, Object: n, Expected: conDesc(c), Actual: conDesc(o)})
		}
	}
	for n, c := range ak {
		if _, ok := ek[n]; !ok && !skip(n) {
			out = append(out, Difference{Kind: ConstraintExtra, Table: t, Object: n, Actual: c.Def})
		}
	}

	et, at := trigs(e), trigs(a)
	for n, tr := range et {
		if skip(n) {
			continue
		}
		o, ok := at[n]
		if !ok {
			out = append(out, Difference{Kind: TriggerMissing, Table: t, Object: n, Expected: tr.Def})
		} else if tr.Def != o.Def {
			out = append(out, Difference{Kind: TriggerChanged, Table: t, Object: n, Expected: tr.Def, Actual: o.Def})
		}
	}
	for n, tr := range at {
		if _, ok := et[n]; !ok && !skip(n) {
			out = append(out, Difference{Kind: TriggerExtra, Table: t, Object: n, Actual: tr.Def})
		}
	}
	return out
}

func nullStr(notNull bool) string {
	if notNull {
		return "NOT NULL"
	}
	return "NULL"
}

func colDefault(c snapshot.Column) string {
	switch {
	case c.Generated != "":
		return "GENERATED " + c.Default
	case c.Identity != "":
		return "IDENTITY(" + c.Identity + ")"
	case c.Default == "":
		return "(none)"
	}
	return c.Default
}

func conDesc(c snapshot.Constraint) string {
	if !c.Validated {
		return c.Def + " (NOT VALIDATED)"
	}
	return c.Def
}

func tableMap(s *snapshot.Schema) map[string]snapshot.Table {
	m := map[string]snapshot.Table{}
	for _, t := range s.Tables {
		m[t.QName()] = t
	}
	return m
}

func cols(t snapshot.Table) map[string]snapshot.Column {
	m := map[string]snapshot.Column{}
	for _, c := range t.Columns {
		m[c.Name] = c
	}
	return m
}

func idxs(t snapshot.Table) map[string]snapshot.Index {
	m := map[string]snapshot.Index{}
	for _, c := range t.Indexes {
		m[c.Name] = c
	}
	return m
}

func cons(t snapshot.Table) map[string]snapshot.Constraint {
	m := map[string]snapshot.Constraint{}
	for _, c := range t.Constraints {
		m[c.Name] = c
	}
	return m
}

func trigs(t snapshot.Table) map[string]snapshot.Trigger {
	m := map[string]snapshot.Trigger{}
	for _, c := range t.Triggers {
		m[c.Name] = c
	}
	return m
}

func compareViews(e, a *snapshot.Schema, opts Options) []Difference {
	var out []Difference
	em, am := map[string]snapshot.View{}, map[string]snapshot.View{}
	for _, v := range e.Views {
		em[v.QName()] = v
	}
	for _, v := range a.Views {
		am[v.QName()] = v
	}
	for n, v := range em {
		if opts.ignored(n) {
			continue
		}
		o, ok := am[n]
		switch {
		case !ok:
			out = append(out, Difference{Kind: ViewMissing, Table: n})
		case v.Def != o.Def || v.Materialized != o.Materialized:
			out = append(out, Difference{Kind: ViewChanged, Table: n, Expected: viewDesc(v), Actual: viewDesc(o)})
		}
	}
	for n := range am {
		if _, ok := em[n]; !ok && !opts.ignored(n) {
			out = append(out, Difference{Kind: ViewExtra, Table: n})
		}
	}
	return out
}

func viewDesc(v snapshot.View) string {
	if v.Materialized {
		return "MATERIALIZED " + v.Def
	}
	return v.Def
}

func compareSequences(e, a *snapshot.Schema, opts Options) []Difference {
	var out []Difference
	em, am := map[string]snapshot.Sequence{}, map[string]snapshot.Sequence{}
	for _, v := range e.Sequences {
		em[v.QName()] = v
	}
	for _, v := range a.Sequences {
		am[v.QName()] = v
	}
	for n, v := range em {
		if opts.ignored(n) {
			continue
		}
		o, ok := am[n]
		switch {
		case !ok:
			out = append(out, Difference{Kind: SequenceMissing, Table: n})
		case v != o:
			out = append(out, Difference{Kind: SequenceChanged, Table: n, Expected: seqDesc(v), Actual: seqDesc(o)})
		}
	}
	for n := range am {
		if _, ok := em[n]; !ok && !opts.ignored(n) {
			out = append(out, Difference{Kind: SequenceExtra, Table: n})
		}
	}
	return out
}

func seqDesc(q snapshot.Sequence) string {
	return fmt.Sprintf("%s start %d increment %d min %d max %d cycle=%v", q.Type, q.Start, q.Increment, q.Min, q.Max, q.Cycle)
}

func compareEnums(e, a *snapshot.Schema, opts Options) []Difference {
	var out []Difference
	em, am := map[string]snapshot.Enum{}, map[string]snapshot.Enum{}
	for _, v := range e.Enums {
		em[v.QName()] = v
	}
	for _, v := range a.Enums {
		am[v.QName()] = v
	}
	for n, v := range em {
		if opts.ignored(n) {
			continue
		}
		o, ok := am[n]
		switch {
		case !ok:
			out = append(out, Difference{Kind: EnumMissing, Table: n})
		case strings.Join(v.Labels, "\x00") != strings.Join(o.Labels, "\x00"):
			out = append(out, Difference{Kind: EnumChanged, Table: n, Expected: strings.Join(v.Labels, ", "), Actual: strings.Join(o.Labels, ", ")})
		}
	}
	for n := range am {
		if _, ok := em[n]; !ok && !opts.ignored(n) {
			out = append(out, Difference{Kind: EnumExtra, Table: n})
		}
	}
	return out
}
