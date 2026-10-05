//go:build darwin || linux

package human

import (
	"context"
	"fmt"
	"strings"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

// AnswerPane observes a user-started turn. It never starts a loop turn.
type PaneObservation struct {
	PaneID, State, Cursor, Source string
	Checked                       bool
	Sequence                      uint64
}
type PaneCompletion struct {
	PaneID, State, Cursor, Since, Event string
	Checked                             bool
	Sequence                            uint64
}
type AnswerPane interface {
	Observe(context.Context) (PaneObservation, error)
	WaitSince(context.Context, string) (PaneCompletion, error)
}
type Settlement struct {
	CandidatePath string                `json:"candidate_path"`
	Text          string                `json:"text"`
	Source        store.FileObservation `json:"source"`
	Observation   PaneObservation       `json:"observation"`
	Completion    PaneCompletion        `json:"completion"`
	Settled       bool                  `json:"settled"`
	Reason        string                `json:"reason"`
}
type SettlementOptions struct {
	Generation       Generation
	PaneID           string
	ExpectedSpecHash string
	Before           store.Inventory
	// Registration declares the exact controller-created candidate file.
	Register func(string) error
	// The caller includes the backend's exact raw/control writes too.
	Authorized func() []store.AuthorizedChange
	Clock      Clock
}

func SettlePaneAnswer(ctx context.Context, s *store.Store, pane AnswerPane, o SettlementOptions) (result Settlement, err error) {
	if pane == nil || o.PaneID == "" || o.ExpectedSpecHash == "" || o.Before.Actor != "planner" || !o.Before.AnswerTurn || o.Register == nil || o.Authorized == nil {
		return result, fmt.Errorf("Answer pane identity and protected inventory are required")
	}
	// Compare even a failed observation/wait before trusting any response.
	defer func() {
		mutation, guardErr := s.Compare(ctx, o.Before, o.Authorized())
		if guardErr != nil {
			err = guardErr
			result.Settled = false
			return
		}
		if mutation != nil {
			err = failure("PLANNER_MUTATION", "Answer turn changed protected files")
			result.Settled = false
		}
	}()
	_, spec, err := s.ReadObserved("SPEC.md", store.TextLimit)
	if err != nil || spec.Hash != o.ExpectedSpecHash {
		return result, failure("ANSWER_CONFLICT", "Answer turn changed the specification")
	}
	q, qobs, err := StableRead(ctx, s, "QUESTIONS.md", o.Clock)
	if err != nil || qobs != o.Generation.QuestionObserved || contract.HashBytes(q) != o.Generation.QuestionHash {
		return result, failure("ANSWER_CONFLICT", "Answer turn changed the questions")
	}
	text, source, err := StableRead(ctx, s, "HUMAN.md", o.Clock)
	if err != nil {
		return result, err
	}
	if source == o.Generation.HumanBaseline || !validText(string(text)) {
		result.Reason = "clarification_without_completed_answer"
		return result, nil
	}
	result.Text = string(text)
	result.Source = source
	result.CandidatePath = "state/human/pane-candidate-" + o.Generation.ID + "-" + contract.HashBytes(text) + ".json"
	candidate := map[string]any{"record_version": 1, "run_id": o.Generation.RunID, "generation_id": o.Generation.ID, "text": result.Text, "source": source, "prior_directive": o.Generation.PriorDirective, "settled": false}
	b, err := contract.Canonical(candidate)
	if err != nil {
		return result, err
	}
	if err = o.Register(result.CandidatePath); err != nil {
		return result, err
	}
	if _, err = s.StagePrivateText(result.CandidatePath, b); err != nil {
		return result, err
	}
	observation, err := pane.Observe(ctx)
	result.Observation = observation
	if err != nil || !observation.Checked || observation.PaneID != o.PaneID || observation.Cursor == "" || observation.Source == "" || observation.Sequence == 0 {
		result.Reason = "answer_completion_uncertain"
		return result, failure("TURN_UNCERTAIN", "Answer turn completion cannot be established")
	}
	switch observation.State {
	case "idle": // Checked current state can settle this user-started answer.
	case "working", "compacting":
		completion, e := pane.WaitSince(ctx, observation.Cursor)
		result.Completion = completion
		if e != nil || !completion.Checked || completion.PaneID != o.PaneID || completion.State != "idle" || completion.Since != observation.Cursor || completion.Cursor == "" || completion.Cursor == observation.Cursor || completion.Event != "turn_ended" || completion.Sequence <= observation.Sequence {
			result.Reason = "answer_completion_uncertain"
			return result, failure("TURN_UNCERTAIN", "Answer turn lacks a subsequent qualified completion")
		}
	default:
		result.Reason = "answer_pane_not_safe"
		return result, failure("TURN_UNCERTAIN", "Answer pane state is uncertain")
	}
	again, observed, err := StableRead(ctx, s, "HUMAN.md", o.Clock)
	if err != nil || source != observed || string(again) != result.Text || !strings.HasPrefix(result.Text, o.Generation.PriorDirective) {
		return result, failure("ANSWER_CONFLICT", "Answer text changed during settlement")
	}
	_, spec, err = s.ReadObserved("SPEC.md", store.TextLimit)
	if err != nil || spec.Hash != o.ExpectedSpecHash {
		return result, failure("ANSWER_CONFLICT", "Answer turn changed the specification")
	}
	_, qobs, err = s.ReadObserved("QUESTIONS.md", Limit)
	if err != nil || qobs != o.Generation.QuestionObserved {
		return result, failure("ANSWER_CONFLICT", "Answer turn changed the questions")
	}
	result.Settled = true
	return result, nil
}
