package prompt

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
)

type Purpose string

const (
	Draft          Purpose = "draft"
	Revision       Purpose = "revision"
	ApplyDirective Purpose = "directive"
	Critique       Purpose = "critique"
	Reminder       Purpose = "reminder"
	Advisory       Purpose = "advisory"
	Closing        Purpose = "closing"
	Confirmation   Purpose = "confirmation"
	AnswerRecord   Purpose = "answer_record"
)

type Directive struct {
	Question string
	Answer   string
	ID       string
}
type Request struct {
	Purpose                      Purpose
	Role, Provider, Backend      string
	Round                        int
	AttemptID, PreviousAttemptID string
	Constraints                  string
	ContextPath                  string
	Directives                   []Directive
	Rubric                       string
	SecondOpinionPath            string
	CriticOutputPath             string
}
type Rendered struct {
	Text             []byte
	TemplateName     string
	TemplateHash     string
	RenderedHash     string
	SourceHashes     map[string]string
	SourceBundleHash string
	AttemptID        string
}
type Values struct{ Round, Human, Context, Constraints, SecondOpinion string }

const PromptLimit = 32 << 20

// RenderTemplate scans only source bytes. Inserted text is never rescanned.
func RenderTemplate(source []byte, values Values) ([]byte, error) {
	if !utf8.Valid(source) || len(source) > PromptLimit {
		return nil, fmt.Errorf("Invalid template bytes")
	}
	fields := map[string]string{"ROUND": values.Round, "HUMAN": values.Human, "CONTEXT": values.Context, "CONSTRAINTS": values.Constraints, "SECOND_OPINION": values.SecondOpinion}
	var out bytes.Buffer
	remaining := string(source)
	for {
		begin := strings.Index(remaining, "{{")
		close := strings.Index(remaining, "}}")
		if begin < 0 {
			if close >= 0 {
				return nil, fmt.Errorf("Unmatched template close marker")
			}
			out.WriteString(remaining)
			break
		}
		if close >= 0 && close < begin {
			return nil, fmt.Errorf("Unmatched template close marker")
		}
		out.WriteString(remaining[:begin])
		end := strings.Index(remaining[begin+2:], "}}")
		if end < 0 {
			return nil, fmt.Errorf("Unresolved template marker")
		}
		end += begin + 2
		key := remaining[begin+2 : end]
		value, known := fields[key]
		if !known || key == "ROUND" && value == "" {
			return nil, fmt.Errorf("Unknown or unresolved template marker %q", key)
		}
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("Invalid substitution bytes")
		}
		out.WriteString(value)
		if out.Len() > PromptLimit {
			return nil, fmt.Errorf("Prompt exceeds its byte limit")
		}
		remaining = remaining[end+2:]
	}
	if out.Len() > PromptLimit {
		return nil, fmt.Errorf("Prompt exceeds its byte limit")
	}
	return out.Bytes(), nil
}
func purposeTemplate(p Purpose) (string, string, error) {
	switch p {
	case Draft:
		return "planner-init.md", "planner", nil
	case Revision:
		return "planner-revise.md", "planner", nil
	case ApplyDirective:
		return "directive-apply.md", "planner", nil
	case Closing:
		return "closing-pass.md", "planner", nil
	case AnswerRecord:
		return "answer-record.md", "planner", nil
	case Critique:
		return "critic.md", "critic", nil
	case Reminder:
		return "verdict-reminder.md", "critic", nil
	case Advisory:
		return "advisory-review.md", "critic", nil
	case Confirmation:
		return "confirm-closing.md", "critic", nil
	}
	return "", "", fmt.Errorf("Unknown prompt purpose")
}
func attemptIDValid(id string) bool {
	if len(id) != 26 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}
func directiveBlock(directives []Directive) (string, error) {
	var out strings.Builder
	for _, d := range directives {
		if !utf8.ValidString(d.Question) || !utf8.ValidString(d.Answer) || !utf8.ValidString(d.ID) {
			return "", fmt.Errorf("Invalid directive text")
		}
		out.WriteString("\n\n--- Archived user directive ")
		out.WriteString(strconv.Quote(d.ID))
		out.WriteString(" (binding) ---\nArchived question (exact bytes):\n")
		out.WriteString(d.Question)
		out.WriteString("\n--- User answer (exact bytes) ---\n")
		out.WriteString(d.Answer)
		out.WriteString("\n--- End archived user directive ---")
	}
	return out.String(), nil
}
func pathBlock(path, heading, body string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.ContainsRune(path, 0) || !utf8.ValidString(path) || !filepath.IsAbs(path) {
		return "", fmt.Errorf("Invalid reference path")
	}
	return "\n\n" + heading + " " + strconv.Quote(path) + ". " + body, nil
}
func Render(q Request) (Rendered, error) {
	result := Rendered{SourceHashes: make(map[string]string), AttemptID: q.AttemptID}
	name, role, err := purposeTemplate(q.Purpose)
	if err != nil {
		return result, err
	}
	if q.Role != role || q.Provider != "claude" && q.Provider != "codex" || q.Backend != "cli" && q.Backend != "gashki" {
		return result, fmt.Errorf("Invalid prompt role, provider, or backend")
	}
	if q.Round < 0 || q.Round > 9999 || !attemptIDValid(q.AttemptID) {
		return result, fmt.Errorf("Invalid prompt round or attempt identity")
	}
	if q.Purpose == Reminder && (!attemptIDValid(q.PreviousAttemptID) || q.PreviousAttemptID == q.AttemptID) {
		return result, fmt.Errorf("Verdict reminder requires a new attempt identity")
	}
	if q.Rubric != "" && q.Rubric != "security" && q.Rubric != "data" && q.Rubric != "decision-memo" && q.Rubric != "plan-spec" {
		return result, fmt.Errorf("Unknown critic rubric")
	}
	load := func(asset string) ([]byte, error) {
		b, err := Template(asset)
		if err != nil {
			return nil, err
		}
		result.SourceHashes[asset] = contract.HashBytes(b)
		return b, nil
	}
	base, err := load(name)
	if err != nil {
		return result, err
	}
	result.TemplateName = name
	result.TemplateHash = contract.HashBytes(base)
	human, err := directiveBlock(q.Directives)
	if err != nil {
		return result, err
	}
	contextBlock, err := pathBlock(q.ContextPath, "A read-only reference codebase is available at", "Ground the review in its actual code. Do not modify it; writes stay in the workspace.")
	if err != nil {
		return result, err
	}
	second, err := pathBlock(q.SecondOpinionPath, "Read the advisory review at", "Its suggestions do not change the accepted critic verdict.")
	if err != nil {
		return result, err
	}
	constraints := ""
	if q.Constraints != "" {
		constraints = "\n\n--- CONSTRAINTS.md (binding) ---\n" + q.Constraints + "\n--- End CONSTRAINTS.md ---"
	}
	text, err := RenderTemplate(base, Values{fmt.Sprintf("r%02d", q.Round), human, contextBlock, constraints, second})
	if err != nil {
		return result, err
	}
	var out bytes.Buffer
	out.Write(text)
	// The short baseline draft and closing templates have no HUMAN marker.
	if !bytes.Contains(base, []byte("{{HUMAN}}")) {
		out.WriteString(human)
	}
	appendAsset := func(asset string) error {
		b, err := load(asset)
		if err != nil {
			return err
		}
		if !utf8.Valid(b) || bytes.Contains(b, []byte("{{")) || bytes.Contains(b, []byte("}}")) {
			return fmt.Errorf("Invalid prompt asset")
		}
		out.WriteString("\n\n")
		out.Write(b)
		return nil
	}
	if role == "planner" && q.Purpose != AnswerRecord {
		if err := appendAsset("questions-" + q.Backend + ".md"); err != nil {
			return result, err
		}
	}
	if q.Purpose == AnswerRecord {
		out.WriteString("\n\nThis turn records an answer only. Keep the archived question and answer unchanged. The controller will apply the directive in a separate turn.")
	}
	if role == "planner" {
		if q.Purpose != AnswerRecord {
			if err := appendAsset("planner-boundary.md"); err != nil {
				return result, err
			}
		}
	} else {
		if err := appendAsset("critic-boundary.md"); err != nil {
			return result, err
		}
		if q.Rubric != "" {
			if err := appendAsset("profiles/" + q.Rubric + ".md"); err != nil {
				return result, err
			}
		}
		if q.CriticOutputPath != "" {
			if q.Backend != "gashki" {
				return result, fmt.Errorf("Registered critic output applies only to Gashki")
			}
			sink, err := pathBlock(q.CriticOutputPath, "Write this critique only to the registered output path", "Keep its verdict as the final non-empty line. Do not write other workspace files.")
			if err != nil {
				return result, err
			}
			out.WriteString(sink)
		}
	}
	if q.Purpose == Reminder {
		out.WriteString("\n\nPrior attempt: " + q.PreviousAttemptID + ". New attempt: " + q.AttemptID + ".")
	}
	rule, err := load("common-review-rule.md")
	if err != nil {
		return result, err
	}
	if bytes.Contains(base, bytes.TrimSpace(rule)) {
		return result, fmt.Errorf("Base template already contains the common review rule")
	}
	if err := appendAsset("common-review-rule.md"); err != nil {
		return result, err
	}
	if out.Len() > PromptLimit || !utf8.Valid(out.Bytes()) {
		return result, fmt.Errorf("Rendered prompt exceeds its limit or has invalid UTF-8")
	}
	result.Text = append([]byte(nil), out.Bytes()...)
	result.RenderedHash = contract.HashBytes(result.Text)
	bundle, err := contract.Canonical(result.SourceHashes)
	if err != nil {
		return result, err
	}
	result.SourceBundleHash = contract.HashBytes(bundle)
	return result, nil
}
