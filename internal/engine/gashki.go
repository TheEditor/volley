//go:build darwin || linux

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/gashki"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/prompt"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type gashkiAdapter struct {
	owner         *Owner
	manager       *gashki.PaneManager
	current       string
	resume        *gashki.SendIntent
	resumeElapsed time.Duration
	cleanup       bool
	ready         bool
	budgetTurn    string
	budget        gashki.Budget
	missingSend   bool
	resumeMissing bool
}

func environment(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			value = strings.TrimPrefix(entry, key+"=")
		}
	}
	return value
}

func (o *Owner) tmuxPath() (string, error) {
	if o.Options.TmuxPath != "" {
		if !filepath.IsAbs(o.Options.TmuxPath) {
			return "", failure("INVALID_CONFIG", "tmux path must be absolute", nil)
		}
		return o.Options.TmuxPath, nil
	}
	// Use the explicit execution environment, never a fixture's ambient PATH.
	for _, dir := range filepath.SplitList(environment(o.Options.Env, "PATH")) {
		path := filepath.Join(dir, "tmux")
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			if !filepath.IsAbs(path) {
				return "", failure("INVALID_CONFIG", "tmux lookup includes the current directory", nil)
			}
			return path, nil
		}
	}
	return "", failure("DEPENDENCY_MISSING", "tmux is absent from the execution path", nil)
}

func (o *Owner) callerWindow(ctx context.Context, tmux, placement string) (string, error) {
	if placement == "normal" || placement == "auto" && environment(o.Options.Env, "TMUX") == "" {
		return "", nil
	}
	pane, tmuxEnv := environment(o.Options.Env, "TMUX_PANE"), environment(o.Options.Env, "TMUX")
	last := strings.LastIndex(tmuxEnv, ",")
	if last < 0 {
		return "", failure("PANE_CONFLICT", "Caller pane is unavailable", nil)
	}
	last = strings.LastIndex(tmuxEnv[:last], ",")
	if last < 0 || pane == "" {
		return "", failure("PANE_CONFLICT", "Caller pane is unavailable", nil)
	}
	var stdout, stderr bytes.Buffer
	r, err := o.Options.Runner.Run(ctx, process.Request{Path: tmux, Args: []string{"-S", tmuxEnv[:last], "display-message", "-p", "-t", pane, "#{window_id}"}, Env: o.Options.Env, Timeout: 10 * time.Second, Stdout: &stdout, Stderr: &stderr})
	window := strings.TrimSpace(stdout.String())
	if err != nil || !r.Settled || r.Outcome != process.Exited || r.Exit != 0 || !strings.HasPrefix(window, "@") || strings.ContainsAny(window, "\n\r\x00") {
		return "", failure("PANE_CONFLICT", "Caller window cannot be verified", nil)
	}
	return window, nil
}

func (o *Owner) currentServer(ctx context.Context) (*agent.ServerContext, error) {
	if o.Prepared.Record.Backend != "gashki" {
		return nil, nil
	}
	path, err := o.tmuxPath()
	if err != nil {
		return nil, err
	}
	record := o.Prepared.Record
	if record.Mechanism == nil || record.Server == nil {
		return nil, failure("STATE_INVALID", "Saved server binding is absent", nil)
	}
	socket := ""
	if record.Mechanism.Socket != nil {
		socket = *record.Mechanism.Socket
	}
	server := agent.ProbeServer(ctx, o.Options.Runner, path, o.Options.Env, o.Store.Path, socket, record.Server.Window, o.Prepared.Gate.Check)
	return &server, nil
}

func (o *Owner) newGashkiAdapter() (*gashkiAdapter, error) {
	path, err := o.tmuxPath()
	if err != nil {
		return nil, err
	}
	record := o.Prepared.Record
	if record.Server == nil || record.Server.Socket == nil || record.Server.SocketKind != "socket" {
		return nil, failure("SERVER_CONFLICT", "Prepared server identity is unknown", nil)
	}
	a := &gashkiAdapter{owner: o}
	gate := func(ctx context.Context, kind string) error {
		if err := o.Prepared.Gate.Check(); err != nil {
			return err
		}
		server, err := o.currentServer(ctx)
		if err != nil {
			return err
		}
		if err = agent.CompareIdentity(record.Caller, agent.CaptureIdentity(o.Options.Env), record.Server, server); err != nil {
			return err
		}
		return agent.GuardBilling(ctx, agent.ResolveRoots(agent.CaptureIdentity(o.Options.Env), server), o.Options.Env, o.Store.Path, record.Billing, nil)
	}
	binary := record.Executables["gashki"]
	c, err := gashki.NewClient(gashki.ClientOptions{RunID: record.RunID, Store: o.Store, Runner: o.Options.Runner, Binary: binary.Path, Config: filepath.Join(o.Store.Path, "gashki.config.toml"), Env: o.Options.Env, SourceProof: o.Options.GashkiSourceProof, Gate: gate,
		Register: func(paths []string) error {
			return o.Guard.Register(agent.PrimitiveRegistration{TurnID: a.current, Operation: "checked Gashki primitive", ControllerPaths: paths})
		}, OnWrite: o.Guard.Authorize,
		BeforeMutation: func(ctx context.Context, intent gashki.CallIntent) error {
			m, err := o.snapshot()
			if err != nil {
				return err
			}
			turn := m.Object("current_turn")
			if intent.Verb == "kill" && a.cleanup && len(turn) == 0 && (m.String("status") == "approved" || m.String("status") == "impasse") {
				ref, _ := m.Object("cleanup")["intent"].(map[string]any)
				path, _ := ref["path"].(string)
				b, err := o.Store.ReadText(path)
				if err != nil || contract.HashBytes(b) != ref["sha256"] {
					return failure("STATE_INVALID", "Cleanup intent differs", nil)
				}
				var panes []gashki.PaneBinding
				if err = json.Unmarshal(b, &panes); err != nil {
					return err
				}
				for _, pane := range panes {
					if len(intent.Args) > 1 && intent.Args[1] == pane.UUID {
						return nil
					}
				}
				return failure("PANE_CONFLICT", "Cleanup target is not in the committed intent", nil)
			}
			if a.current == "" || turn["id"] != a.current {
				return failure("STATE_INVALID", "Gashki mutation has no current turn intent", nil)
			}
			b, err := o.Store.ReadText("state/turns/" + a.current + "/engine-request.json")
			if err != nil || contract.HashBytes(b) != turn["intent_hash"] {
				return failure("STATE_INVALID", "Gashki mutation intent differs", nil)
			}
			history, err := o.Store.History()
			if err != nil {
				return err
			}
			for _, transaction := range history {
				if transaction.Kind != "intent" {
					continue
				}
				var saved store.Snapshot
				if err = json.Unmarshal(transaction.Next, &saved); err != nil {
					return err
				}
				if saved.Object("current_turn")["id"] == a.current && saved.Object("current_turn")["intent_hash"] == turn["intent_hash"] {
					return o.Store.ReadyIntent(transaction.ID)
				}
			}
			return failure("STATE_INVALID", "Gashki mutation has no committed intent", nil)
		}})
	if err != nil {
		return nil, err
	}
	a.manager = &gashki.PaneManager{Client: c, Observer: gashki.TmuxObserver{Path: path, Runner: o.Options.Runner, Gate: gate}, ExpectedServer: gashki.ServerBinding{SocketPath: *record.Server.Socket, Device: record.Server.SocketDevice, Inode: record.Server.SocketInode, CallerWindow: record.Server.Window}}
	return a, nil
}

func (a *gashkiAdapter) Prepare(ctx context.Context, q review.TurnRequest) (review.PreparedTurn, error) {
	o := a.owner
	var p review.PreparedTurn
	if q.RunID != o.State.RunID || q.Workspace != o.Store.Path || q.PromptHash != contract.HashBytes(q.Prompt) {
		return p, failure("INVALID_INPUT", "Gashki turn binding differs", nil)
	}
	if err := o.Prepared.Gate.Check(); err != nil {
		return p, err
	}
	if err := o.Store.PrepareTurn(q.TurnID); err != nil {
		return p, err
	}
	root := o.Prepared.Record.Roots.Claude
	if q.Provider == "codex" {
		root = o.Prepared.Record.Roots.Codex
	}
	effective := ""
	if root != nil {
		effective = *root
	}
	skills, err := prompt.PlanSkills(prompt.SkillOptions{Provider: q.Provider, EffectiveRoot: effective, ContextPath: o.State.Settings.ContextDir})
	if err != nil {
		return p, err
	}
	sources := []string{filepath.Join(o.Store.Path, ".claude/settings.json"), filepath.Join(o.Store.Path, ".claude/settings.local.json")}
	if o.Prepared.Record.Roots.Claude != nil {
		sources = append(sources, filepath.Join(*o.Prepared.Record.Roots.Claude, "settings.json"))
	}
	inspection, err := agent.InspectPermissionSources(sources)
	if err != nil {
		return p, err
	}
	// Pane startup arguments stay fixed across turns. The turn guard protects
	// all committed history as it grows; a stub pass does not prove prevention.
	args, _, err := agent.BuildGashkiArguments(agent.ArgumentOptions{Request: q, Settings: o.State.Settings, Skills: skills, InheritedChecked: true, InheritedAllow: inspection.Allow})
	if err != nil {
		return p, err
	}
	p = review.PreparedTurn{Request: q, Argv: args, CapabilityFacts: map[string]bool{"qualified_cursor": true, "vendor_permissions_verified": false, "pane_answers": true, "placement": true}}
	path := "state/turns/" + q.TurnID + "/gk-prepared.json"
	if old, err := o.Store.ReadText(path); err == nil {
		var saved review.PreparedTurn
		if err := decode(old, &saved); err != nil || !reflect.DeepEqual(saved, p) {
			return p, failure("IDENTITY_CONFLICT", "Saved pane turn preparation differs", nil)
		}
	} else if !os.IsNotExist(err) {
		return p, err
	} else {
		b, err := contract.Canonical(p)
		if err != nil {
			return p, err
		}
		if _, err = o.Store.StagePrivateText(path, b); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (a *gashkiAdapter) Perform(ctx context.Context, p review.PreparedTurn) (out review.TurnOutcome, err error) {
	o, q := a.owner, p.Request
	a.current = q.TurnID
	a.missingSend = false
	defer func() { a.current = "" }()
	out = review.TurnOutcome{Kind: review.NotStarted, Reply: review.Reply{Kind: "missing", Reason: "No checked reply"}, ArtifactHashes: map[string]string{}, Warnings: []string{"MODEL_UNRECORDED"}, Retention: review.RetentionDecision{Kind: "retain", Reason: "Keep checked pane evidence"}}
	if err = o.Prepared.Gate.Check(); err != nil {
		return out, err
	}
	budget := gashki.Budget{Limit: q.Timeout, Started: time.Now(), ElapsedBefore: q.ElapsedBefore}
	if a.resume != nil {
		budget.ElapsedBefore = a.resumeElapsed
	}
	a.budgetTurn, a.budget = q.TurnID, budget
	defer func() {
		if left, finite := budget.Remaining(); err == nil && finite && left <= 0 {
			err = failure("TURN_TIMEOUT", "Pane turn budget expired during output collection", nil)
		}
	}()
	if !a.ready {
		if err = a.manager.Client.Preflight(ctx); err != nil {
			return out, err
		}
		a.ready = true
	}
	role := q.Role
	if q.Purpose == "advisory" {
		role = "second"
	}
	var pane gashki.PaneBinding
	if a.resume != nil {
		pane = a.resume.Pane
	} else {
		spawn, e := a.manager.PrepareSpawn(ctx, gashki.SpawnRequest{RunID: q.RunID, Role: role, Provider: q.Provider, Cwd: q.Workspace, Placement: o.State.Settings.Placement, AgentArgs: p.Argv, TrustFolder: o.State.Settings.TrustFolder}, budget)
		if e != nil {
			return out, e
		}
		pane, err = a.manager.Spawn(ctx, spawn, nil, budget)
		if err != nil {
			return out, err
		}
	}
	out.Retention.OwnedTargets = []string{pane.UUID}
	if q.Role == "critic" {
		for _, path := range q.ExpectedArtifacts {
			_, obs, e := o.Store.ReadObserved(path, store.TextLimit)
			if e != nil {
				return out, e
			}
			if obs.Kind != "absent" && a.resume == nil {
				return out, failure("OUTPUT_CONFLICT", "Critique output exists before its turn", map[string]any{"path": path})
			}
		}
		if err = o.Guard.Register(agent.PrimitiveRegistration{TurnID: q.TurnID, Operation: "checked Gashki artifact", AgentPaths: q.ExpectedArtifacts}); err != nil {
			return out, err
		}
	}
	var send gashki.SendIntent
	if a.resume != nil {
		send = *a.resume
	} else {
		send, err = a.manager.PrepareSend(ctx, q.TurnID, pane, q.Prompt, budget)
		if err != nil {
			return out, err
		}
	}
	if a.resume == nil {
		if err = a.saveSendGuard("send_prepared", review.DeliveryReceipt{PaneUUID: pane.UUID}, budget); err != nil {
			return out, err
		}
	}
	out.Delivery, err = a.manager.Deliver(ctx, send, budget)
	if err != nil {
		// This fact is set inside the guard after exact send/server checks.
		// A later failed guard comparison prevents its publication.
		var native *contract.Error
		if errors.As(err, &native) && native.Code == "SEND_UNCERTAIN" && a.manager.Client.SourceChecked() && ctx.Err() == nil {
			if checked, e := a.manager.MissingReceiptRecovery(ctx, send, budget); e == nil {
				a.missingSend = checked
			}
		}
		return out, err
	}
	if a.resume == nil || a.resumeMissing {
		if err = a.saveDeliveryGuard(out.Delivery, budget); err != nil {
			return out, err
		}
	}
	if err = o.hook("engine.gashki.delivered"); err != nil {
		return out, err
	}
	out.Completion, err = a.manager.AwaitCompletion(ctx, send, out.Delivery, budget)
	if err != nil {
		return out, err
	}
	out.Kind = review.Completed
	if q.Role == "critic" {
		if len(q.ExpectedArtifacts) != 1 {
			return out, failure("STATE_INVALID", "Critique output binding is absent", nil)
		}
		b, e := o.Store.ReadText(q.ExpectedArtifacts[0])
		if e != nil {
			return out, e
		}
		if len(bytes.TrimSpace(b)) == 0 {
			return out, failure("TURN_FAILED", "Critique file is empty", nil)
		}
		path := "state/turns/" + q.TurnID + "/final.txt"
		if err = o.Guard.Register(agent.PrimitiveRegistration{TurnID: q.TurnID, Operation: "checked reply copy", ControllerPaths: []string{path}}); err != nil {
			return out, err
		}
		if _, err = o.Store.StagePrivateText(path, b); err != nil {
			return out, err
		}
		obs, e := o.Store.Observe(path)
		if e != nil {
			return out, e
		}
		if err = o.Guard.Authorize(path, obs); err != nil {
			return out, err
		}
		out.Reply = review.Reply{Kind: "found", Path: path, Hash: contract.HashBytes(b), Root: o.Store.Path, Source: q.ExpectedArtifacts[0], Format: "owned-gashki-file-v1", Version: o.Prepared.Record.Executables["gashki"].Version, PromptPath: send.PromptPath}
		out.ArtifactHashes[q.ExpectedArtifacts[0]] = out.Reply.Hash
	} else {
		obs, e := o.Store.Observe("SPEC.md")
		if e != nil {
			return out, e
		}
		out.ArtifactHashes["SPEC.md"] = obs.Hash
		root := o.Prepared.Record.Roots.Claude
		if q.Provider == "codex" {
			root = o.Prepared.Record.Roots.Codex
		}
		if root == nil {
			return out, failure("IDENTITY_CONFLICT", "Prepared transcript root is unknown", nil)
		}
		captureCtx := ctx
		cancel := func() {}
		if left, finite := budget.Remaining(); finite {
			if left <= 0 {
				return out, failure("TURN_TIMEOUT", "Pane turn budget expired before reply collection", nil)
			}
			captureCtx, cancel = context.WithTimeout(ctx, left)
		}
		capture, e := agent.CaptureTranscript(captureCtx, agent.TranscriptRequest{Provider: q.Provider, Root: *root, Workspace: q.Workspace, PromptPath: filepath.Join(o.Store.Path, send.PromptPath), CheckedCompletion: true, CompletedAt: time.Now(), Check: func(ctx context.Context) error { return o.Prepared.Gate.Check() }}, nil)
		cancel()
		if e != nil {
			if ctx.Err() == nil && captureCtx.Err() == context.DeadlineExceeded {
				return out, failure("TURN_TIMEOUT", "Pane turn budget expired during reply collection", nil)
			}
			return out, e
		}
		paths := []string{}
		out.Reply, e = agent.SaveCapturedReply(o.Store, q.TurnID, capture, func(path string) error {
			paths = append(paths, path)
			return o.Guard.Register(agent.PrimitiveRegistration{TurnID: q.TurnID, Operation: "checked transcript copy", ControllerPaths: []string{path}})
		})
		if e != nil {
			return out, e
		}
		for _, path := range paths {
			obs, e := o.Store.Observe(path)
			if e != nil {
				return out, e
			}
			if e = o.Guard.Authorize(path, obs); e != nil {
				return out, e
			}
		}
		if out.Reply.Kind != "found" {
			out.Warnings = append(out.Warnings, "REPLY_UNAVAILABLE")
		}
	}
	return out, nil
}

func (a *gashkiAdapter) saveDeliveryGuard(delivery review.DeliveryReceipt, budget gashki.Budget) error {
	return a.saveSendGuard("delivery_confirmed", delivery, budget)
}

func (a *gashkiAdapter) saveSendGuard(operation string, delivery review.DeliveryReceipt, budget gashki.Budget) error {
	o := a.owner
	proof, err := o.Guard.CurrentProof()
	if err != nil {
		return err
	}
	b, err := contract.Canonical(proof)
	if err != nil {
		return err
	}
	path := "state/turns/" + a.current + "/gk-recovery-guard.json"
	if operation == "send_prepared" {
		path = "state/turns/" + a.current + "/gk-send-guard.json"
	}
	if err = o.Guard.Register(agent.PrimitiveRegistration{TurnID: a.current, Operation: "checked delivery guard", ControllerPaths: []string{path}}); err != nil {
		return err
	}
	hash, err := o.Store.StagePrivateText(path, b)
	if err != nil {
		return err
	}
	obs, err := o.Store.Observe(path)
	if err != nil {
		return err
	}
	if err = o.Guard.Authorize(path, obs); err != nil {
		return err
	}
	m, err := o.snapshot()
	if err != nil {
		return err
	}
	turn := m.Object("current_turn")
	turn["cursor"], turn["operation"] = delivery.Cursor, operation
	turn["pane_uuid"] = delivery.PaneUUID
	turn["recovery_guard"] = map[string]any{"path": path, "sha256": hash}
	if left, finite := budget.Remaining(); finite {
		turn["remaining"] = left.String()
	}
	if err = saveState(o.Store, m, o.State, "control", nil, nil); err != nil {
		return err
	}
	changes, err := checkpointChanges(o.Store, o.Guard.before)
	if err != nil {
		return err
	}
	o.Guard.mu.Lock()
	o.Guard.checkpoints = changes
	o.Guard.mu.Unlock()
	return nil
}

var _ review.TurnAdapter = (*gashkiAdapter)(nil)

func (o *Owner) recordGashkiPane(m store.Snapshot, q review.TurnRequest, out review.TurnOutcome) error {
	b, err := o.Store.ReadText("state/turns/" + q.TurnID + "/gk-send-intent.json")
	if err != nil {
		return err
	}
	var send gashki.SendIntent
	if err = decode(b, &send); err != nil {
		return err
	}
	role := q.Role
	if q.Purpose == "advisory" {
		role = "second"
	}
	pane := send.Pane
	if send.RunID != q.RunID || send.TurnID != q.TurnID || pane.RunID != q.RunID || pane.Role != role || pane.Provider != q.Provider || pane.UUID == "" || pane.UUID != out.Delivery.PaneUUID {
		return failure("STATE_INVALID", "Completed pane identity differs", nil)
	}
	m.Object("panes")[role] = map[string]any{"uuid": pane.UUID, "target": pane.Target, "selector": pane.Selector, "provider": pane.Provider, "ready_cursor": pane.ReadyCursor}
	ids := []string{}
	for _, role := range []string{"planner", "critic", "second"} {
		p, _ := m.Object("panes")[role].(map[string]any)
		if id, _ := p["uuid"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	m.Object("retention")["panes"] = ids
	return nil
}
