package estimate

import "testing"

func TestRangeString(t *testing.T) {
	cases := []struct {
		k    *Kind
		rows int64
		want string
	}{
		{Rewrite, 14_000_000, "1-6 min"},
		{IndexBuild, 14_000_000, "1-6 min"},
		{Rewrite, 100_000_000, "6-42 min"},
		{Scan, 10, "~1s"},
	}
	for _, c := range cases {
		if got := For(c.k, c.rows).String(); got != c.want {
			t.Errorf("%s %d: got %q, want %q", c.k.Name, c.rows, got, c.want)
		}
	}
}

func TestHumanRows(t *testing.T) {
	if got := HumanRows(14_200_000); got != "14.2M" {
		t.Errorf("got %q", got)
	}
}
