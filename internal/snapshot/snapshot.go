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
	FormatVersion int     `json:"format_version"`
	Engine        string  `json:"engine"`
	ServerMajor   int     `json:"server_major,omitempty"`
	Tables        []Table `json:"tables"`
}

type Table struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes,omitempty"`
	Constraints []Constraint `json:"constraints,omitempty"`
}

// QName is the schema-qualified table name.
func (t Table) QName() string { return t.Schema + "." + t.Name }

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
	}
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
