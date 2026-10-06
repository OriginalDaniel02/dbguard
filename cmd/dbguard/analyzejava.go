package main

import (
	"os"

	"github.com/OriginalDaniel02/dbguard/internal/javamig"
	"github.com/OriginalDaniel02/dbguard/internal/override"
	"github.com/OriginalDaniel02/dbguard/internal/rules"
)

// analyzeJava checks a Flyway Java migration. It never runs the Java: the SQL string literals
// are extracted from the source (text blocks, "a" + "b" concatenation, String.format
// templates) and analyzed with the same rules as a SQL migration. Findings are reported at
// the Java line of the statement.
func analyzeJava(text string, opts rules.Options, engine string) ([]rules.Finding, []string, error) {
	opts.Tool = "flyway-java"
	ex := javamig.Extract(text)

	valid := rules.Parses
	if engine == "mysql" {
		valid = rules.MySQLParses
	}
	comb := ex.Combine(valid)
	problems := append([]string(nil), comb.Problems...)
	if len(ex.Segments) == 0 {
		problems = append(problems, "line 1: no SQL statements were found in this Java migration, so nothing was analyzed. If it builds its SQL dynamically or reads it from another file, DB Guard cannot see it")
	}

	var found []rules.Finding
	if engine == "mysql" {
		var more []string
		found, more = rules.CheckMySQLLenient(comb.SQL, opts)
		problems = append(problems, more...)
	} else {
		var err error
		if found, err = rules.Check(comb.SQL, opts); err != nil {
			return nil, nil, err
		}
	}
	// Report each finding at the line of its statement in the Java file.
	for i := range found {
		if line := comb.JavaLine(found[i].Line); line > 0 {
			found[i].Line = line
		}
	}
	problems = append(problems, override.Apply(text, found)...)
	return found, problems, nil
}

// readHead returns the first bytes of a file, for cheap content sniffing.
func readHead(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 16384)
	n, _ := f.Read(buf)
	return string(buf[:n])
}
