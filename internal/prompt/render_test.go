package prompt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
)

type capture struct {
	Purpose          Purpose `json:"purpose"`
	Provider         string  `json:"provider"`
	Backend          string  `json:"backend"`
	Rubric           string  `json:"rubric"`
	Template         string  `json:"template"`
	TemplateHash     string  `json:"template_hash"`
	RenderedHash     string  `json:"rendered_hash"`
	SourceBundleHash string  `json:"source_bundle_hash"`
	Text             string  `json:"text"`
}

func matrix(t *testing.T) []capture {
	t.Helper()
	var records []capture
	rule, err := Template("common-review-rule.md")
	if err != nil {
		t.Fatal(err)
	}
	question := "  Which option?\n1. Keep {{ROUND}} literal.\n2. Change it.\nRecommendation: 1.  \n"
	answer := " \tChoose 1.\nKeep {{UNKNOWN}} and }} unchanged.\nRésumé: café.  \n\n"
	for _, purpose := range []Purpose{Draft, Revision, ApplyDirective, Critique, Reminder, Advisory, Closing, Confirmation, AnswerRecord} {
		for _, provider := range []string{"claude", "codex"} {
			for _, backend := range []string{"cli", "gashki"} {
				for _, rubric := range []string{"", "security", "data", "decision-memo", "plan-spec"} {
					name, role, _ := purposeTemplate(purpose)
					t.Run(fmt.Sprintf("A-PROMPT-01/%s/%s/%s/%s", purpose, provider, backend, rubric), func(t *testing.T) {
						q := Request{Purpose: purpose, Role: role, Provider: provider, Backend: backend, Round: 3, AttemptID: strings.Repeat("a", 26), PreviousAttemptID: strings.Repeat("b", 26), Rubric: rubric, Constraints: "Preserve the explicit requirement. {{CONTEXT}}", ContextPath: "/owned/reference", Directives: []Directive{{Question: question, Answer: answer, ID: "directive-1"}}}
						if purpose == Closing {
							q.SecondOpinionPath = "/owned/workspace/rounds/second-opinion.md"
						}
						if role == "critic" && backend == "gashki" {
							q.CriticOutputPath = "/owned/workspace/rounds/r03.critique.md"
						}
						r, err := Render(q)
						if err != nil {
							t.Fatal(err)
						}
						if bytes.Count(r.Text, bytes.TrimSpace(rule)) != 1 {
							t.Fatal("Common review rule count")
						}
						if !bytes.Contains(r.Text, []byte(question)) || !bytes.Contains(r.Text, []byte(answer)) {
							t.Fatal("Exact directive bytes were lost")
						}
						if !bytes.Contains(r.Text, []byte(q.Constraints)) {
							t.Fatal("Constraint bytes were lost")
						}
						if r.TemplateName != name || r.RenderedHash != contract.HashBytes(r.Text) || r.TemplateHash == r.RenderedHash {
							t.Fatal("Prompt hash binding")
						}
						base, _ := Template(name)
						if r.TemplateHash != contract.HashBytes(base) {
							t.Fatal("Template hash binding")
						}
						for _, candidate := range []string{"security", "data", "decision-memo", "plan-spec"} {
							fragment, _ := Template("profiles/" + candidate + ".md")
							_, recorded := r.SourceHashes["profiles/"+candidate+".md"]
							want := role == "critic" && rubric == candidate
							if recorded != want || bytes.Contains(r.Text, fragment) != want {
								t.Fatal("Rubric role boundary", candidate)
							}
						}
						_, questionPolicy := r.SourceHashes["questions-"+backend+".md"]
						if questionPolicy != (role == "planner" && purpose != AnswerRecord) {
							t.Fatal("Question instruction boundary")
						}
						if purpose == Advisory && !strings.Contains(string(r.Text), "advisory") {
							t.Fatal("Advisory scope missing")
						}
						if purpose == Reminder && (!strings.Contains(string(r.Text), q.PreviousAttemptID) || !strings.Contains(string(r.Text), q.AttemptID)) {
							t.Fatal("Attempt binding")
						}
						if strings.Contains(string(r.Text), "write a response file") {
							t.Fatal("Planner response file request")
						}
						records = append(records, capture{purpose, provider, backend, rubric, r.TemplateName, r.TemplateHash, r.RenderedHash, r.SourceBundleHash, string(r.Text)})
					})
				}
			}
		}
	}
	return records
}

func TestCapturedPrompts(t *testing.T) {
	got, err := json.MarshalIndent(matrix(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile(filepath.Join("testdata", "captured-prompts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Captured prompt fixture differs; review source and rendered hashes before updating it")
	}
	t.Logf("F-PURE captured prompts: 180; SHA256=%s", contract.HashBytes(got))
}

func TestLiteralAndRefusalBeforeDelivery(t *testing.T) {
	for _, source := range []string{"{{UNKNOWN}}", "{{ROUND}}", "before {{HUMAN", "before }} after", "{{HUMAN {{ROUND}}"} {
		t.Run("A-PROMPT-02/refuse/"+source, func(t *testing.T) {
			deliveries := 0
			if _, err := RenderTemplate([]byte(source), Values{}); err == nil {
				deliveries++
			}
			if deliveries != 0 {
				t.Fatal("Invalid template was delivered")
			}
		})
	}
	user := "  {{UNKNOWN}} {{ROUND}} }}\n{{HUMAN}}\n"
	got, err := RenderTemplate([]byte("prefix{{HUMAN}}suffix"), Values{Human: user})
	if err != nil || string(got) != "prefix"+user+"suffix" {
		t.Fatal("User bytes were rescanned", err)
	}
	for _, source := range [][]byte{{0xff}, bytes.Repeat([]byte("x"), PromptLimit+1)} {
		if _, err := RenderTemplate(source, Values{}); err == nil {
			t.Fatal("Invalid source accepted")
		}
	}
	if _, err := RenderTemplate([]byte("{{HUMAN}}"), Values{Human: string([]byte{0xff})}); err == nil {
		t.Fatal("Invalid replacement accepted")
	}
}

func TestPromptRequestRefusals(t *testing.T) {
	base := Request{Purpose: Reminder, Role: "critic", Provider: "claude", Backend: "cli", Round: 1, AttemptID: strings.Repeat("a", 26), PreviousAttemptID: strings.Repeat("b", 26)}
	for name, change := range map[string]func(*Request){
		"purpose":           func(q *Request) { q.Purpose = "unknown" },
		"role":              func(q *Request) { q.Role = "planner" },
		"provider":          func(q *Request) { q.Provider = "unknown" },
		"backend":           func(q *Request) { q.Backend = "unknown" },
		"round":             func(q *Request) { q.Round = -1 },
		"round-upper":       func(q *Request) { q.Round = 10000 },
		"attempt":           func(q *Request) { q.AttemptID = "short" },
		"old-attempt":       func(q *Request) { q.PreviousAttemptID = "" },
		"same-attempt":      func(q *Request) { q.PreviousAttemptID = q.AttemptID },
		"rubric":            func(q *Request) { q.Rubric = "unknown" },
		"relative-context":  func(q *Request) { q.ContextPath = "reference" },
		"nul-context":       func(q *Request) { q.ContextPath = "/reference\x00" },
		"invalid-directive": func(q *Request) { q.Directives = []Directive{{Answer: string([]byte{0xff})}} },
		"direct-sink":       func(q *Request) { q.CriticOutputPath = "/owned/output" },
	} {
		t.Run(name, func(t *testing.T) {
			q := base
			change(&q)
			if _, err := Render(q); err == nil {
				t.Fatal("Invalid request accepted")
			}
		})
	}
}

func TestBaselineRuleAndQuestions(t *testing.T) {
	rule, err := Template("common-review-rule.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "Resolve technical design objections together. Revise proposed choices without treating them as approved for implementation. Ask the user during review only when progress requires a change to an explicit user requirement or a user preference that the available evidence cannot settle. Keep future execution approvals as gates in the plan; do not stop this review to request permission for future execution. Preserve explicit requirements and binding constraints. Do not treat a proposed design choice as a settled user requirement. A request for a user answer in a critique is not binding by itself; apply this rule before forwarding it.\n"
	if string(rule) != want {
		t.Fatal("Common rule differs from the pinned Bash baseline")
	}
	for _, backend := range []string{"cli", "gashki"} {
		b, err := Template("questions-" + backend + ".md")
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range []string{"recommendation", "SPEC.md", "QUESTIONS.md", "whitespace", "heading", "archived"} {
			if !strings.Contains(strings.ToLower(string(b)), strings.ToLower(word)) {
				t.Fatal("Missing question policy", backend, word)
			}
		}
	}
}
