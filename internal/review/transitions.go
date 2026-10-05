package review

import (
	"fmt"
	"strings"
)

const (
	Prepare        Phase = "prepare"
	Draft          Phase = "draft"
	ApplyDirective Phase = "apply_directive"
	Critique       Phase = "critique"
	CritiqueRetry  Phase = "critique_retry"
	Revise         Phase = "revise"
	AwaitAnswer    Phase = "await_answer"
	CommitFinal    Phase = "commit_final"
	Cleanup        Phase = "cleanup"
)

// Verdict parsing changes only the parsing view. Callers retain the original
// bytes and bind the returned line to their exact artifact hash.
type Verdict struct {
	Value         string `json:"value"`
	Hash          string `json:"hash"`
	Line          int    `json:"line"`
	ParserVersion int    `json:"parser_version"`
}

func ParseVerdict(text string) Verdict {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	v := Verdict{Value: "MISSING", ParserVersion: 1}
	if end == 0 {
		return v
	}
	// A final example inside an unclosed fence is not a verdict. Closing fence
	// lines also fail the exact final-line rule below.
	var fence byte
	fenceLength := 0
	for _, line := range lines[:end-1] {
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 {
			continue
		}
		if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
			continue
		}
		marker := trimmed[0]
		n := 0
		for n < len(trimmed) && trimmed[n] == marker {
			n++
		}
		if n < 3 {
			continue
		}
		if fence == 0 {
			if marker == '`' && strings.ContainsRune(trimmed[n:], '`') {
				continue
			}
			fence, fenceLength = marker, n
		} else if marker == fence && n >= fenceLength && strings.TrimSpace(trimmed[n:]) == "" {
			fence, fenceLength = 0, 0
		}
	}
	if fence != 0 {
		return v
	}
	switch lines[end-1] {
	case "VERDICT: APPROVE":
		v.Value = "APPROVE"
	case "VERDICT: REVISE":
		v.Value = "REVISE"
	default:
		return v
	}
	v.Line = end
	return v
}

// AfterTurn is the ordinary review state machine. Effects, archive writes and
// evidence validation belong to the owner. A cap never skips a required final
// revision or converts unreviewed bytes into approval.
func AfterTurn(s Snapshot, verdict string, questions bool) (Snapshot, error) {
	next := s
	next.Status = Ready
	next.CurrentTurn = ""
	switch s.Phase {
	case Draft:
		next.Round = 1
		next.Phase = Critique
	case ApplyDirective:
		next.Phase = Critique
	case Critique, CritiqueRetry:
		switch verdict {
		case "APPROVE":
			next.Phase = CommitFinal
		case "REVISE":
			next.Phase = Revise
		case "MISSING":
			if s.Phase == Critique {
				next.Phase = CritiqueRetry
			} else {
				next.Phase = Revise
			}
		default:
			return s, fmt.Errorf("Unknown parsed verdict")
		}
	case Revise:
		next.Round++
		next.Phase = Critique
	default:
		return s, fmt.Errorf("Phase has no ordinary turn transition: %s", s.Phase)
	}
	if s.Phase == Draft || s.Phase == Revise || s.Phase == ApplyDirective {
		if next.Round > next.Cap {
			next.Status = Impasse
		} else if questions {
			next.Status = AwaitingAnswer
			next.Phase = AwaitAnswer
		}
	}
	return next, nil
}

// Attachment makes the status table explicit. A saved active intent always
// requires recovery; the absence of a live owner says nothing about completion.
func Attachment(s Snapshot, higherCap bool) string {
	if s.CurrentTurn != "" {
		return "recover"
	}
	switch s.Status {
	case Ready, Running:
		return "execute"
	case AwaitingAnswer:
		return "answer"
	case Handover:
		return "recover"
	case Approved:
		return "saved-result"
	case Impasse:
		if higherCap {
			return "execute"
		}
		return "impasse"
	case Stopped:
		return "fresh-workspace"
	}
	return "invalid"
}
