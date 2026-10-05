//go:build darwin || linux

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type Request struct {
	Workspace                 string
	Resolved                  *config.Resolved // fresh creation only; attachment uses saved values
	Explicit                  map[string]any
	Brief, Seed, Key          string
	Resume, Wait, Interactive bool
	Input                     io.Reader
	Terminal                  bool
	// AmendSpec is supplied only by a recorded resolution command, never inferred
	// from a changed artifact. The full command surface belongs to T19/T20.
	AmendSpec bool
}

type Options struct {
	Runner process.Runner
	Env    []string
	Clock  human.Clock
	Fault  store.Fault
	// Hook is a harness seam. Release builds have no environment trigger.
	Hook     func(string, *store.Store) error
	Register func(context.Context, store.Snapshot) error
}

type Owner struct {
	Store    *store.Store
	Prepared agent.Prepared
	State    State
	Options  Options
	Request  Request
	Guard    *TurnGuard
	Adapter  *agent.Direct
}

func Run(ctx context.Context, request Request, options Options) (map[string]any, error) {
	if request.Workspace == "" || options.Env == nil {
		return nil, failure("INVALID_INPUT", "Workspace and explicit environment are required", nil)
	}
	if request.Brief != "" && request.Seed != "" {
		return nil, failure("INVALID_INPUT", "Brief and seed sources are mutually exclusive", nil)
	}
	if request.Interactive && (!request.Wait || !request.Terminal || request.Input == nil) {
		return nil, failure("INVALID_INPUT", "Interactive answers require wait and terminal input", nil)
	}
	if len(request.Key) > 1024 || strings.ContainsRune(request.Key, 0) {
		return nil, failure("INVALID_INPUT", "Invalid request key", nil)
	}
	if options.Runner == nil {
		options.Runner = process.UnixRunner{}
	}
	if request.Resume {
		if _, err := os.Stat(request.Workspace); err != nil {
			return nil, failure("NOT_FOUND", "Workspace is absent", nil)
		}
	} else if err := os.MkdirAll(request.Workspace, 0700); err != nil {
		return nil, err
	}
	s, err := store.Open(request.Workspace)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	s.Fault = options.Fault
	if err = s.Prepare(); err != nil {
		return nil, err
	}
	if err = s.AcquireOwner(ctx, 0); err != nil {
		m, _, loadErr := s.LoadSnapshot()
		if loadErr == nil {
			return Data(m), err
		}
		return nil, err
	}
	o := &Owner{Store: s, Options: options, Request: request}
	m, _, err := s.LoadSnapshot()
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		return nil, err
	}
	if fresh {
		if request.Resume {
			return nil, failure("NOT_FOUND", "Run is absent", nil)
		}
		if request.Resolved == nil {
			return nil, failure("INVALID_CONFIG", "Fresh run needs resolved settings", nil)
		}
		settings := request.Resolved.Settings
		if settings.Backend != "cli" {
			return nil, failure("INVALID_CONFIG", "This review slice requires the direct backend", nil)
		}
		if err = o.preflight(); err != nil {
			return nil, err
		}
		m, err = initialize(s, settings)
		if err != nil {
			return nil, err
		}
		if err = o.create(ctx, m); err != nil {
			return Data(m), err
		}
	} else {
		if _, err = s.Recover(); err != nil {
			return Data(m), err
		}
		m, _, err = s.LoadSnapshot()
		if err != nil {
			return nil, err
		}
		if err = o.attach(ctx, m); err != nil {
			return Data(m), err
		}
	}
	o.Guard = &TurnGuard{Store: s, Gate: o.Prepared.Gate}
	o.Adapter, err = agent.NewDirect(agent.DirectOptions{Prepared: o.Prepared, Store: s, Runner: options.Runner, Env: options.Env, InheritedChecked: true, Register: o.Guard.Register, OnWrite: o.Guard.Authorize})
	if err != nil {
		return Data(m), err
	}
	indexWarning := false
	if options.Register != nil {
		current, _, e := s.LoadSnapshot()
		if e == nil {
			indexWarning = options.Register(ctx, current) != nil
		}
	}
	ctx, stopMonitor := watchStop(ctx, s.Path, o.State.RunID)
	defer stopMonitor()
	err = o.drive(ctx)
	if ctx.Err() != nil {
		err = o.interrupted(ctx)
	}
	m, _, loadErr := s.LoadSnapshot()
	if loadErr != nil {
		if err != nil {
			return nil, err
		}
		return nil, loadErr
	}
	data := Data(m)
	if indexWarning {
		data["warnings"] = []string{"INDEX_UNAVAILABLE"}
	}
	if m.String("status") == "approved" {
		if err == nil {
			data["final_result"] = o.auxiliaryData(m)
		}
	}
	if m.String("status") == "approved" || m.String("status") == "impasse" {
		pending, e := o.PendingInputs(context.WithoutCancel(ctx))
		if e != nil {
			if err == nil {
				return data, e
			}
		} else if len(pending) > 0 {
			data["pending_inputs"] = pending
		}
	}
	return data, err
}

func Data(m store.Snapshot) map[string]any {
	var question any
	if q := m.Object("question"); len(q) > 0 && q["answered"] != true {
		question = q["id"]
	}
	return map[string]any{"run_id": m["run_id"], "workspace": m["canonical_workspace"], "status": m["status"], "phase": m["phase"], "round": m["round"], "max_rounds": m["max_rounds"], "turn": m["current_turn"], "question_id": question, "spec_hash": m["spec_hash"], "owner": "foreground", "retention": m["retention"], "next_action": m["phase"], "recommended_action": nil}
}

func readSeed(path string) ([]byte, store.FileObservation, error) {
	physical, err := config.Physical(path)
	if err != nil {
		code := "INVALID_INPUT"
		if os.IsNotExist(err) {
			code = "NOT_FOUND"
		}
		return nil, store.FileObservation{}, failure(code, "Cannot read seed source", map[string]any{"path": path})
	}
	s, err := store.Open(filepath.Dir(physical))
	if err != nil {
		return nil, store.FileObservation{}, failure("INVALID_INPUT", "Cannot open seed source", map[string]any{"path": path})
	}
	defer s.Close()
	b, obs, err := s.ReadObserved(filepath.Base(physical), human.Limit)
	if err != nil {
		return nil, obs, failure("INVALID_INPUT", "Seed must be regular UTF-8 within 1 MiB", map[string]any{"path": path})
	}
	if obs.Kind == "absent" {
		return nil, obs, failure("NOT_FOUND", "Seed source is absent", map[string]any{"path": path})
	}
	if obs.Kind != "file" || len(bytes.TrimSpace(b)) == 0 || bytes.ContainsRune(b, 0) {
		return nil, obs, failure("INVALID_INPUT", "Seed must be nonempty regular UTF-8 within 1 MiB", nil)
	}
	obs.Path = physical
	return b, obs, nil
}

// Reject invalid initial inputs before a manifest can bind a creation request.
// Creation reads and checks the source again under the same owner lock.
func (o *Owner) preflight() error {
	hasBasis := false
	for _, target := range []string{"BRIEF.md", "SPEC.md", "CONSTRAINTS.md"} {
		b, old, err := o.Store.ReadObserved(target, store.TextLimit)
		if err != nil {
			return failure("INVALID_INPUT", "Initial input must be regular UTF-8 within its byte limit", map[string]any{"path": target})
		}
		source := ""
		if target == "BRIEF.md" {
			source = o.Request.Brief
		} else if target == "SPEC.md" {
			source = o.Request.Seed
		}
		if source != "" {
			seed, observed, err := readSeed(source)
			if err != nil {
				return err
			}
			if old.Kind != "absent" && old.Hash != observed.Hash {
				return failure("CONFIG_CONFLICT", "Existing seed target differs", map[string]any{"path": target})
			}
			b, old = seed, observed
		}
		if old.Kind == "absent" {
			continue
		}
		if bytes.ContainsRune(b, 0) || (target != "CONSTRAINTS.md" && len(bytes.TrimSpace(b)) == 0) {
			return failure("INVALID_INPUT", "Initial basis must be nonempty text without NUL", map[string]any{"path": target})
		}
		if target != "CONSTRAINTS.md" {
			hasBasis = true
		}
	}
	if !hasBasis {
		return failure("INVALID_INPUT", "A run needs BRIEF.md or SPEC.md", nil)
	}
	return nil
}

func (o *Owner) create(ctx context.Context, m store.Snapshot) error {
	s := o.Store
	seeds := make(map[string]store.FileObservation)
	var artifacts []store.Artifact
	for target, source := range map[string]string{"BRIEF.md": o.Request.Brief, "SPEC.md": o.Request.Seed} {
		if source == "" {
			continue
		}
		b, obs, err := readSeed(source)
		if err != nil {
			return err
		}
		seeds[target] = obs
		old, err := observation(s, target)
		if err != nil {
			return err
		}
		if old.Kind != "absent" {
			if old.Hash != obs.Hash {
				return failure("CONFIG_CONFLICT", "Existing seed target differs", map[string]any{"path": target})
			}
			continue
		}
		hash, err := s.StageText("state/control/seed-"+m.String("run_id")+"-"+target, b)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, store.Artifact{StagedPath: "state/control/seed-" + m.String("run_id") + "-" + target, TargetPath: target, Hash: hash})
	}
	if len(artifacts) > 0 {
		tx, err := s.NewTransaction("control", m, artifacts, nil)
		if err != nil {
			return err
		}
		if err = s.CommitTransaction(tx); err != nil {
			return err
		}
	}
	bindings, err := bindInputs(s)
	if err != nil {
		return err
	}
	if bindings["SPEC.md"].Kind == "absent" && bindings["BRIEF.md"].Kind == "absent" {
		return failure("INVALID_INPUT", "A run needs BRIEF.md or SPEC.md", nil)
	}
	p, err := agent.Prepare(ctx, agent.PrepareOptions{Resolved: *o.Request.Resolved, Store: s, Runner: o.Options.Runner, Env: o.Options.Env, Billing: agent.BillingSelection{Allowed: o.Request.Resolved.Settings.AllowAPIKey, Explicit: o.Request.Explicit["allow_api_key"] != nil}})
	if err != nil {
		return err
	}
	o.Prepared = p
	hash, err := promptSetHash()
	if err != nil {
		return err
	}
	o.State = State{RecordVersion: 1, RunID: m.String("run_id"), Settings: p.Settings.Settings, Bindings: bindings, Seeds: seeds, PromptSetHash: hash, ActiveApplications: []string{}}
	m, _, err = s.LoadSnapshot()
	if err != nil {
		return err
	}
	m["spec_hash"] = bindings["SPEC.md"].Hash
	m["phase"] = "critique"
	if bindings["SPEC.md"].Kind == "absent" {
		m["phase"] = "draft"
		m["round"] = 0
	}
	m["request_hash"], err = o.requestHash()
	if err != nil {
		return err
	}
	if o.Request.Key != "" {
		m["request_keys"] = []string{o.Request.Key}
	}
	return saveState(s, m, o.State, "control", nil, nil)
}

func (o *Owner) requestHash() (string, error) {
	bindings := map[string]store.FileObservation{"BRIEF.md": o.State.Bindings["BRIEF.md"], "CONSTRAINTS.md": o.State.Bindings["CONSTRAINTS.md"]}
	var contextIdentity any
	if path := o.State.Settings.ContextDir; path != "" {
		s, err := store.Open(path)
		if err != nil {
			return "", err
		}
		defer s.Close()
		obs, err := s.RootObservation()
		if err != nil {
			return "", err
		}
		contextIdentity = obs
	}
	b, err := contract.Canonical(map[string]any{"settings": o.State.Settings, "seeds": o.State.Seeds, "inputs": bindings, "prompt_set": o.State.PromptSetHash, "context": contextIdentity, "executables": o.Prepared.Record.Executables, "identity": o.Prepared.Record.Caller})
	return contract.HashBytes(b), err
}

func stringsOf(v any) []string {
	var result []string
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		for _, value := range values {
			result = append(result, fmt.Sprint(value))
		}
	}
	return result
}

func (o *Owner) attach(ctx context.Context, m store.Snapshot) error {
	state, err := LoadState(o.Store, m)
	if err != nil {
		return err
	}
	o.State = state
	p, err := agent.DecodePreparation(o.Store, m)
	if err != nil {
		return err
	}
	p.Settings = config.Resolved{Settings: state.Settings}
	o.Prepared = p
	hash, err := promptSetHash()
	if err != nil {
		return err
	}
	conflict := func(message string) error {
		code := "CONFIG_CONFLICT"
		for _, key := range stringsOf(m["request_keys"]) {
			if key == o.Request.Key && key != "" {
				code = "IDEMPOTENCY_CONFLICT"
			}
		}
		return failure(code, message, nil)
	}
	if hash != state.PromptSetHash {
		return conflict("Prompt set differs from the saved run")
	}
	bound := state
	if len(m.Object("current_turn")) > 0 || state.Auxiliary.Restoration != nil {
		bound.Bindings = make(map[string]store.FileObservation)
		for path, obs := range state.Bindings {
			if path != "SPEC.md" || (m.Object("current_turn")["role"] != "planner" && state.Auxiliary.Restoration == nil) {
				bound.Bindings[path] = obs
			}
		}
		if o.Request.AmendSpec {
			return failure("TURN_UNCERTAIN", "Spec amendment cannot clear an unfinished turn", nil)
		}
	}
	if err = checkBindings(o.Store, bound); err != nil && !o.Request.AmendSpec {
		return err
	}
	if o.Request.AmendSpec {
		if q := m.Object("question"); len(q) > 0 && q["answered"] != true {
			return failure("ANSWER_REQUIRED", "Spec amendment cannot answer an open question", nil)
		}
		for _, path := range []string{"BRIEF.md", "CONSTRAINTS.md"} {
			after, e := observation(o.Store, path)
			if e != nil || after != state.Bindings[path] {
				return failure("ARTIFACT_CHANGED", "Binding input changed", map[string]any{"path": path})
			}
		}
		obs, e := observation(o.Store, "SPEC.md")
		if e != nil || obs.Kind != "file" || obs.Hash == "" {
			return failure("INVALID_INPUT", "Spec amendment must be a complete file", nil)
		}
		o.State.Bindings["SPEC.md"] = obs
		m["spec_hash"] = obs.Hash
		m["reviewed_spec_hash"] = ""
		m["status"] = "ready"
		m["phase"] = "critique"
		m["current_turn"] = nil
		if number(m["completed_rounds"]) >= number(m["round"]) {
			m["round"] = number(m["completed_rounds"]) + 1
		}
		if number(m["round"]) > number(m["max_rounds"]) {
			m["status"] = "impasse"
		}
		if err = saveState(o.Store, m, o.State, "control", nil, nil); err != nil {
			return err
		}
	}
	settingsBytes, _ := contract.Canonical(state.Settings)
	values, err := inputObject(settingsBytes)
	if err != nil {
		return err
	}
	higher := false
	for key, value := range o.Request.Explicit {
		if key == "max_rounds" && !o.Request.Resume {
			if number(value) != number(m["max_rounds"]) {
				return conflict("Round cap differs from the saved run")
			}
			continue
		}
		if key == "max_rounds" && o.Request.Resume {
			cap := number(value)
			old := number(m["max_rounds"])
			if cap < old {
				return conflict("Round cap cannot decrease")
			}
			higher = cap > old
			continue
		}
		if key == "poll_interval" {
			continue
		} // observation frequency is not a model input
		before, _ := contract.Canonical(values[key])
		after, _ := contract.Canonical(value)
		if !bytes.Equal(before, after) {
			return conflict("Explicit setting differs from the frozen run: " + key)
		}
	}
	for target, source := range map[string]string{"BRIEF.md": o.Request.Brief, "SPEC.md": o.Request.Seed} {
		if source == "" {
			continue
		}
		_, obs, e := readSeed(source)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(state.Seeds[target], obs) {
			return conflict("Seed source differs from the saved request")
		}
	}
	if m.String("status") == "stopped" {
		return failure("CONFIG_CONFLICT", "Stopped run requires a fresh workspace", nil)
	}
	requestHash, err := o.requestHash()
	if err != nil {
		return err
	}
	if requestHash != m.String("request_hash") {
		return conflict("Saved request identity differs")
	}
	if len(m.Object("current_turn")) == 0 {
		// Approved attachments make no external calls, including metadata probes.
		if err = p.CheckResume(agent.CaptureIdentity(o.Options.Env), nil, p.Record.Executables); err != nil {
			return err
		}
	} else if err = p.Gate.Check(); err != nil {
		return err
	}
	if higher {
		if err = p.IncreaseCap(number(o.Request.Explicit["max_rounds"])); err != nil {
			return err
		}
		m, _, err = o.Store.LoadSnapshot()
		if err != nil {
			return err
		}
		if m.String("status") == "impasse" {
			m["status"] = "ready"
			m["phase"] = "critique"
			if err = saveState(o.Store, m, o.State, "control", nil, nil); err != nil {
				return err
			}
			qb, qobs, e := human.StableRead(ctx, o.Store, "QUESTIONS.md", o.Options.Clock)
			if e != nil {
				return e
			}
			if len(bytes.TrimSpace(qb)) > 0 {
				o.State.QuestionPending = true
				o.State.QuestionObserved = qobs
				if e = saveState(o.Store, m, o.State, "control", nil, nil); e != nil {
					return e
				}
			}
		}
	}
	if o.Request.Key != "" {
		keys := stringsOf(m["request_keys"])
		known := false
		for _, key := range keys {
			if key == o.Request.Key {
				known = true
			}
		}
		if !known {
			m["request_keys"] = append(keys, o.Request.Key)
			if err = saveState(o.Store, m, o.State, "control", nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func inputObject(b []byte) (map[string]any, error) {
	// Canonical typed settings contain only integers, so the owned-record reader
	// preserves the same value domain used by configuration validation.
	var values map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&values); err != nil {
		return nil, err
	}
	return values, nil
}

func (o *Owner) snapshot() (store.Snapshot, error) {
	m, _, err := o.Store.LoadSnapshot()
	return m, err
}

func pure(m store.Snapshot) review.Snapshot {
	id, _ := m.Object("current_turn")["id"].(string)
	return review.Snapshot{Status: review.Status(m.String("status")), Phase: review.Phase(m.String("phase")), Round: number(m["round"]), Cap: number(m["max_rounds"]), CurrentTurn: id, Revision: m.Revision()}
}

func (o *Owner) hook(name string) error {
	if o.Options.Hook != nil {
		return o.Options.Hook(name, o.Store)
	}
	return nil
}

func (o *Owner) drive(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return o.interrupted(ctx)
		}
		m, err := o.snapshot()
		if err != nil {
			return err
		}
		if len(m.Object("current_turn")) > 0 {
			if err = o.recoverTurn(ctx, m); err != nil {
				return err
			}
			continue
		}
		if o.State.Auxiliary.Restoration != nil {
			if err = o.finishRestoration(ctx, m); err != nil {
				return err
			}
			continue
		}
		if err = o.repairDeliveries(); err != nil {
			return err
		}
		m, err = o.snapshot()
		if err != nil {
			return err
		}
		if o.State.QuestionPending {
			_, obs, e := o.Store.ReadObserved("QUESTIONS.md", human.Limit)
			if e != nil {
				return e
			}
			if obs != o.State.QuestionObserved {
				return failure("ANSWER_CONFLICT", "Question changed before its generation committed", nil)
			}
			if _, e = human.OpenQuestion(ctx, o.Store, o.State.LastTurn, number(m["round"]), o.Options.Clock); e != nil {
				return e
			}
			o.State.QuestionPending = false
			m, e = o.snapshot()
			if e != nil {
				return e
			}
			if e = saveState(o.Store, m, o.State, "control", nil, nil); e != nil {
				return e
			}
			continue
		}
		if err = checkBindings(o.Store, o.State); err != nil {
			return err
		}
		if err = o.Prepared.Gate.Check(); err != nil {
			return err
		}
		switch review.Attachment(pure(m), false) {
		case "saved-result":
			return o.validateFinal(m)
		case "impasse":
			if restored, e := o.tryRestoration(ctx, m); e != nil {
				return e
			} else if restored {
				continue
			}
			return failure("REVIEW_IMPASSE", "No reviewed approval within the round cap", nil)
		case "fresh-workspace":
			return failure("CONFIG_CONFLICT", "Stopped run requires a fresh workspace", nil)
		case "recover":
			code := "TURN_UNCERTAIN"
			codes := stringsOf(m["errors"])
			if len(codes) > 0 {
				r, _ := contract.Load()
				if c, ok := r.Find(codes[len(codes)-1]); ok && c.Exit == 8 {
					code = c.Code
				}
			}
			return failure(code, "Run requires investigation", nil)
		case "invalid":
			return failure("STATE_INVALID", "Unknown run status", nil)
		}
		if m.String("status") == "awaiting_answer" {
			if err = o.awaitAnswer(ctx, m); err != nil {
				return err
			}
			continue
		}
		if m.String("phase") != "draft" {
			if err = o.consumeInputs(ctx, m); err != nil {
				return err
			}
		}
		m, err = o.snapshot()
		if err != nil {
			return err
		}
		if m.String("phase") == "commit_final" {
			if err = o.finalize(ctx, m); err != nil {
				return err
			}
			continue
		}
		if err = o.turn(ctx, m); err != nil {
			return err
		}
	}
}

func (o *Owner) interrupted(ctx context.Context) error {
	code := "CONTROLLER_INTERRUPTED"
	if fmt.Sprint(context.Cause(ctx)) == "SIGTERM" {
		code = "CONTROLLER_STOPPED"
	}
	m, err := o.snapshot()
	if err == nil {
		status := m.String("status")
		m["status"] = "handover"
		if stopped, ok := context.Cause(ctx).(stopControl); ok {
			code = "CONTROLLER_STOPPED"
			if prior := m.Object("stop_control"); prior["sha256"] == stopped.Receipt.Hash && prior["applied"] == true {
				return failure(code, "Controller stop is recorded", nil)
			}
			m["status"] = "stopped"
			if status == "approved" || status == "impasse" {
				m["status"] = status
			}
			m["stop_control"] = map[string]any{"path": stopped.Receipt.Path, "sha256": stopped.Receipt.Hash, "applied": true}
			if turn := m.Object("current_turn"); len(turn) > 0 {
				turn["delivery_uncertain"] = true
			}
		}
		m["errors"] = append(stringsOf(m["errors"]), code)
		_ = saveState(o.Store, m, o.State, "control", nil, nil)
	}
	return failure(code, "Controller signal interrupted the review", nil)
}

func (o *Owner) handover(m store.Snapshot, err error) error {
	code := "TURN_UNCERTAIN"
	var typed *contract.Error
	if errors.As(err, &typed) {
		code = typed.Code
	}
	m["status"] = "handover"
	m["errors"] = append(stringsOf(m["errors"]), code)
	if turn := m.Object("current_turn"); len(turn) > 0 {
		turn["delivery_uncertain"] = true
	}
	if e := saveState(o.Store, m, o.State, "control", nil, nil); e != nil {
		// A protected checkpoint may itself have been damaged. Do not replace
		// the checked mutation error with a second storage error or repair it.
		if typed != nil {
			if typed.Evidence == nil {
				typed.Evidence = make(map[string]any)
			}
			typed.Evidence["checkpoint_error"] = e.Error()
			return err
		}
		return e
	}
	return err
}
