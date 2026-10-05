package override

import (
	"testing"

	"github.com/dbguard/dbguard/internal/rules"
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
