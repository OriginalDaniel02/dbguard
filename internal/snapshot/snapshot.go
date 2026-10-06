// Package snapshot defines a normalized, comparable view of a database schema
// (tables, columns, indexes, constraints) and its JSON form. Extraction from a
// live database lives in internal/pg; this package has no database dependency.
package snapshot

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Version of the snapshot file format.
const Version = 1

type Schema struct {
	FormatVersion int        `json:"format_version"`
	Engine        string     `json:"engine"`
	ServerMajor   int        `json:"server_major,omitempty"`
	Tables        []Table    `json:"tables"`
	Views         []View     `json:"views,omitempty"` // includes materialized views
	Sequences     []Sequence `json:"sequences,omitempty"`
	Enums         []Enum     `json:"enums,omitempty"`
}

type View struct {
	Schema       string `json:"schema"`
	Name         string `json:"name"`
	Def          string `json:"def"`
	Materialized bool   `json:"materialized,omitempty"`
}

// QName is the schema-qualified view name.
func (v View) QName() string { return qualified(v.Schema, v.Name) }

// qualified joins schema and name; MySQL objects have no schema qualifier.
func qualified(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

type Sequence struct {
	Schema    string `json:"schema"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Start     int64  `json:"start"`
	Min       int64  `json:"min"`
	Max       int64  `json:"max"`
	Increment int64  `json:"increment"`
	Cycle     bool   `json:"cycle,omitempty"`
}

func (q Sequence) QName() string { return qualified(q.Schema, q.Name) }

// Enum labels are kept in their sort order, which is meaningful.
type Enum struct {
	Schema string   `json:"schema"`
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

func (e Enum) QName() string { return qualified(e.Schema, e.Name) }

type Trigger struct {
	Name string `json:"name"`
	Def  string `json:"def"`
}

type Table struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes,omitempty"`
	Constraints []Constraint `json:"constraints,omitempty"`
	Triggers    []Trigger    `json:"triggers,omitempty"`
}

// QName is the schema-qualified table name.
func (t Table) QName() string { return qualified(t.Schema, t.Name) }

type Column struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	NotNull   bool   `json:"not_null,omitempty"`
	Default   string `json:"default,omitempty"`
	Identity  string `json:"identity,omitempty"`  // "a" always, "d" by default
	Generated string `json:"generated,omitempty"` // "s" stored
}

type Index struct {
	Name    string `json:"name"`
	Def     string `json:"def"`
	Unique  bool   `json:"unique,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type Constraint struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // p primary, u unique, f foreign, c check, x exclusion
	Def       string `json:"def"`
	Validated bool   `json:"validated"`
}

// Normalize sorts everything so snapshots are deterministic and diffable.
// Column order is intentionally not preserved: it is not meaningful drift.
func (s *Schema) Normalize() {
	s.FormatVersion = Version
	sort.Slice(s.Tables, func(i, j int) bool { return s.Tables[i].QName() < s.Tables[j].QName() })
	for i := range s.Tables {
		t := &s.Tables[i]
		sort.Slice(t.Columns, func(a, b int) bool { return t.Columns[a].Name < t.Columns[b].Name })
		sort.Slice(t.Indexes, func(a, b int) bool { return t.Indexes[a].Name < t.Indexes[b].Name })
		sort.Slice(t.Constraints, func(a, b int) bool { return t.Constraints[a].Name < t.Constraints[b].Name })
		sort.Slice(t.Triggers, func(a, b int) bool { return t.Triggers[a].Name < t.Triggers[b].Name })
	}
	sort.Slice(s.Views, func(i, j int) bool { return s.Views[i].QName() < s.Views[j].QName() })
	sort.Slice(s.Sequences, func(i, j int) bool { return s.Sequences[i].QName() < s.Sequences[j].QName() })
	sort.Slice(s.Enums, func(i, j int) bool { return s.Enums[i].QName() < s.Enums[j].QName() })
}

func Write(w io.Writer, s *Schema) error {
	s.Normalize()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

func Read(r io.Reader) (*Schema, error) {
	var s Schema
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return nil, fmt.Errorf("invalid snapshot file: %w", err)
	}
	if s.FormatVersion != Version {
		return nil, fmt.Errorf("unsupported snapshot format_version %d (want %d)", s.FormatVersion, Version)
	}
	s.Normalize()
	return &s, nil
}
