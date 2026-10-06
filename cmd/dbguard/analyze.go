package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OriginalDaniel02/dbguard/internal/flyway"
	"github.com/OriginalDaniel02/dbguard/internal/liquibase"
	"github.com/OriginalDaniel02/dbguard/internal/override"
	"github.com/OriginalDaniel02/dbguard/internal/rules"
)

// analyze checks one migration file. Plain SQL (Flyway, or Liquibase "formatted
// SQL") goes straight to the rules; Liquibase XML/YAML/JSON changelogs are first
// translated to the equivalent PostgreSQL or MySQL. It returns findings and non-fatal problems.
func analyze(path, text string, opts rules.Options, engine string) ([]rules.Finding, []string, error) {
	if !liquibase.IsStructured(path) {
		if liquibase.Sniff(path, []byte(head(text))) {
			opts.Tool = "liquibase"
		}
		if engine == "mysql" {
			found, problems := rules.CheckMySQLLenient(text, opts)
			return found, append(problems, override.Apply(text, found)...), nil
		}
		found, err := rules.Check(text, opts)
		if err != nil {
			return nil, nil, err
		}
		return found, override.Apply(text, found), nil
	}

	lbEngine := liquibase.EnginePostgres
	if engine == "mysql" {
		lbEngine = liquibase.EngineMySQL
	}
	cl, err := liquibase.ParseFor(path, []byte(text), lbEngine)
	if err != nil {
		return nil, nil, err
	}
	opts.Tool = "liquibase"
	// <property> values act as ${placeholders}; explicit --placeholder values win.
	merged := map[string]string{}
	for k, v := range cl.Properties {
		merged[k] = v
	}
	for k, v := range opts.Placeholders {
		merged[k] = v
	}
	opts.Placeholders = merged

	var comb liquibase.Combined
	var found []rules.Finding
	if engine == "mysql" {
		comb = cl.Combine(rules.MySQLParses)
		var mysqlProblems []string
		found, mysqlProblems = rules.CheckMySQLLenient(comb.SQL, opts)
		comb.Problems = append(comb.Problems, mysqlProblems...)
	} else {
		comb = cl.Combine(rules.Parses)
		if found, err = rules.Check(comb.SQL, opts); err != nil {
			return nil, nil, err
		}
	}
	problems := append([]string(nil), comb.Problems...)

	// Findings are reported at the changeSet's line in the original file.
	// A changeSet's own comment may acknowledge a finding: "dbguard:ignore <rule> reason: ...".
	used := map[int]bool{}
	for i := range found {
		idx := comb.SpanAt(found[i].Line)
		if idx < 0 {
			continue
		}
		cs := cl.Changesets[idx]
		found[i].Line = cs.Line
		if rule, reason, ok := override.ParseComment(cs.Comment); ok && reason != "" && rule == found[i].Rule {
			found[i].Override = reason
			used[idx] = true
		}
	}
	for idx, cs := range cl.Changesets {
		rule, reason, ok := override.ParseComment(cs.Comment)
		switch {
		case !ok:
		case reason == "":
			problems = append(problems, fmt.Sprintf("line %d: changeSet %s: dbguard:ignore %s needs a 'reason: ...'", cs.Line, cs.ID, rule))
		case !used[idx]:
			problems = append(problems, fmt.Sprintf("line %d: changeSet %s: dbguard:ignore %s matched no finding", cs.Line, cs.ID, rule))
		}
	}
	// Comments above a changeSet (<!-- --> in XML, # in YAML) work like SQL ones.
	problems = append(problems, override.Apply(text, found)...)
	return found, problems, nil
}

func head(s string) string {
	if len(s) > 16384 {
		return s[:16384]
	}
	return s
}

// collect expands paths into migration files. Explicit .sql files are always
// checked (so CI can pass exactly the files a change touched). XML/YAML/JSON files,
// explicit or found in a directory, are checked only if they are Liquibase
// changelogs, recognized by content. Directories are searched for Flyway-named
// SQL files and Liquibase changelogs.
func collect(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch {
			case flyway.IsMigration(d.Name()):
				out = append(out, path)
			case path == p && !liquibase.IsStructured(path):
				out = append(out, path) // an explicit SQL file is always checked
			case liquibase.IsStructured(path) || strings.EqualFold(filepath.Ext(path), ".sql"):
				// XML/YAML/JSON (explicit or found by walking) are only checked if they
				// really are Liquibase changelogs, so a stray config.json is ignored.
				f, err := os.Open(path)
				if err != nil {
					return nil
				}
				buf := make([]byte, 16384)
				n, _ := f.Read(buf)
				f.Close()
				if liquibase.Sniff(path, buf[:n]) {
					out = append(out, path)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
