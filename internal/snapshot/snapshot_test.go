package snapshot

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTripAndDeterminism(t *testing.T) {
	s := &Schema{Engine: "postgres", Tables: []Table{
		{Schema: "public", Name: "b", Columns: []Column{{Name: "z", Type: "int"}, {Name: "a", Type: "text", NotNull: true}}},
		{Schema: "public", Name: "a"},
	}}
	var one, two bytes.Buffer
	if err := Write(&one, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(bytes.NewReader(one.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tables[0].Name != "a" || got.Tables[1].Columns[0].Name != "a" {
		t.Fatalf("not normalized (sorted): %+v", got.Tables)
	}
	if err := Write(&two, got); err != nil {
		t.Fatal(err)
	}
	if one.String() != two.String() {
		t.Fatal("snapshot output must be byte-for-byte deterministic so it diffs cleanly in git")
	}
}

func TestRejectsBadFiles(t *testing.T) {
	if _, err := Read(strings.NewReader("not json")); err == nil {
		t.Error("garbage must be rejected")
	}
	if _, err := Read(strings.NewReader(`{"format_version": 99, "tables": []}`)); err == nil || !strings.Contains(err.Error(), "format_version") {
		t.Errorf("unknown version must be rejected clearly: %v", err)
	}
}
