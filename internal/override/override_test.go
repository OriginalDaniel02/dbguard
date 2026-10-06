package override

import (
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/rules"
)

func check(t *testing.T, sql string) ([]rules.Finding, []string) {
	t.Helper()
	fs, err := rules.Check(sql, rules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fs, Apply(sql, fs)
}

func TestOverrideAbove(t *testing.T) {
	sql := "SELECT 1;\n-- dbguard:ignore create-index reason: table is write-quiet at 3am deploy window\nCREATE INDEX i ON t (a);\n"
	fs, probs := check(t, sql)
	if len(probs) != 0 || len(fs) != 1 || fs[0].Override == "" {
		t.Fatalf("want acknowledged, got %+v / %v", fs, probs)
	}
	if fs[0].Line != 3 {
		t.Errorf("line = %d, want 3", fs[0].Line)
	}
}

func TestOverrideWithoutReasonRejected(t *testing.T) {
	fs, probs := check(t, "-- dbguard:ignore create-index\nCREATE INDEX i ON t (a);\n")
	if fs[0].Override != "" || len(probs) != 1 {
		t.Fatalf("reasonless ignore must not apply: %+v / %v", fs, probs)
	}
}

func TestOverrideWrongRuleOrUnused(t *testing.T) {
	fs, probs := check(t, "-- dbguard:ignore drop-column reason: x\nCREATE INDEX i ON t (a);\n")
	if fs[0].Override != "" || len(probs) != 1 {
		t.Fatalf("wrong-rule ignore must not apply: %+v / %v", fs, probs)
	}
}

func TestCommentStyles(t *testing.T) {
	cases := map[string]string{
		"xml":  "<!-- dbguard:ignore create-index reason: quiet window -->",
		"yaml": "# dbguard:ignore create-index reason: quiet window",
		"sql":  "-- dbguard:ignore create-index reason: quiet window",
	}
	for name, line := range cases {
		ds := Parse(line + "\n")
		if len(ds) != 1 || ds[0].Rule != "create-index" || ds[0].Reason != "quiet window" {
			t.Errorf("%s: %+v", name, ds)
		}
	}
	if ds := Parse("<!-- dbguard:ignore create-index -->"); len(ds) != 1 || ds[0].Reason != "" {
		t.Errorf("reasonless xml directive must parse with an empty reason: %+v", ds)
	}
}

func TestParseComment(t *testing.T) {
	r, why, ok := ParseComment("dbguard:ignore add-not-null reason: backfilled in V41")
	if !ok || r != "add-not-null" || why != "backfilled in V41" {
		t.Fatalf("%q %q %v", r, why, ok)
	}
	if _, _, ok := ParseComment("just a normal changeset comment"); ok {
		t.Error("ordinary comments are not directives")
	}
	if _, why, ok := ParseComment("dbguard:ignore create-index"); !ok || why != "" {
		t.Error("a reasonless directive parses, but with an empty reason so callers can reject it")
	}
}

func TestXMLOverrideAboveChangeSet(t *testing.T) {
	sql := "<databaseChangeLog>\n<!-- dbguard:ignore create-index reason: quiet window -->\n<changeSet id=\"1\" author=\"a\">\n"
	fs := []rules.Finding{{Rule: rules.CreateIndex, Line: 3, Statement: "CREATE INDEX i ON t (a)"}}
	if probs := Apply(sql, fs); len(probs) != 0 || fs[0].Override != "quiet window" {
		t.Fatalf("%+v %v", fs, probs)
	}
}

func TestDirectiveAboveOtherCommentLines(t *testing.T) {
	// The ignore may be followed by more comment lines (any syntax) before the statement.
	cases := map[string]string{
		"sql":  "-- dbguard:ignore create-index reason: quiet window\n-- see ticket 42\n\nCREATE INDEX i ON t (a);\n",
		"yaml": "# dbguard:ignore create-index reason: quiet window\n# see ticket 42\nCREATE INDEX i ON t (a);\n",
		"java": "// dbguard:ignore create-index reason: quiet window\n// see ticket 42\n/* more */\nCREATE INDEX i ON t (a);\n",
		"xml":  "<!-- dbguard:ignore create-index reason: quiet window -->\n<!-- see ticket 42 -->\nCREATE INDEX i ON t (a);\n",
	}
	for name, sql := range cases {
		fs, err := rules.Check("CREATE INDEX i ON t (a);", rules.Options{})
		if err != nil {
			t.Fatal(err)
		}
		fs[0].Line = strings.Count(strings.TrimRight(sql, "\n"), "\n") + 1 // the statement is the last line
		if probs := Apply(sql, fs); len(probs) != 0 || fs[0].Override != "quiet window" {
			t.Errorf("%s: override not applied: %+v %v", name, fs[0], probs)
		}
	}
	// A real statement between the directive and the finding breaks the link.
	sql := "// dbguard:ignore create-index reason: x\nint a = 1;\nCREATE INDEX i ON t (a);\n"
	fs, _ := rules.Check("CREATE INDEX i ON t (a);", rules.Options{})
	fs[0].Line = 3
	if Apply(sql, fs); fs[0].Override != "" {
		t.Error("an ignore separated from the statement by code must not apply")
	}
}
