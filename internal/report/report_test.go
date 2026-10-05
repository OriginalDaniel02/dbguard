package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dbguard/dbguard/internal/estimate"
	"github.com/dbguard/dbguard/internal/rules"
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
	if err := JSON(&b, sample()); err != nil || !strings.Contains(b.String(), "create-index") {
		t.Fatalf("json: %v %s", err, b.String())
	}
}
