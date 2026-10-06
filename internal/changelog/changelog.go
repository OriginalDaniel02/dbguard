// Package changelog turns a history of schema snapshots (as written by
// `dbguard drift --save-dir`) into a searchable per-table change log: for every
// pair of consecutive snapshots of an environment, what changed between them.
// It answers "when did this column's type actually change?".
package changelog

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OriginalDaniel02/dbguard/internal/drift"
	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

// TimeLayout is the file name layout `drift --save-dir` uses: 20261006T060000Z.json.
const TimeLayout = "20060102T150405Z"

// Snapshot is one saved snapshot of an environment.
type Snapshot struct {
	Env    string
	Time   time.Time
	Path   string
	Schema *snapshot.Schema
}

// Entry is one change detected between two consecutive snapshots of an environment.
// The exact time of the change is unknown: it happened between PrevTime and Time.
type Entry struct {
	Time     time.Time `json:"time"`      // when the snapshot that revealed the change was taken
	PrevTime time.Time `json:"prev_time"` // the previous snapshot
	Env      string    `json:"env"`
	Table    string    `json:"table"` // object name; views, sequences and enums use their own name
	Object   string    `json:"object,omitempty"`
	Kind     string    `json:"kind"`   // a drift kind, e.g. column-type-changed
	Action   string    `json:"action"` // added | dropped | changed
	Change   string    `json:"change"` // human readable
	Before   string    `json:"before,omitempty"`
	After    string    `json:"after,omitempty"`
}

// LoadDir reads <dir>/<env>/<timestamp>.json files. JSON files placed directly in dir
// belong to the environment "default". Files that are not snapshots are skipped and
// reported in warnings, so one stray file does not hide the history.
func LoadDir(dir string) (snaps []Snapshot, warnings []string, err error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, nil, err
	}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".json") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		env := "default"
		if parent := filepath.Dir(rel); parent != "." {
			env = filepath.ToSlash(parent)
		}
		stamp := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		t, perr := time.Parse(TimeLayout, stamp)
		if perr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: skipped, file name is not a %s timestamp", rel, TimeLayout))
			return nil
		}
		f, oerr := os.Open(p)
		if oerr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: skipped (%v)", rel, oerr))
			return nil
		}
		defer f.Close()
		s, rerr := snapshot.Read(f)
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: skipped (%v)", rel, rerr))
			return nil
		}
		snaps = append(snaps, Snapshot{Env: env, Time: t.UTC(), Path: p, Schema: s})
		return nil
	})
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].Env != snaps[j].Env {
			return snaps[i].Env < snaps[j].Env
		}
		return snaps[i].Time.Before(snaps[j].Time)
	})
	return snaps, warnings, err
}

// Build diffs every consecutive pair of snapshots per environment. Entries come out
// newest first.
func Build(snaps []Snapshot, opts drift.Options) []Entry {
	byEnv := map[string][]Snapshot{}
	for _, s := range snaps {
		byEnv[s.Env] = append(byEnv[s.Env], s)
	}
	var out []Entry
	for env, list := range byEnv {
		sort.Slice(list, func(i, j int) bool { return list[i].Time.Before(list[j].Time) })
		for i := 1; i < len(list); i++ {
			prev, next := list[i-1], list[i]
			if prev.Schema.Engine != "" && next.Schema.Engine != "" && prev.Schema.Engine != next.Schema.Engine {
				continue // an environment that switched engines has no meaningful diff
			}
			// Compare treats "expected" as the baseline: -extra = in next only, -missing = in prev only.
			for _, d := range drift.Compare(prev.Schema, next.Schema, opts) {
				out = append(out, entryFor(env, prev.Time, next.Time, d))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		if out[i].Env != out[j].Env {
			return out[i].Env < out[j].Env
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Object < out[j].Object
	})
	return out
}

func entryFor(env string, prev, next time.Time, d drift.Difference) Entry {
	e := Entry{Time: next, PrevTime: prev, Env: env, Table: d.Table, Object: d.Object, Kind: d.Kind, Before: d.Expected, After: d.Actual}
	noun := kindNoun(d.Kind)
	name := noun
	if d.Object != "" {
		name = noun + " " + d.Object
	}
	switch {
	case strings.HasSuffix(d.Kind, "-extra"):
		e.Action = "added"
		e.Before, e.After = "", d.Actual
		e.Change = name + " added"
		if d.Actual != "" {
			e.Change += " (" + d.Actual + ")"
		}
	case strings.HasSuffix(d.Kind, "-missing"):
		e.Action = "dropped"
		e.Before, e.After = d.Expected, ""
		e.Change = name + " dropped"
		if d.Expected != "" {
			e.Change += " (was " + d.Expected + ")"
		}
	default:
		e.Action = "changed"
		e.Change = name + " " + changedVerb(d.Kind) + ": " + d.Expected + " -> " + d.Actual
	}
	return e
}

// kindNoun is "column" for column-type-changed, "table" for table-extra, ...
func kindNoun(kind string) string {
	if i := strings.Index(kind, "-"); i > 0 {
		return kind[:i]
	}
	return kind
}

func changedVerb(kind string) string {
	switch kind {
	case drift.ColumnTypeChanged:
		return "type changed"
	case drift.ColumnNullability:
		return "nullability changed"
	case drift.ColumnDefault:
		return "default changed"
	}
	return "changed"
}

// Query filters entries. Zero values match everything.
type Query struct {
	Env    string    // exact
	Table  string    // glob, matched against the object name (e.g. "orders", "public.*")
	Object string    // glob against the column / index / constraint name
	Kind   string    // exact drift kind, or a noun prefix such as "column"
	Action string    // added | dropped | changed
	Text   string    // case-insensitive substring of the change, before or after
	Since  time.Time // inclusive
	Until  time.Time // inclusive
}

// Match reports whether an entry satisfies the query.
func (q Query) Match(e Entry) bool {
	if q.Env != "" && e.Env != q.Env {
		return false
	}
	if q.Table != "" && !globOrContains(q.Table, e.Table) {
		return false
	}
	if q.Object != "" && !globOrContains(q.Object, e.Object) {
		return false
	}
	if q.Kind != "" && e.Kind != q.Kind && kindNoun(e.Kind) != q.Kind {
		return false
	}
	if q.Action != "" && e.Action != q.Action {
		return false
	}
	if !q.Since.IsZero() && e.Time.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && e.Time.After(q.Until) {
		return false
	}
	if q.Text != "" {
		hay := strings.ToLower(e.Table + " " + e.Object + " " + e.Change + " " + e.Before + " " + e.After)
		if !strings.Contains(hay, strings.ToLower(q.Text)) {
			return false
		}
	}
	return true
}

// globOrContains: a pattern with glob characters is matched as a glob, a plain word
// matches the name exactly.
func globOrContains(pattern, name string) bool {
	if strings.ContainsAny(pattern, "*?[") {
		ok, _ := path.Match(pattern, name)
		return ok
	}
	return pattern == name || strings.HasSuffix(name, "."+pattern)
}

// Filter returns the entries matching q, preserving order.
func Filter(entries []Entry, q Query) []Entry {
	var out []Entry
	for _, e := range entries {
		if q.Match(e) {
			out = append(out, e)
		}
	}
	return out
}
