package drift

import (
	"strings"
	"testing"

	"github.com/OriginalDaniel02/dbguard/internal/snapshot"
)

func withObjects() *snapshot.Schema {
	s := base()
	s.Tables[0].Triggers = []snapshot.Trigger{{Name: "audit_trg", Def: "CREATE TRIGGER audit_trg AFTER UPDATE ON public.accounts FOR EACH ROW EXECUTE FUNCTION audit()"}}
	s.Views = []snapshot.View{{Schema: "public", Name: "active_accounts", Def: " SELECT id FROM accounts WHERE plan <> 'free'"}}
	s.Sequences = []snapshot.Sequence{{Schema: "public", Name: "accounts_id_seq", Type: "bigint", Start: 1, Min: 1, Max: 9223372036854775807, Increment: 1}}
	s.Enums = []snapshot.Enum{{Schema: "public", Name: "plan_t", Labels: []string{"free", "pro"}}}
	s.Normalize()
	return s
}

func cloneAll(s *snapshot.Schema) *snapshot.Schema {
	c := clone(s)
	c.Views = append([]snapshot.View(nil), s.Views...)
	c.Sequences = append([]snapshot.Sequence(nil), s.Sequences...)
	c.Enums = append([]snapshot.Enum(nil), s.Enums...)
	for i := range c.Tables {
		c.Tables[i].Triggers = append([]snapshot.Trigger(nil), s.Tables[i].Triggers...)
	}
	return c
}

func TestObjectsIdenticalNoDrift(t *testing.T) {
	if d := Compare(withObjects(), cloneAll(withObjects()), Options{}); len(d) != 0 {
		t.Fatalf("unexpected: %v", d)
	}
}

func TestMissingObjects(t *testing.T) {
	act := cloneAll(withObjects())
	act.Views, act.Sequences, act.Enums = nil, nil, nil
	act.Tables[0].Triggers = nil
	d := Compare(withObjects(), act, Options{})
	for _, k := range []string{ViewMissing, SequenceMissing, EnumMissing, TriggerMissing} {
		if !hasKind(d, k) {
			t.Errorf("missing %s in %v", k, d)
		}
	}
	for _, x := range d {
		if x.Kind == ViewMissing && !strings.Contains(x.String(), "view public.active_accounts is missing") {
			t.Errorf("message: %s", x)
		}
	}
}

func TestExtraObjects(t *testing.T) {
	d := Compare(base(), cloneAll(withObjects()), Options{})
	for _, k := range []string{ViewExtra, SequenceExtra, EnumExtra, TriggerExtra} {
		if !hasKind(d, k) {
			t.Errorf("missing %s in %v", k, d)
		}
	}
}

func TestChangedObjects(t *testing.T) {
	act := cloneAll(withObjects())
	act.Views[0].Def = " SELECT id, email FROM accounts"
	act.Sequences[0].Increment = 10
	act.Enums[0].Labels = []string{"free", "pro", "enterprise"} // value added by hand
	act.Tables[0].Triggers[0].Def = "CREATE TRIGGER audit_trg BEFORE UPDATE ON public.accounts FOR EACH ROW EXECUTE FUNCTION audit()"
	d := Compare(withObjects(), act, Options{})
	got := map[string]Difference{}
	for _, x := range d {
		got[x.Kind] = x
	}
	for _, k := range []string{ViewChanged, SequenceChanged, EnumChanged, TriggerChanged} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s in %v", k, d)
		}
	}
	if e := got[EnumChanged]; e.Expected != "free, pro" || e.Actual != "free, pro, enterprise" {
		t.Errorf("enum detail: %+v", e)
	}
	if !strings.Contains(got[SequenceChanged].Actual, "increment 10") {
		t.Errorf("sequence detail: %+v", got[SequenceChanged])
	}
}

func TestEnumLabelOrderMatters(t *testing.T) {
	act := cloneAll(withObjects())
	act.Enums[0].Labels = []string{"pro", "free"}
	if d := Compare(withObjects(), act, Options{}); !hasKind(d, EnumChanged) {
		t.Fatalf("label order is part of the type: %v", d)
	}
}

func TestIgnoreObjects(t *testing.T) {
	act := cloneAll(withObjects())
	act.Views = append(act.Views, snapshot.View{Schema: "public", Name: "reporting_tmp", Def: "x"})
	act.Sequences[0].Increment = 10
	d := Compare(withObjects(), act, Options{Ignore: []string{"public.reporting_*", "public.accounts_id_seq"}})
	if len(d) != 0 {
		t.Fatalf("ignores must cover views and sequences: %v", d)
	}
}
