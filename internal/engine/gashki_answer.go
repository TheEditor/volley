//go:build darwin || linux

package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/gashki"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type answerPane struct {
	adapter *gashkiAdapter
	pane    gashki.PaneBinding
	budget  gashki.Budget
}

func eventSequence(cursor string) (uint64, error) {
	if !strings.HasPrefix(cursor, "e1.") {
		return 0, fmt.Errorf("Unknown cursor format")
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(cursor, "e1."), 10, 64)
	if err != nil || n == 0 || cursor != "e1."+strconv.FormatUint(n, 10) {
		return 0, fmt.Errorf("Invalid cursor")
	}
	return n, nil
}

func (p *answerPane) Observe(ctx context.Context) (human.PaneObservation, error) {
	var result human.PaneObservation
	if err := p.adapter.manager.CheckPane(ctx, p.pane, p.budget); err != nil {
		return result, err
	}
	call, err := p.adapter.manager.Client.FreshCall(ctx, "observe", nil, p.budget, "observe", p.pane.UUID)
	if err != nil {
		return result, err
	}
	if call.ValidationError != "" || !call.Response.OK {
		return result, failure("TURN_UNCERTAIN", "Answer pane observation is not checked", nil)
	}
	d := gashki.Map(call.Response.Data)
	seq, err := eventSequence(gashki.String(d["cursor"]))
	if err != nil || d["id"] != p.pane.UUID || d["name"] != p.pane.Selector || d["agent"] != p.pane.Provider || d["target"] != p.pane.Target || d["source"] != "hook" {
		return result, failure("TURN_UNCERTAIN", "Answer pane identity or hook cursor differs", nil)
	}
	result = human.PaneObservation{PaneID: p.pane.UUID, State: gashki.String(d["state"]), Cursor: gashki.String(d["cursor"]), Source: "hook", Checked: true, Sequence: seq}
	return result, nil
}

func (p *answerPane) WaitSince(ctx context.Context, since string) (human.PaneCompletion, error) {
	var result human.PaneCompletion
	start, err := eventSequence(since)
	if err != nil {
		return result, err
	}
	for {
		chunk, err := p.budget.WaitChunk()
		if err != nil {
			return result, err
		}
		if err = p.adapter.manager.CheckPane(ctx, p.pane, p.budget); err != nil {
			return result, err
		}
		call, err := p.adapter.manager.Client.FreshCall(ctx, "wait", nil, p.budget, "wait", p.pane.UUID, "--until=idle", "--since="+since, "--wait-timeout="+chunk.String())
		if err != nil {
			return result, err
		}
		if call.ValidationError != "" {
			return result, failure("TURN_UNCERTAIN", "Answer wait is not checked", nil)
		}
		if !call.Response.OK {
			if call.Response.Errors[0].Code != "WAIT_TIMEOUT" {
				return result, gashki.NativeError(gashki.MapFailure(call.Response.Errors[0].Code, gashki.ActionContext{Verb: "wait", Checked: true, Unfinished: true}), nil)
			}
			view, err := p.Observe(ctx)
			if err != nil {
				return result, err
			}
			if view.State != "working" && view.State != "compacting" {
				return result, failure("TURN_UNCERTAIN", "Answer pane lacks qualified completion", nil)
			}
			continue
		}
		d := gashki.Map(call.Response.Data)
		event := gashki.Map(d["event"])
		cursor := gashki.String(d["cursor"])
		seq, err := eventSequence(cursor)
		if err != nil || seq <= start || gashki.Integer(event["seq"]) != int64(seq) || d["id"] != p.pane.UUID || d["name"] != p.pane.Selector || d["state"] != "idle" || d["until"] != "idle" || event["kind"] != "turn_ended" || event["source"] != "hook" {
			return result, failure("TURN_UNCERTAIN", "Answer turn completion is not bound", nil)
		}
		return human.PaneCompletion{PaneID: p.pane.UUID, State: "idle", Cursor: cursor, Since: since, Event: "turn_ended", Checked: true, Sequence: seq}, nil
	}
}

// A file or command answer cannot release a loop turn while the owned planner
// is still working. This observer never submits a prompt or starts a pane.
func (o *Owner) settleGashkiAnswer(ctx context.Context, g human.Generation, m store.Snapshot) error {
	a := o.Gashki
	_, spec, err := o.Store.ReadObserved("SPEC.md", store.TextLimit)
	if err != nil || spec.Hash != m.String("spec_hash") {
		return failure("ANSWER_CONFLICT", "Answer turn changed the specification", nil)
	}
	_, questions, err := o.Store.ReadObserved("QUESTIONS.md", human.Limit)
	if err != nil || questions != g.QuestionObserved {
		return failure("ANSWER_CONFLICT", "Answer turn changed the questions", nil)
	}
	b, err := o.Store.ReadText("state/control/gk-spawn-" + o.State.RunID + "-planner-pane.json")
	if err != nil {
		return err
	}
	var pane gashki.PaneBinding
	if err = decode(b, &pane); err != nil {
		return err
	}
	limit, err := time.ParseDuration(o.State.Settings.WaitTimeout)
	if err != nil {
		return err
	}
	q := review.TurnRequest{RunID: o.State.RunID, TurnID: g.ID, Role: "planner", Provider: pane.Provider, Purpose: "answer_record"}
	a.current = g.ID
	defer func() { a.current = "" }()
	_, err = o.Guard.Execute(ctx, q, func(ctx context.Context) (review.TurnOutcome, error) {
		out := review.TurnOutcome{}
		if !a.ready {
			if err := a.manager.Client.Preflight(ctx); err != nil {
				return out, err
			}
			a.ready = true
		}
		observer := &answerPane{adapter: a, pane: pane, budget: gashki.Budget{Limit: limit, Started: time.Now()}}
		text, source, err := human.StableRead(ctx, o.Store, "HUMAN.md", o.Options.Clock)
		if err != nil {
			return out, err
		}
		if source == g.HumanBaseline || len(strings.TrimSpace(string(text))) == 0 {
			view, err := observer.Observe(ctx)
			if err != nil {
				return out, err
			}
			if view.State == "working" || view.State == "compacting" {
				_, err = observer.WaitSince(ctx, view.Cursor)
				if err != nil {
					return out, err
				}
			} else if view.State != "idle" {
				return out, failure("TURN_UNCERTAIN", "Planner pane is not idle", nil)
			}
			return out, nil
		}
		candidateHash := ""
		candidatePath := ""
		settled, err := human.SettlePaneAnswer(ctx, o.Store, observer, human.SettlementOptions{Generation: g, PaneID: pane.UUID, ExpectedSpecHash: m.String("spec_hash"), Before: o.Guard.before, Clock: o.Options.Clock,
			Register: func(path string) error {
				candidatePath = path
				candidate := map[string]any{"record_version": 1, "run_id": g.RunID, "generation_id": g.ID, "text": string(text), "source": source, "prior_directive": g.PriorDirective, "settled": false}
				b, err := contract.Canonical(candidate)
				if err != nil {
					return err
				}
				candidateHash = contract.HashBytes(b)
				return o.Guard.Register(agent.PrimitiveRegistration{TurnID: g.ID, Operation: "checked pane answer candidate", ControllerPaths: []string{path}})
			}, Authorized: func() []store.AuthorizedChange {
				if candidatePath != "" {
					obs, err := o.Store.Observe(candidatePath)
					if err == nil && obs.Hash == candidateHash {
						_ = o.Guard.Authorize(candidatePath, obs)
					}
				}
				proof, err := o.Guard.CurrentProof()
				if err != nil {
					return nil
				}
				return proof.Changes
			}})
		if err != nil {
			return out, err
		}
		if !settled.Settled {
			return out, failure("ANSWER_REQUIRED", "Pane turn did not record a completed answer", map[string]any{"question_id": g.ID})
		}
		return out, nil
	})
	return err
}
