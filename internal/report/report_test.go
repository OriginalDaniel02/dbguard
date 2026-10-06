package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/estimate"
	"github.com/OriginalDaniel02/dbguard/internal/rules"
)

func sample() []File {
	r := estimate.For(estimate.IndexBuild, 14_000_000)
	return []File{{
		Path: "db/migration/V2__x.sql",
		Findings: []rules.Finding{
			{Rule: rules.CreateIndex, Risk: rules.High, Table: "public.transactions", Rows: 14_000_000, Line: 4,
				Lock: "SHARE lock", Message: "this will lock public.transactions (14.0M rows) for approximately 18-70s",
				Alternative: "CREATE INDEX CONCURRENTLY", Estimate: &r},
			{Rule: rules.AddNotNull, Risk: rules.MediumHigh, Table: "public.transactions", Line: 7,
				Lock: "scan", Message: "m", Alternative: "alt", Override: "write-quiet table"},
		},
		Problems: []string{"line 9: dbguard:ignore x matched no finding"},
	}}
}

func TestMarkdownComment(t *testing.T) {
	var b bytes.Buffer
	Markdown(&b, sample(), rules.MediumHigh)
	out := b.String()
	for _, want := range []string{
		Marker, "1 risky migration change(s) block this merge", "public.transactions",
		"18-70s", "CREATE INDEX CONCURRENTLY", "dbguard:ignore create-index reason:",
		"acknowledged", "write-quiet table", "matched no finding",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n%s", want, out)
		}
	}
}

func TestMarkdownAllClear(t *testing.T) {
	var b bytes.Buffer
	Markdown(&b, nil, rules.MediumHigh)
	if !strings.Contains(b.String(), "no blocking migration risks") || !strings.Contains(b.String(), Marker) {
		t.Fatalf("unexpected: %s", b.String())
	}
}

func TestBlocking(t *testing.T) {
	hi := rules.Finding{Risk: rules.High}
	if !Blocking(hi, rules.MediumHigh) {
		t.Error("high should block at medium-high")
	}
	if Blocking(rules.Finding{Risk: rules.Low}, rules.MediumHigh) {
		t.Error("low should not block at medium-high")
	}
	hi.Override = "reason"
	if Blocking(hi, rules.MediumHigh) {
		t.Error("acknowledged finding must not block")
	}
}

func TestTextAndJSON(t *testing.T) {
	var b bytes.Buffer
	Text(&b, sample(), rules.MediumHigh)
	if !strings.Contains(b.String(), "1 blocking finding(s)") || !strings.Contains(b.String(), "ACKNOWLEDGED") {
		t.Fatalf("text: %s", b.String())
	}
	b.Reset()
	if err := JSON(&b, sample(), rules.MediumHigh); err != nil || !strings.Contains(b.String(), "create-index") {
		t.Fatalf("json: %v %s", err, b.String())
	}
}

// The JSON is a public contract (the VS Code extension and CI scripts read it): lock it down.
func TestJSONContract(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample(), rules.MediumHigh); err != nil {
		t.Fatal(err)
	}
	var files []map[string]any
	if err := json.Unmarshal(b.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	f := files[0]
	if f["path"] != "db/migration/V2__x.sql" || len(f["problems"].([]any)) != 1 {
		t.Fatalf("file: %v", f)
	}
	first := f["findings"].([]any)[0].(map[string]any)
	for _, key := range []string{"rule", "risk", "table", "rows", "line", "statement", "lock", "message", "alternative", "estimate", "blocking"} {
		if _, ok := first[key]; !ok {
			t.Errorf("missing key %q in %v", key, first)
		}
	}
	if first["risk"] != "high" || first["blocking"] != true || first["rule"] != "create-index" || first["line"] != float64(4) {
		t.Errorf("values: %v", first)
	}
	est := first["estimate"].(map[string]any)
	if est["text"] == "" || est["min_seconds"].(float64) <= 0 || est["max_seconds"].(float64) < est["min_seconds"].(float64) {
		t.Errorf("estimate: %v", est)
	}
	second := f["findings"].([]any)[1].(map[string]any)
	if second["blocking"] != false || second["override"] != "write-quiet table" {
		t.Errorf("an acknowledged finding never blocks: %v", second)
	}
	if _, ok := second["estimate"]; ok {
		t.Error("estimate is omitted when there is none")
	}
	// Keys must not leak Go field names.
	if strings.Contains(b.String(), `"Rule"`) || strings.Contains(b.String(), `"Risk"`) {
		t.Error("JSON must use the documented lowercase names")
	}
	// Empty lists are [] and never null.
	b.Reset()
	JSON(&b, []File{{Path: "a.sql"}}, rules.High)
	if !strings.Contains(b.String(), `"findings": []`) || !strings.Contains(b.String(), `"problems": []`) {
		t.Errorf("empty lists must be []: %s", b.String())
	}
}

func TestFindingRoundTripsThroughJSON(t *testing.T) {
	var b bytes.Buffer
	JSON(&b, sample(), rules.MediumHigh)
	var back []File
	if err := json.Unmarshal(b.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	g := back[0].Findings[0]
	if g.Risk != rules.High || g.Estimate == nil || g.Estimate.Max <= g.Estimate.Min || g.Rule != rules.CreateIndex {
		t.Fatalf("round trip lost data: %+v", g)
	}
}
