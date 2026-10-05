package review

import "testing"

func TestALOOP02VerdictGrammar(t *testing.T) {
	cases := []struct {
		name, text, value string
		line              int
	}{
		{"approve", "Review\nVERDICT: APPROVE\n", "APPROVE", 2},
		{"revise", "VERDICT: REVISE", "REVISE", 1},
		{"crlf", "Review\r\nVERDICT: APPROVE\r\n\r\n", "APPROVE", 2},
		{"blank-tail", "VERDICT: APPROVE\n \n\t\n", "APPROVE", 1},
		{"embedded", "VERDICT: APPROVE\nAnother line", "MISSING", 0},
		{"quote", "> VERDICT: APPROVE", "MISSING", 0},
		{"fence", "```\nVERDICT: APPROVE\n```", "MISSING", 0},
		{"open-fence", "```example\nVERDICT: APPROVE", "MISSING", 0},
		{"tilde-fence", "~~~\nVERDICT: REVISE", "MISSING", 0},
		{"lower", "VERDICT: approve", "MISSING", 0},
		{"comment", "VERDICT: APPROVE # comment", "MISSING", 0},
		{"prefix", "Final VERDICT: APPROVE", "MISSING", 0},
		{"spaces", "VERDICT: APPROVE ", "MISSING", 0},
		{"indent", " VERDICT: APPROVE", "MISSING", 0},
		{"empty", "\n \n", "MISSING", 0},
		{"closed-example", "```\nVERDICT: REVISE\n```\nVERDICT: APPROVE", "APPROVE", 4},
		{"short-close", "````\n```\nVERDICT: APPROVE", "MISSING", 0},
		{"close-with-info", "```\n```example\nVERDICT: APPROVE", "MISSING", 0},
		{"long-close", "```\n````\nVERDICT: APPROVE", "APPROVE", 3},
		{"other-marker", "```\n~~~\nVERDICT: APPROVE", "MISSING", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ParseVerdict(c.text)
			if v.Value != c.value || v.Line != c.line || v.ParserVersion != 1 {
				t.Fatalf("%+v", v)
			}
		})
	}
}

func TestALOOPTransitions(t *testing.T) {
	s := Snapshot{Status: Running, Phase: Draft, Round: 0, Cap: 1, CurrentTurn: "active"}
	n, err := AfterTurn(s, "", false)
	if err != nil || n.Round != 1 || n.Phase != Critique {
		t.Fatal(n, err)
	}
	n, err = AfterTurn(n, "MISSING", false)
	if err != nil || n.Phase != CritiqueRetry {
		t.Fatal(n, err)
	}
	n, err = AfterTurn(n, "MISSING", false)
	if err != nil || n.Phase != Revise {
		t.Fatal(n, err)
	}
	n, err = AfterTurn(n, "", true)
	if err != nil || n.Status != Impasse || n.Round != 2 {
		t.Fatal(n, err)
	}
	if Attachment(n, false) != "impasse" || Attachment(n, true) != "execute" {
		t.Fatal("cap attachment")
	}
	for _, status := range []Status{Ready, Running, AwaitingAnswer, Handover, Approved, Impasse, Stopped} {
		s.Status = status
		s.CurrentTurn = "active"
		if Attachment(s, false) != "recover" {
			t.Fatal(status)
		}
	}
}

func TestAFINALTransitions(t *testing.T) {
	for _, changed := range []bool{false, true} {
		for _, questions := range []bool{false, true} {
			s := Snapshot{Status: Running, Phase: Closing, Round: 1, Cap: 2, CurrentTurn: "active"}
			n, err := AfterAuxiliary(s, changed, questions)
			if err != nil || n.CurrentTurn != "" {
				t.Fatal(n, err)
			}
			if changed || questions {
				if n.Round != 2 {
					t.Fatal(n)
				}
				if questions {
					if n.Status != AwaitingAnswer || n.Phase != AwaitAnswer {
						t.Fatal(n)
					}
				} else if n.Phase != ConfirmClosing {
					t.Fatal(n)
				}
			} else if n.Phase != CommitFinal || n.Round != 1 {
				t.Fatal(n)
			}
		}
	}
	n, err := AfterAuxiliary(Snapshot{Status: Running, Phase: SecondOpinion, Round: 1, Cap: 1}, true, true)
	if err != nil || n.Phase != CommitFinal || n.Round != 1 {
		t.Fatal(n, err)
	}
	for _, v := range []string{"APPROVE", "REVISE", "MISSING"} {
		n, err = AfterTurn(Snapshot{Status: Running, Phase: ConfirmClosing, Round: 2, Cap: 2}, v, false)
		if err != nil {
			t.Fatal(err)
		}
		want := CommitFinal
		if v == "REVISE" {
			want = Revise
		}
		if v == "MISSING" {
			want = CritiqueRetry
		}
		if n.Phase != want {
			t.Fatal(n)
		}
	}
}
