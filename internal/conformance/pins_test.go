package conformance

import "testing"

func TestPinDetectsChanges(t *testing.T) {
	base := Pinned{ID: "B-01::verb=run", Target: Target{Verb: "run"}, Verdict: "pass", Reason: "checked"}
	good := Verdict{ID: base.ID, Target: base.Target, Verdict: base.Verdict, Reason: base.Reason}
	if err := ComparePin([]Pinned{base}, []Verdict{good}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"add", "remove", "rename", "not-applicable", "reason", "target"} {
		t.Run(kind, func(t *testing.T) {
			rows := []Verdict{good}
			switch kind {
			case "add":
				extra := good
				extra.ID = "other"
				rows = append(rows, extra)
			case "remove":
				rows = nil
			case "rename":
				rows[0].ID = "renamed"
			case "not-applicable":
				rows[0].Verdict = "not_applicable"
			case "reason":
				rows[0].Reason = "changed"
			case "target":
				rows[0].Target.Flag = "--extra"
			}
			if err := ComparePin([]Pinned{base}, rows); err == nil {
				t.Fatal("pin accepted", kind)
			}
		})
	}
}
