//go:build darwin || linux

package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhash "hash"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/prompt"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type PrimitiveRegistration struct {
	TurnID, Operation           string
	ControllerPaths, AgentPaths []string
}
type DirectOptions struct {
	Prepared                    Prepared
	Store                       *store.Store
	Runner                      process.Runner
	Env                         []string
	RequiredSkills              []string
	CommittedHistory            []string
	InheritedChecked            bool
	InheritedAllow              []string
	AdditionalPermissionSources []string
	Register                    func(PrimitiveRegistration) error
	OnIdentity                  func(context.Context, string, string) error
	OnWrite                     func(string, store.FileObservation) error
}
type Direct struct {
	options     DirectOptions
	permissions PermissionInspection
}

type sessionRecord struct {
	RecordVersion                            int `json:"record_version"`
	RunID, Provider, Role, ID, Model, Effort string
	Identity                                 IdentityContext
	Root                                     *string
	Executable                               ExecutableBinding
}
type directRecord struct {
	RecordVersion int                 `json:"record_version"`
	RequestHash   string              `json:"request_hash"`
	Prepared      review.PreparedTurn `json:"prepared"`
	Skills        prompt.SkillPlan    `json:"skills"`
	Permissions   ClaudeSettings      `json:"permissions"`
}
type directResult struct {
	RecordVersion  int                `json:"record_version"`
	RequestHash    string             `json:"request_hash"`
	Outcome        review.TurnOutcome `json:"outcome"`
	Process        process.Result     `json:"process"`
	EvidenceHashes map[string]string  `json:"evidence_hashes"`
	ErrorCode      string             `json:"error_code"`
}

func NewDirect(o DirectOptions) (*Direct, error) {
	if o.Store == nil || o.Runner == nil || o.Env == nil || o.Prepared.Gate == nil || o.Register == nil || o.Prepared.Record.Backend != "cli" {
		return nil, fmt.Errorf("Direct backend requires prepared records, ownership, a runner, explicit environment, and sink registration")
	}
	if err := CompareIdentity(o.Prepared.Record.Caller, CaptureIdentity(o.Env), nil, nil); err != nil {
		return nil, err
	}
	if err := GuardBilling(context.Background(), o.Prepared.Record.Roots, o.Env, o.Store.Path, o.Prepared.Record.Billing, o.AdditionalPermissionSources); err != nil {
		return nil, err
	}
	o.Env = append([]string(nil), o.Env...)
	o.RequiredSkills = append([]string(nil), o.RequiredSkills...)
	o.CommittedHistory = append([]string(nil), o.CommittedHistory...)
	o.InheritedAllow = append([]string(nil), o.InheritedAllow...)
	paths := []string{filepath.Join(o.Store.Path, ".claude", "settings.json"), filepath.Join(o.Store.Path, ".claude", "settings.local.json")}
	if o.Prepared.Record.Roots.Claude != nil {
		paths = append(paths, filepath.Join(*o.Prepared.Record.Roots.Claude, "settings.json"))
	}
	paths = append(paths, o.AdditionalPermissionSources...)
	inspection, err := InspectPermissionSources(paths)
	if err != nil {
		return nil, err
	}
	o.InheritedAllow = append(o.InheritedAllow, inspection.Allow...)
	return &Direct{options: o, permissions: inspection}, nil
}
func validUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
		} else if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
func directID(id string) bool {
	if len(id) != 26 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= '2' && ch <= '7') {
			return false
		}
	}
	return true
}
func ownedArtifact(path string) bool {
	p := filepath.Clean(path)
	return path != "" && p == path && !filepath.IsAbs(p) && p != "." && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsRune(p, 0) && (p == "SPEC.md" || strings.HasPrefix(p, "rounds/"))
}
func requestHash(q review.TurnRequest) (string, error) {
	b, err := contract.Canonical(q)
	if err != nil {
		return "", err
	}
	return contract.HashBytes(b), nil
}
func decodeOwned(b []byte, target any) error {
	if _, err := input.Record(bytes.NewReader(b), store.RecordLimit); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func (d *Direct) save(path string, value any) error {
	b, err := contract.Canonical(value)
	if err != nil {
		return err
	}
	_, err = d.options.Store.StagePrivateText(path, b)
	if err != nil {
		return err
	}
	return d.authorize(path, contract.HashBytes(b))
}

func (d *Direct) authorize(path, hash string) error {
	if d.options.OnWrite == nil {
		return nil
	}
	_, obs, err := d.options.Store.ReadRaw(path)
	if err != nil {
		return err
	}
	if obs.Hash != hash {
		return safeError("ARTIFACT_CHANGED", "Controller output bytes differ", map[string]any{"path": path})
	}
	return d.options.OnWrite(path, obs)
}

type hashSink struct {
	sink io.Writer
	hash stdhash.Hash
}

func (s *hashSink) Write(b []byte) (int, error) {
	n, err := s.sink.Write(b)
	_, _ = s.hash.Write(b[:n])
	return n, err
}
func (d *Direct) session(q review.TurnRequest, id string) sessionRecord {
	o := d.options
	root := o.Prepared.Record.Roots.Codex
	if q.Provider == "claude" {
		root = o.Prepared.Record.Roots.Claude
	}
	return sessionRecord{1, q.RunID, q.Provider, q.Role, id, o.Prepared.Record.RequestedModel[q.Provider], o.Prepared.Record.RequestedEffort[q.Provider], o.Prepared.Record.Caller, root, o.Prepared.Record.Executables[q.Provider]}
}
func sessionPath(q review.TurnRequest, id string) string {
	return "state/control/session-" + q.Provider + "-" + q.Role + "-" + id + ".json"
}
func (d *Direct) saveIdentity(ctx context.Context, q review.TurnRequest, id string) error {
	if !validUUID(id) {
		return fmt.Errorf("Invalid session UUID")
	}
	if err := d.options.Register(PrimitiveRegistration{TurnID: q.TurnID, Operation: "record session identity", ControllerPaths: []string{sessionPath(q, id), "state/turns/" + q.TurnID + "/session-created.json"}}); err != nil {
		return err
	}
	if err := d.save(sessionPath(q, id), d.session(q, id)); err != nil {
		return err
	}
	if err := d.save("state/turns/"+q.TurnID+"/session-created.json", d.session(q, id)); err != nil {
		return err
	}
	if d.options.OnIdentity != nil {
		return d.options.OnIdentity(ctx, q.Role, id)
	}
	return nil
}
func (d *Direct) checkSession(q review.TurnRequest) error {
	b, err := d.options.Store.ReadText(sessionPath(q, q.SessionID))
	if err != nil {
		return safeError("SESSION_LOST", "Exact saved session binding is unavailable", nil)
	}
	var saved sessionRecord
	if err := decodeOwned(b, &saved); err != nil || !reflect.DeepEqual(saved, d.session(q, q.SessionID)) {
		return safeError("IDENTITY_CONFLICT", "Saved session configuration or identity differs", nil)
	}
	return nil
}
func (d *Direct) Prepare(ctx context.Context, q review.TurnRequest) (review.PreparedTurn, error) {
	var p review.PreparedTurn
	o := d.options
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if !directID(q.RunID) || !directID(q.TurnID) || q.RunID != o.Prepared.Record.RunID || q.Workspace != o.Store.Path || q.Round < 0 || q.Round > 9999 || q.Attempt < 1 || q.Timeout < 0 || q.ElapsedBefore < 0 || q.Role != "planner" && q.Role != "critic" || q.Provider != "claude" && q.Provider != "codex" || len(q.Prompt) == 0 || len(q.Prompt) > prompt.PromptLimit || !utf8.Valid(q.Prompt) || bytes.ContainsRune(q.Prompt, 0) || q.PromptHash != contract.HashBytes(q.Prompt) {
		return p, safeError("INVALID_INPUT", "Invalid direct turn binding", nil)
	}
	if q.Provider == "claude" {
		if err := checkInputs(d.permissions.Sources); err != nil {
			return p, err
		}
	}
	if q.SessionID != "" && (!o.Prepared.Settings.Settings.Persistent || !validUUID(q.SessionID)) {
		return p, safeError("SESSION_LOST", "Resume requires an exact persistent session UUID", nil)
	}
	for _, path := range q.ExpectedArtifacts {
		if !ownedArtifact(path) {
			return p, safeError("INVALID_INPUT", "Invalid expected artifact path", nil)
		}
	}
	if q.SessionID != "" {
		if err := d.checkSession(q); err != nil {
			return p, err
		}
	}
	if err := o.Prepared.Gate.Check(); err != nil {
		return p, err
	}
	hash, err := requestHash(q)
	if err != nil {
		return p, err
	}
	dir := "state/turns/" + q.TurnID
	if err := o.Store.PrepareTurn(q.TurnID); err != nil {
		return p, err
	}
	var previous *directRecord
	if b, err := o.Store.ReadText(dir + "/direct-prepared.json"); err == nil {
		var saved directRecord
		if err := decodeOwned(b, &saved); err != nil || saved.RecordVersion != 1 || saved.RequestHash != hash || !reflect.DeepEqual(saved.Prepared.Request, q) {
			return p, safeError("IDENTITY_CONFLICT", "Turn preparation conflicts with saved request", nil)
		}
		previous = &saved
	} else if !os.IsNotExist(err) {
		return p, err
	}
	root := o.Prepared.Record.Roots.Codex
	if q.Provider == "claude" {
		root = o.Prepared.Record.Roots.Claude
	}
	effective := ""
	if root != nil {
		effective = *root
	}
	skills, err := prompt.PlanSkills(prompt.SkillOptions{Provider: q.Provider, EffectiveRoot: effective, ContextPath: o.Prepared.Settings.Settings.ContextDir, Required: o.RequiredSkills})
	if err != nil {
		return p, err
	}
	intended := q.SessionID
	if q.Provider == "claude" && o.Prepared.Settings.Settings.Persistent && intended == "" {
		if previous != nil {
			intended = previous.Prepared.IntendedIdentity
		} else {
			intended, err = newUUID()
			if err != nil {
				return p, err
			}
		}
	}
	args, permissions, err := BuildDirectArguments(ArgumentOptions{q, o.Prepared.Settings.Settings, o.Prepared.Record.Executables[q.Provider].Path, filepath.Join(o.Store.Path, dir, "final.pending"), intended, skills, o.CommittedHistory, o.InheritedChecked, o.InheritedAllow})
	if err != nil {
		return p, safeError("INVALID_INPUT", "Direct argument preparation refused: "+err.Error(), nil)
	}
	p = review.PreparedTurn{Request: q, Argv: args, IntendedIdentity: intended, CapabilityFacts: map[string]bool{"process_per_turn": true, "exact_resume_id": true, "read_only_critic": q.Provider == "codex", "vendor_permissions_verified": false, "pane_answers": false, "placement": false}}
	if previous != nil {
		if !reflect.DeepEqual(previous.Prepared, p) || !reflect.DeepEqual(previous.Skills, skills) || !reflect.DeepEqual(previous.Permissions, permissions) {
			return p, safeError("IDENTITY_CONFLICT", "Saved direct preparation no longer matches its bindings", nil)
		}
		if intended != "" {
			check := q
			check.SessionID = intended
			if err := d.checkSession(check); err != nil {
				return p, err
			}
		}
		return p, nil
	}
	if q.Provider == "claude" && intended != "" && q.SessionID == "" {
		if err := d.saveIdentity(ctx, q, intended); err != nil {
			return p, err
		}
	}
	if err := d.save(dir+"/direct-prepared.json", directRecord{1, hash, p, skills, permissions}); err != nil {
		return p, err
	}
	return p, nil
}

func (d *Direct) registration(p review.PreparedTurn) PrimitiveRegistration {
	dir := "state/turns/" + p.Request.TurnID
	r := PrimitiveRegistration{TurnID: p.Request.TurnID, Operation: "direct launch"}
	for _, name := range []string{"delivery-intent.json", "process-start.json", "stdout.pending", "stderr.pending", "process-exit.json", "final.txt", "session-created.json", "direct-result.json"} {
		r.ControllerPaths = append(r.ControllerPaths, dir+"/"+name)
	}
	if p.Request.Provider == "codex" {
		r.AgentPaths = []string{dir + "/final.pending"}
	}
	return r
}

func (d *Direct) recoverResult(q review.TurnRequest, hash string) (review.TurnOutcome, error) {
	var outcome review.TurnOutcome
	b, err := d.options.Store.ReadText("state/turns/" + q.TurnID + "/direct-result.json")
	if err != nil {
		return outcome, err
	}
	var saved directResult
	if err := decodeOwned(b, &saved); err != nil || saved.RecordVersion != 1 || saved.RequestHash != hash {
		return outcome, safeError("STATE_INVALID", "Direct completion record is invalid", nil)
	}
	for path, expected := range saved.EvidenceHashes {
		_, observed, err := d.options.Store.ReadRaw(path)
		if err != nil || observed.Hash != expected {
			return outcome, safeError("ARTIFACT_CHANGED", "Direct completion evidence changed", map[string]any{"path": path})
		}
		if err := d.options.Store.SyncRetained(path); err != nil {
			return outcome, err
		}
	}
	if err := d.options.Store.SyncRetained("state/turns/" + q.TurnID + "/direct-result.json"); err != nil {
		return outcome, err
	}
	outcome = saved.Outcome
	for path, expected := range outcome.ArtifactHashes {
		if q.Role == "critic" && strings.HasPrefix(path, "rounds/") {
			if outcome.Reply.Hash != expected {
				return outcome, safeError("STATE_INVALID", "Critique artifact binding is invalid", nil)
			}
			continue
		}
		obs, err := d.options.Store.Observe(path)
		if err != nil || obs.Hash != expected {
			return outcome, safeError("ARTIFACT_CHANGED", "Completed artifact changed before recovery", map[string]any{"path": path})
		}
	}
	if saved.ErrorCode != "" {
		return outcome, safeError(saved.ErrorCode, "Retained direct turn requires inspection", nil)
	}
	return outcome, nil
}

func (d *Direct) Perform(ctx context.Context, p review.PreparedTurn) (review.TurnOutcome, error) {
	began := time.Now()
	q := p.Request
	dir := "state/turns/" + q.TurnID
	out := review.TurnOutcome{Kind: review.NotStarted, ArtifactHashes: map[string]string{}, Warnings: []string{}, Reply: review.Reply{Kind: "missing"}, Retention: review.RetentionDecision{Kind: "retain", Reason: "Keep owned turn evidence"}}
	hash, err := requestHash(q)
	if err != nil {
		return out, err
	}
	b, err := d.options.Store.ReadText(dir + "/direct-prepared.json")
	if err != nil {
		return out, err
	}
	var saved directRecord
	if err := decodeOwned(b, &saved); err != nil || saved.RequestHash != hash || !reflect.DeepEqual(saved.Prepared, p) {
		return out, safeError("IDENTITY_CONFLICT", "Prepared direct operation changed", nil)
	}
	if len(p.Argv) < 2 || p.Argv[0] != d.options.Prepared.Record.Executables[q.Provider].Path {
		return out, safeError("IDENTITY_CONFLICT", "Direct executable binding differs", nil)
	}
	if recovered, err := d.recoverResult(q, hash); err == nil || !os.IsNotExist(err) {
		return recovered, err
	}
	if _, err := d.options.Store.ReadText(dir + "/delivery-intent.json"); err == nil {
		out.Kind = review.Uncertain
		return out, safeError("TURN_UNCERTAIN", "Existing launch intent has no verified completion; do not relaunch", nil)
	} else if !os.IsNotExist(err) {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := d.options.Prepared.Gate.Check(); err != nil {
		return out, err
	}
	if err := GuardBilling(ctx, d.options.Prepared.Record.Roots, d.options.Env, q.Workspace, d.options.Prepared.Record.Billing, d.options.AdditionalPermissionSources); err != nil {
		return out, err
	}
	if q.Provider == "claude" {
		if err := checkInputs(d.permissions.Sources); err != nil {
			return out, err
		}
	}
	if err := d.options.Register(d.registration(p)); err != nil {
		return out, err
	}
	if err := d.save(dir+"/delivery-intent.json", map[string]any{"record_version": 1, "request_hash": hash, "intended_session": p.IntendedIdentity}); err != nil {
		return out, err
	}
	stdout, err := d.options.Store.BeginOutput(dir + "/stdout.pending")
	if err != nil {
		return out, err
	}
	stderr, err := d.options.Store.BeginOutput(dir + "/stderr.pending")
	if err != nil {
		stdout.Close()
		return out, err
	}
	outSink := &hashSink{stdout, sha256.New()}
	errSink := &hashSink{stderr, sha256.New()}
	writer := &codexWriter{sink: outSink, session: p.IntendedIdentity, onIdentity: func(id string) error { return d.saveIdentity(ctx, q, id) }}
	var stdoutWriter io.Writer = &limitedWriter{sink: outSink}
	if q.Provider == "codex" {
		stdoutWriter = writer
	}
	env, _ := process.ChildEnvironment(d.options.Env, "cli")
	result := process.Result{Outcome: process.NotStarted, Exit: -1}
	err = d.options.Prepared.Gate.Call("direct launch", func() error {
		if err := GuardBilling(ctx, d.options.Prepared.Record.Roots, d.options.Env, q.Workspace, d.options.Prepared.Record.Billing, d.options.AdditionalPermissionSources); err != nil {
			return err
		}
		if q.Provider == "claude" {
			if err := checkInputs(d.permissions.Sources); err != nil {
				return err
			}
		}
		var runErr error
		result, runErr = d.options.Runner.Run(ctx, process.Request{Path: p.Argv[0], Args: p.Argv[1:], Cwd: q.Workspace, Env: env, Timeout: q.Timeout, ElapsedBefore: q.ElapsedBefore + time.Since(began), Stdout: stdoutWriter, Stderr: &limitedWriter{sink: errSink}, OnStart: func(id process.Identity) error { return d.save(dir+"/process-start.json", id) }})
		return runErr
	})
	for _, sink := range []struct {
		path string
		file *os.File
		hash stdhash.Hash
	}{{dir + "/stdout.pending", stdout, outSink.hash}, {dir + "/stderr.pending", stderr, errSink.hash}} {
		obs, checkErr := d.options.Store.CheckOutput(sink.path, sink.file, hex.EncodeToString(sink.hash.Sum(nil)))
		if checkErr == nil && d.options.OnWrite != nil {
			checkErr = d.options.OnWrite(sink.path, obs)
		}
		if err == nil {
			err = checkErr
		}
	}
	finishOut := d.options.Store.FinishOutput(dir+"/stdout.pending", stdout)
	finishErr := d.options.Store.FinishOutput(dir+"/stderr.pending", stderr)
	if err == nil {
		err = finishOut
	}
	if err == nil {
		err = finishErr
	}
	if exitErr := d.save(dir+"/process-exit.json", result); err == nil {
		err = exitErr
	}
	code := ""
	out.Completion = review.CompletionReceipt{Exit: result.Exit, Settled: result.Settled, SessionID: p.IntendedIdentity, EvidencePaths: []string{dir + "/stdout.pending", dir + "/stderr.pending", dir + "/process-exit.json"}}
	out.Delivery = review.DeliveryReceipt{PayloadHash: q.PromptHash, Kind: review.NotStarted}
	if result.Started {
		out.Delivery.Kind = review.Completed
	}
	if !result.Started {
		out.Kind = review.NotStarted
		code = "UPSTREAM_FAILURE"
		var typed *contract.Error
		if errors.As(err, &typed) {
			code = typed.Code
		}
	} else if !result.Settled || result.Outcome != process.Exited {
		out.Kind = review.Uncertain
		code = "TURN_UNCERTAIN"
	} else if err != nil || result.Exit != 0 {
		out.Kind = review.Failed
		code = "UPSTREAM_FAILURE"
	} else {
		out.Kind = review.Completed
	}
	if q.Provider == "codex" && result.Started {
		writer.finish()
		out.Completion.SessionID = writer.session
		if out.Kind != review.Uncertain && (writer.err != nil || result.Exit == 0 && !writer.completed) {
			out.Kind = review.Failed
			code = "UPSTREAM_FAILURE"
			out.Warnings = append(out.Warnings, "Invalid or incomplete Codex event protocol")
		}
		if d.options.Prepared.Settings.Settings.Persistent && !validUUID(writer.session) {
			out.Kind = review.Uncertain
			code = "SESSION_LOST"
		}
		if writer.conflict {
			out.Kind = review.IdentityConflict
			code = "IDENTITY_CONFLICT"
		}
	}
	if out.Kind == review.Completed {
		source := dir + "/stdout.pending"
		if q.Provider == "codex" {
			source = dir + "/final.pending"
		}
		out.Reply.Root = d.options.Store.Path
		out.Reply.Source = source
		out.Reply.Format = q.Provider + "-owned-output-v1"
		out.Reply.Version = d.options.Prepared.Record.Executables[q.Provider].Version
		out.Reply.SessionID = out.Completion.SessionID
		reply, readErr := d.options.Store.ReadText(source)
		if readErr == nil && len(bytes.TrimSpace(reply)) != 0 && !bytes.ContainsRune(reply, 0) {
			if err := d.saveText(dir+"/final.txt", reply); err != nil {
				return out, err
			}
			out.Reply.Kind = "found"
			out.Reply.Path = dir + "/final.txt"
			out.Reply.Hash = contract.HashBytes(reply)
		} else {
			out.Reply.Reason = "Final reply is absent, empty, invalid, or unreadable"
			if readErr != nil && !os.IsNotExist(readErr) {
				out.Reply.Kind = "read-failed"
			}
			if q.Role == "critic" {
				out.Kind = review.Failed
				code = "ARTIFACT_MISSING"
			} else {
				out.Warnings = append(out.Warnings, "REPLY_UNAVAILABLE")
			}
		}
		for _, path := range q.ExpectedArtifacts {
			if q.Role == "critic" && strings.HasPrefix(path, "rounds/") {
				if out.Reply.Kind == "found" {
					out.ArtifactHashes[path] = out.Reply.Hash
				}
				continue
			}
			b, readErr := d.options.Store.ReadText(path)
			if readErr != nil || len(bytes.TrimSpace(b)) == 0 || bytes.ContainsRune(b, 0) {
				out.Kind = review.Failed
				code = "ARTIFACT_MISSING"
				continue
			}
			out.ArtifactHashes[path] = contract.HashBytes(b)
		}
		out.Warnings = append(out.Warnings, "MODEL_UNRECORDED")
	}
	out.Completion.Kind = out.Kind
	evidence := map[string]string{}
	for _, path := range append(append([]string{}, out.Completion.EvidencePaths...), out.Reply.Path) {
		if path == "" {
			continue
		}
		_, obs, readErr := d.options.Store.ReadRaw(path)
		if readErr != nil {
			return out, readErr
		}
		evidence[path] = obs.Hash
	}
	if q.Provider == "codex" {
		if _, obs, readErr := d.options.Store.ReadRaw(dir + "/final.pending"); readErr == nil {
			evidence[dir+"/final.pending"] = obs.Hash
		}
	}
	if err := d.save(dir+"/direct-result.json", directResult{1, hash, out, result, evidence, code}); err != nil {
		return out, err
	}
	if code != "" {
		return out, safeError(code, "Direct turn did not produce a verified successful completion", map[string]any{"turn_id": q.TurnID, "exit": result.Exit})
	}
	return out, nil
}
func (d *Direct) saveText(path string, b []byte) error {
	_, err := d.options.Store.StagePrivateText(path, b)
	if err != nil {
		return err
	}
	return d.authorize(path, contract.HashBytes(b))
}

// Execute uses the engine's guard. The adapter does not implement a second
// protected-file comparator. The engine checks even failed operations.
func (d *Direct) Execute(ctx context.Context, p review.PreparedTurn, guard review.GuardedTurnExecutor) (review.TurnOutcome, error) {
	if guard == nil {
		return review.TurnOutcome{}, fmt.Errorf("Guarded executor is required")
	}
	return guard.Execute(ctx, p.Request, func(ctx context.Context) (review.TurnOutcome, error) { return d.Perform(ctx, p) })
}
