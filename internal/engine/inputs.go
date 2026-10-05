//go:build darwin || linux

package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/store"
	"golang.org/x/sys/unix"
)

type timerClock struct{}

func (timerClock) Wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (o *Owner) consumeInputs(ctx context.Context, m store.Snapshot) error {
	if err := o.repairDeliveries(); err != nil {
		return err
	}
	m, err := o.snapshot()
	if err != nil {
		return err
	}
	applications, _ := m["applications"].([]any)
	for _, entry := range applications {
		d := entry.(map[string]any)
		if d["planner_delivered"] == true {
			continue
		}
		a, err := human.LoadApplication(o.Store, fmt.Sprint(d["receipt_hash"]))
		if err != nil {
			return err
		}
		if _, err = human.RemoveConsumedSource(o.Store, a); err != nil {
			return err
		}
		if a.GenerationID != "" && m.Object("question")["id"] == a.GenerationID {
			g, err := human.LoadGeneration(o.Store, a.GenerationID)
			if err != nil {
				return err
			}
			if g.HumanBaseline.Kind == "file" {
				if _, err = o.Store.RemoveObserved(g.HumanBaseline); err != nil {
					return err
				}
			}
		}
	}
	inbox, err := o.Store.ReadInbox(ctx, m.String("run_id"), time.Second)
	if err != nil {
		return err
	}
	applied := make(map[string]bool)
	for _, id := range append(stringsOf(m["answers"]), stringsOf(m["steering"])...) {
		applied[id] = true
	}
	q := m.Object("question")
	for _, entry := range inbox.Entries {
		if applied[entry.ReceiptHash] {
			continue
		}
		if entry.Kind == "answer" && (entry.GenerationID == nil || *entry.GenerationID != q["id"] || q["answered"] == true) {
			continue
		}
		if entry.Kind != "answer" && entry.Kind != "steer" {
			continue
		}
		var generation *human.Generation
		if entry.Kind == "answer" {
			generation, err = human.LoadGeneration(o.Store, *entry.GenerationID)
			if err != nil {
				return err
			}
		}
		a, err := human.CommitApplication(ctx, o.Store, entry)
		if err != nil {
			return err
		}
		if _, err = human.RemoveConsumedSource(o.Store, a); err != nil {
			return err
		}
		if generation != nil && generation.HumanBaseline.Kind == "file" {
			if _, err = o.Store.RemoveObserved(generation.HumanBaseline); err != nil {
				return err
			}
		}
		applied[entry.ReceiptHash] = true
		m, err = o.snapshot()
		if err != nil {
			return err
		}
		q = m.Object("question")
	}
	// A legacy file outside an unanswered generation is one-shot steering.
	if len(q) == 0 || q["answered"] == true {
		b, obs, err := human.StableRead(ctx, o.Store, "HUMAN.md", o.Options.Clock)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(b)) > 0 {
			key := contract.HashBytes([]byte(fmt.Sprintf("steer:%d:%d:%s", obs.Device, obs.Inode, obs.Hash)))
			sub, err := human.Submit(ctx, o.Store, m.String("run_id"), "", "steer", "legacy-file", human.Input{Text: string(b), IdempotencyKey: key}, &obs, false, "")
			if err != nil {
				return err
			}
			if !applied[sub.Entry.ReceiptHash] {
				a, err := human.CommitApplication(ctx, o.Store, sub.Entry)
				if err != nil {
					return err
				}
				if _, err = human.RemoveConsumedSource(o.Store, a); err != nil {
					return err
				}
			}
		}
	}
	m, err = o.snapshot()
	if err != nil {
		return err
	}
	if m.String("status") != "awaiting_answer" {
		_, ids, err := o.directives(m, "planner")
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if number(m["round"]) > 0 && number(m["completed_rounds"]) >= number(m["round"]) {
				m["round"] = number(m["round"]) + 1
			}
			m["status"] = "ready"
			m["phase"] = "apply_directive"
			return saveState(o.Store, m, o.State, "control", nil, nil)
		}
	}
	return nil
}

func (o *Owner) awaitAnswer(ctx context.Context, m store.Snapshot) error {
	g, err := human.LoadGeneration(o.Store, fmt.Sprint(m.Object("question")["id"]))
	if err != nil {
		return err
	}
	if err = o.hook("engine.answer.wait"); err != nil {
		return err
	}
	// The reader is explicit and is never installed in noninteractive mode.
	var terminal <-chan terminalCandidate
	if o.Request.Interactive {
		readCtx, cancel := context.WithCancel(ctx)
		channel := make(chan terminalCandidate, 1)
		terminal = channel
		go func() {
			reader := o.Request.Input
			if file, ok := reader.(*os.File); ok {
				reader = &terminalReader{ctx: readCtx, file: file}
			}
			text, complete, err := human.TerminalInput(reader)
			channel <- terminalCandidate{text, complete, err}
		}()
		defer func() {
			cancel()
			if terminal == nil {
				return
			}
			select {
			case candidate := <-channel:
				if candidate.text != "" {
					text := candidate.text
					if candidate.complete {
						text += "\n"
					}
					_, _, _ = human.TerminalAnswer(context.WithoutCancel(ctx), o.Store, *g, strings.NewReader(text))
				}
			case <-time.After(time.Second):
			}
		}()
	}
	for {
		if o.Gashki != nil {
			if err = o.settleGashkiAnswer(ctx, *g, m); err != nil {
				return err
			}
		}
		if err = checkBindings(o.Store, o.State); err != nil {
			return err
		}
		if err = o.consumeInputs(ctx, m); err != nil {
			return err
		}
		m, err = o.snapshot()
		if err != nil {
			return err
		}
		if m.Object("question")["answered"] == true {
			return nil
		}
		if _, withdrawn, err := human.FileWithdrawal(ctx, o.Store, *g, o.Options.Clock); err != nil {
			return err
		} else if withdrawn {
			if err = o.consumeInputs(ctx, m); err != nil {
				return err
			}
			return nil
		}
		_, qobs, err := o.Store.ReadObserved("QUESTIONS.md", human.Limit)
		if err != nil {
			return err
		}
		if qobs != g.QuestionObserved {
			return failure("ANSWER_CONFLICT", "Open question changed after its generation began", nil)
		}
		if _, found, err := human.FileAnswer(ctx, o.Store, *g, o.Options.Clock); err != nil {
			return err
		} else if found {
			if err = o.consumeInputs(ctx, m); err != nil {
				return err
			}
			return nil
		}
		if terminal != nil {
			select {
			case candidate := <-terminal:
				if candidate.err != nil {
					return candidate.err
				}
				// Reuse the exact terminal submission path, including EOF retention.
				text := candidate.text
				if candidate.complete {
					text += "\n"
				}
				_, _, err = human.TerminalAnswer(ctx, o.Store, *g, strings.NewReader(text))
				if err != nil {
					return err
				}
				terminal = nil
				if candidate.complete {
					if err = o.consumeInputs(ctx, m); err != nil {
						return err
					}
					return nil
				}
				return failure("ANSWER_REQUIRED", "Terminal EOF did not answer the question", map[string]any{"question_id": g.ID, "run_id": g.RunID})
			default:
			}
		}
		if !o.Request.Wait {
			return failure("ANSWER_REQUIRED", "Answer the current question before review can continue", map[string]any{"question_id": g.ID, "run_id": g.RunID, "commands": []string{"volley human answer WORKSPACE --question-id ID --from-stdin", "volley human skip WORKSPACE --question-id ID --yes"}})
		}
		poll, err := time.ParseDuration(o.State.Settings.PollInterval)
		if err != nil {
			return err
		}
		if explicit, ok := o.Request.Explicit["poll_interval"].(string); ok {
			poll, err = time.ParseDuration(explicit)
			if err != nil {
				return err
			}
		}
		clock := o.Options.Clock
		if clock == nil {
			clock = timerClock{}
		}
		if err = clock.Wait(ctx, poll); err != nil {
			return o.interrupted(ctx)
		}
	}
}

type terminalCandidate struct {
	text     string
	complete bool
	err      error
}

// Terminal reads have one explicit consumer. Polling makes cancellation release
// that consumer when a file or command answer wins, without closing stdin.
type terminalReader struct {
	ctx  context.Context
	file *os.File
}

func (r *terminalReader) Read(b []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		fds := []unix.PollFd{{Fd: int32(r.file.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return 0, io.ErrClosedPipe
		}
		return r.file.Read(b)
	}
}
