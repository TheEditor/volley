//go:build darwin || linux

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

type ExecutableBinding struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}
type GashkiMechanism struct {
	StateDir string  `json:"state_dir"`
	Socket   *string `json:"tmux_socket"`
	Ready    string  `json:"ready_timeout"`
	Wait     string  `json:"wait_timeout"`
}
type PreparationRecord struct {
	RecordVersion           int                          `json:"record_version"`
	RunID                   string                       `json:"run_id"`
	Backend                 string                       `json:"backend"`
	Caller                  IdentityContext              `json:"caller"`
	Server                  *ServerContext               `json:"server"`
	Roots                   EffectiveRoots               `json:"roots"`
	Executables             map[string]ExecutableBinding `json:"executables"`
	Mechanism               *GashkiMechanism             `json:"mechanism"`
	Billing                 BillingSelection             `json:"billing"`
	RequestedModel          map[string]string            `json:"requested_model"`
	RequestedEffort         map[string]string            `json:"requested_effort"`
	ObservedModel           map[string]*string           `json:"observed_model"`
	ObservedEffort          map[string]*string           `json:"observed_effort"`
	ObservationReason       string                       `json:"observation_reason"`
	RemovedEnvironmentNames []string                     `json:"removed_environment_names"`
}
type PrepareOptions struct {
	Resolved               config.Resolved
	Store                  *store.Store
	Runner                 process.Runner
	Env                    []string
	TmuxPath, CallerWindow string
	Billing                BillingSelection
	ExtraGuardSettings     []string
	// Lookup is preparation's sole active executable resolution. It must preserve
	// Go's ErrDot policy; production uses config.LookupExecutable.
	Lookup func(string) (string, error)
}
type Prepared struct {
	Record   PreparationRecord
	Gate     *FrozenGate
	Settings config.Resolved
	StateDir string
}
type initialObservation struct {
	Path   string
	Hash   string
	Exists bool
	Info   os.FileInfo
}

func observeInput(path string) (initialObservation, error) {
	observation := initialObservation{Path: path}
	if path == "" {
		return observation, nil
	}
	b, info, err := readRegular(path, input.Limit)
	if os.IsNotExist(err) {
		return observation, nil
	}
	if err != nil {
		return observation, err
	}
	observation.Exists = true
	observation.Hash = contract.HashBytes(b)
	observation.Info = info
	return observation, nil
}
func checkInputs(expected []initialObservation) error {
	for _, before := range expected {
		if before.Path == "" {
			continue
		}
		after, err := observeInput(before.Path)
		if err != nil || before.Exists != after.Exists || before.Hash != after.Hash || before.Exists && !os.SameFile(before.Info, after.Info) {
			return safeError("ARTIFACT_CHANGED", "Selected preparation input changed", map[string]any{"actor": "external_or_unknown", "path": before.Path, "expected_hash": before.Hash, "observed_hash": after.Hash})
		}
	}
	return nil
}
func readRegular(path string, limit int) ([]byte, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, os.ErrInvalid
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(b) > limit {
		return nil, nil, os.ErrInvalid
	}
	after, err := f.Stat()
	if err != nil || info.Size() != after.Size() {
		return nil, nil, os.ErrInvalid
	}
	return b, info, nil
}

type cappedText struct {
	bytes.Buffer
	limit int
}

func (w *cappedText) Write(b []byte) (int, error) {
	if w.Len()+len(b) > w.limit {
		return 0, fmt.Errorf("External metadata exceeds its byte limit")
	}
	return w.Buffer.Write(b)
}
func runText(ctx context.Context, runner process.Runner, request process.Request) (string, process.Result, error) {
	if runner == nil {
		return "", process.Result{}, fmt.Errorf("Process runner is required")
	}
	out := &cappedText{limit: input.Limit}
	discard := &cappedText{limit: input.Limit}
	request.Stdout = out
	request.Stderr = discard
	result, err := runner.Run(ctx, request)
	return out.String(), result, err
}
func normalExit(r process.Result) bool {
	return r.Outcome == process.Exited && r.Exit == 0 && r.Settled && !r.TimedOut && !r.Interrupted
}
func bindExecutable(ctx context.Context, options PrepareOptions, path string, before func() error) (ExecutableBinding, error) {
	lookup := options.Lookup
	if lookup == nil {
		lookup = config.LookupExecutable
	}
	resolved, err := lookup(path)
	if err != nil {
		return ExecutableBinding{}, safeError("DEPENDENCY_MISSING", "Active executable cannot be resolved", map[string]any{"path": path, "reason": err.Error()})
	}
	if !filepath.IsAbs(resolved) {
		return ExecutableBinding{}, safeError("INVALID_CONFIG", "Resolved executable is not absolute", map[string]any{"path": resolved})
	}
	if err := before(); err != nil {
		return ExecutableBinding{}, err
	}
	version, result, err := runText(ctx, options.Runner, process.Request{Path: resolved, Args: []string{"--version"}, Cwd: options.Store.Path, Env: options.Env, Timeout: 10 * time.Second})
	if err != nil || !normalExit(result) {
		return ExecutableBinding{}, safeError("INVALID_CONFIG", "Active executable version query failed", map[string]any{"path": resolved})
	}
	version = strings.TrimSpace(version)
	if version == "" || strings.ContainsRune(version, 0) || len(version) > 65536 {
		return ExecutableBinding{}, safeError("INVALID_CONFIG", "Active executable version is invalid", map[string]any{"path": resolved})
	}
	return ExecutableBinding{resolved, version}, nil
}
func gashkiSourcePath(settings config.Settings, env []string) (string, error) {
	if settings.GashkiConfig != "" {
		return settings.GashkiConfig, nil
	}
	root := envVariable(env, "XDG_CONFIG_HOME", "caller")
	if root.Present && root.Value != nil && filepath.IsAbs(*root.Value) {
		return filepath.Join(*root.Value, "gashki/config.toml"), nil
	}
	home := envVariable(env, "HOME", "caller")
	if home.Value == nil || !filepath.IsAbs(*home.Value) {
		return "", safeError("INVALID_CONFIG", "Gashki default config path is unknown", nil)
	}
	return filepath.Join(*home.Value, ".config/gashki/config.toml"), nil
}
func parseGashkiRecord(b []byte) (GashkiMechanism, error) {
	var values map[string]any
	if len(b) > input.Limit || !utf8.Valid(b) {
		return GashkiMechanism{}, fmt.Errorf("Gashki record exceeds its byte limit")
	}
	if err := toml.Unmarshal(b, &values); err != nil {
		return GashkiMechanism{}, fmt.Errorf("Gashki settings are not valid TOML")
	}
	for key, value := range values {
		if key != "state_dir" && key != "tmux_socket" && key != "ready_timeout" && key != "wait_timeout" {
			return GashkiMechanism{}, fmt.Errorf("Unknown Gashki setting")
		}
		if _, ok := value.(string); !ok {
			return GashkiMechanism{}, fmt.Errorf("Gashki setting is not a string")
		}
	}
	state, _ := values["state_dir"].(string)
	ready, _ := values["ready_timeout"].(string)
	wait, _ := values["wait_timeout"].(string)
	if !filepath.IsAbs(state) || strings.ContainsRune(state, 0) {
		return GashkiMechanism{}, fmt.Errorf("Gashki state_dir is not absolute")
	}
	for _, d := range []string{ready, wait} {
		duration, err := time.ParseDuration(d)
		if err != nil || duration < time.Second || duration > 24*time.Hour {
			return GashkiMechanism{}, fmt.Errorf("Gashki timeout is invalid")
		}
	}
	var socket *string
	if v, ok := values["tmux_socket"].(string); ok {
		if len(v) < 1 || len(v) > 64 {
			return GashkiMechanism{}, fmt.Errorf("Gashki socket is invalid")
		}
		for _, r := range v {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return GashkiMechanism{}, fmt.Errorf("Gashki socket is invalid")
			}
		}
		socket = &v
	}
	return GashkiMechanism{state, socket, ready, wait}, nil
}
func gashkiQuery(ctx context.Context, o PrepareOptions, binding ExecutableBinding, selected string, flags []string, before func() error) (string, error) {
	if err := before(); err != nil {
		return "", err
	}
	args := []string{}
	if selected != "" {
		args = append(args, "--config", selected)
	}
	args = append(args, flags...)
	out, r, err := runText(ctx, o.Runner, process.Request{Path: binding.Path, Args: args, Cwd: o.Store.Path, Env: o.Env, Timeout: 10 * time.Second})
	if err != nil || !normalExit(r) {
		return "", safeError("INVALID_CONFIG", "Gashki preparation query failed", map[string]any{"operation": "config", "config": selected})
	}
	return out, nil
}
func stateDirValue(text string) (string, error) {
	value, err := input.Record(strings.NewReader(text), input.Limit)
	if err != nil {
		return "", safeError("INVALID_CONFIG", "Gashki state query is invalid JSON", nil)
	}
	envelope, ok := value.(map[string]any)
	if !ok || envelope["ok"] != true {
		return "", safeError("INVALID_CONFIG", "Gashki state query did not succeed", nil)
	}
	for _, key := range []string{"ok", "tool_version", "data", "meta", "warnings", "commands", "errors"} {
		if _, exists := envelope[key]; !exists {
			return "", safeError("INVALID_CONFIG", "Gashki state query envelope is incomplete", nil)
		}
	}
	if len(envelope) != 7 {
		return "", safeError("INVALID_CONFIG", "Gashki state query envelope has unknown fields", nil)
	}
	version, ok := envelope["tool_version"].(string)
	if !ok || version == "" {
		return "", safeError("INVALID_CONFIG", "Gashki state query version is invalid", nil)
	}
	errors, ok := envelope["errors"].([]any)
	if !ok || len(errors) != 0 {
		return "", safeError("INVALID_CONFIG", "Gashki state query has errors", nil)
	}
	for _, key := range []string{"commands", "warnings"} {
		if _, ok := envelope[key].([]any); !ok {
			return "", safeError("INVALID_CONFIG", "Gashki state query lists are invalid", nil)
		}
	}
	meta, ok := envelope["meta"].(map[string]any)
	if !ok || meta["contract_version"] != "2" {
		return "", safeError("INVALID_CONFIG", "Gashki state query contract differs", nil)
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok || len(data) != 2 || data["key"] != "state_dir" {
		return "", safeError("INVALID_CONFIG", "Gashki state query data is invalid", nil)
	}
	path, ok := data["value"].(string)
	if !ok || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", safeError("INVALID_CONFIG", "Gashki state query value is invalid", nil)
	}
	return path, nil
}
func recordObservation(o store.FileObservation, path string) map[string]any {
	return map[string]any{"path": path, "sha256": o.Hash, "bytes": o.Bytes, "type": o.Kind, "device": o.Device, "inode": o.Inode}
}
func Prepare(ctx context.Context, o PrepareOptions) (Prepared, error) {
	result := Prepared{Settings: o.Resolved}
	if o.Env == nil {
		return result, safeError("INVALID_CONFIG", "Explicit caller environment is required", nil)
	}
	if o.Store == nil {
		return result, fmt.Errorf("Owned store is required")
	}
	caller := CaptureIdentity(o.Env)
	record := PreparationRecord{RecordVersion: 1, Backend: o.Resolved.Settings.Backend, Caller: caller, Executables: make(map[string]ExecutableBinding), Billing: o.Billing, RequestedModel: map[string]string{"claude": o.Resolved.Settings.ClaudeModel, "codex": o.Resolved.Settings.CodexModel}, RequestedEffort: map[string]string{"claude": o.Resolved.Settings.ClaudeEffort, "codex": o.Resolved.Settings.CodexEffort}, ObservedModel: map[string]*string{"claude": nil, "codex": nil}, ObservedEffort: map[string]*string{"claude": nil, "codex": nil}}
	m, _, err := o.Store.LoadSnapshot()
	if err != nil {
		return result, err
	}
	record.RunID = m.String("run_id")
	record.ObservationReason = "No provider conversation has run; model and effort remain unobserved"
	_, record.RemovedEnvironmentNames = process.ChildEnvironment(o.Env, record.Backend)
	selected, err := observeInput(o.Resolved.SelectedFile)
	if err != nil {
		return result, err
	}
	if selected.Exists != o.Resolved.Exists || selected.Exists && selected.Hash != o.Resolved.SelectedHash {
		return result, safeError("ARTIFACT_CHANGED", "Resolved Volley input changed before preparation", map[string]any{"path": selected.Path})
	}
	inputs := []initialObservation{selected}
	before := func() error { return checkInputs(inputs) }
	active := []string{"claude", "codex"}
	if record.Backend == "gashki" {
		active = []string{"gashki"}
		source, err := gashkiSourcePath(o.Resolved.Settings, o.Env)
		if err != nil {
			return result, err
		}
		observed, err := observeInput(source)
		if err != nil {
			return result, err
		}
		if o.Resolved.Settings.GashkiConfig != "" && !observed.Exists {
			return result, safeError("INVALID_CONFIG", "Named Gashki source is missing", map[string]any{"path": source})
		}
		inputs = append(inputs, observed)
	}
	for _, name := range active {
		path := o.Resolved.Settings.ClaudeBin
		if name == "codex" {
			path = o.Resolved.Settings.CodexBin
		}
		if name == "gashki" {
			path = o.Resolved.Settings.GashkiBin
		}
		binding, err := bindExecutable(ctx, o, path, before)
		if err != nil {
			return result, err
		}
		record.Executables[name] = binding
		switch name {
		case "claude":
			result.Settings.Settings.ClaudeBin = binding.Path
		case "codex":
			result.Settings.Settings.CodexBin = binding.Path
		case "gashki":
			result.Settings.Settings.GashkiBin = binding.Path
		}
	}
	var gashkiBytes []byte
	if record.Backend == "gashki" {
		source := o.Resolved.Settings.GashkiConfig
		text, err := gashkiQuery(ctx, o, record.Executables["gashki"], source, []string{"config", "show", "--toml"}, before)
		if err != nil {
			return result, err
		}
		gashkiBytes = []byte(text)
		mechanism, err := parseGashkiRecord(gashkiBytes)
		if err != nil {
			return result, safeError("INVALID_CONFIG", "Gashki settings record is invalid", nil)
		}
		raw, err := gashkiQuery(ctx, o, record.Executables["gashki"], source, []string{"config", "get", "state_dir", "--json"}, before)
		if err != nil {
			return result, err
		}
		state, err := stateDirValue(raw)
		if err != nil {
			return result, err
		}
		if state != mechanism.StateDir {
			return result, safeError("ARTIFACT_CHANGED", "Gashki source mechanism changed", nil)
		}
		result.StateDir = state
		record.Mechanism = &mechanism
		socket := ""
		if mechanism.Socket != nil {
			socket = *mechanism.Socket
		}
		server := ProbeServer(ctx, o.Runner, o.TmuxPath, o.Env, o.Store.Path, socket, o.CallerWindow, before)
		if err := before(); err != nil {
			return result, err
		}
		record.Server = &server
		result.Settings.Settings.GashkiConfig = filepath.Join(o.Store.Path, "gashki.config.toml")
	}
	record.Roots = ResolveRoots(caller, record.Server)
	if err := GuardBilling(ctx, record.Roots, o.Env, o.Store.Path, o.Billing, o.ExtraGuardSettings); err != nil {
		return result, err
	}
	if o.Billing.Allowed != o.Resolved.Settings.AllowAPIKey {
		return result, safeError("INVALID_CONFIG", "Billing selection differs from resolved setting", nil)
	}
	volleyBytes, err := result.Settings.TOML()
	if err != nil {
		return result, err
	}
	if err := before(); err != nil {
		return result, err
	}
	id, err := store.NewID()
	if err != nil {
		return result, err
	}
	var artifacts []store.Artifact
	records := make(map[string]store.FileObservation)
	stage := func(name string, b []byte) (store.FileObservation, error) {
		existing, e := o.Store.Observe(name)
		if e == nil {
			if existing.Hash != contract.HashBytes(b) {
				return existing, safeError("OUTPUT_CONFLICT", "Existing settings record differs; use a fresh workspace", map[string]any{"path": name})
			}
			info, err := os.Lstat(filepath.Join(o.Store.Path, name))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				return existing, safeError("OUTPUT_CONFLICT", "Existing settings record is not a private owned record; use a fresh workspace", map[string]any{"path": name})
			}
			return existing, nil
		}
		if !os.IsNotExist(e) {
			return existing, e
		}
		path := "state/turns/" + id + "/" + name
		hash, e := o.Store.StagePrivateText(path, b)
		if e != nil {
			return existing, e
		}
		observed, e := o.Store.Observe(path)
		if e != nil {
			return existing, e
		}
		observed.Path = name
		artifacts = append(artifacts, store.Artifact{StagedPath: path, TargetPath: name, Hash: hash})
		return observed, nil
	}
	volleyObs, err := stage("volley.config.toml", volleyBytes)
	if err != nil {
		return result, err
	}
	records["volley.config.toml"] = volleyObs
	m["config_records"] = map[string]any{"volley": recordObservation(volleyObs, filepath.Join(o.Store.Path, "volley.config.toml")), "gashki": map[string]any{"path": "", "sha256": "", "bytes": 0, "type": "absent", "device": 0, "inode": 0}}
	if record.Backend == "gashki" {
		obs, err := stage("gashki.config.toml", gashkiBytes)
		if err != nil {
			return result, err
		}
		records["gashki.config.toml"] = obs
		m.Object("config_records")["gashki"] = recordObservation(obs, filepath.Join(o.Store.Path, "gashki.config.toml"))
	}
	m["identity"] = caller.manifestRecord()
	m["setting_sources"] = o.Resolved.Sources
	for _, role := range []string{"planner", "critic"} {
		session, _ := m.Object("sessions")[role].(map[string]any)
		if session != nil {
			session["reason"] = record.ObservationReason
		}
	}
	serverRecord := m.Object("server")
	if record.Server != nil {
		serverRecord["socket_path"] = record.Server.Socket
		serverRecord["home"] = record.Server.Context.Home.Value
		serverRecord["claude_root"] = variableRecord(record.Server.Context.ClaudeRoot)
		serverRecord["codex_root"] = variableRecord(record.Server.Context.CodexRoot)
		serverRecord["identity_reason"] = record.Server.Reason
		serverRecord["caller_window"] = o.CallerWindow
		serverRecord["state_dir"] = result.StateDir
		serverRecord["socket_device"] = record.Server.SocketDevice
		serverRecord["socket_inode"] = record.Server.SocketInode
		serverRecord["socket_kind"] = record.Server.SocketKind
	}
	executableRecords := m.Object("executables")
	for name, binding := range record.Executables {
		executableRecords[name] = map[string]any{"path": binding.Path, "version": binding.Version}
	}
	receiptBytes, err := contract.Canonical(record)
	if err != nil {
		return result, err
	}
	if err := contract.Validate("preparation", record); err != nil {
		return result, err
	}
	receiptStage := "state/turns/" + id + "/preparation.json"
	hash, err := o.Store.StagePrivateText(receiptStage, receiptBytes)
	if err != nil {
		return result, err
	}
	obs, err := o.Store.Observe(receiptStage)
	if err != nil {
		return result, err
	}
	target := "state/control/preparation-" + id + ".json"
	m["preparation"] = recordObservation(obs, target)
	artifacts = append(artifacts, store.Artifact{StagedPath: receiptStage, TargetPath: target, Hash: hash})
	if err := before(); err != nil {
		return result, err
	}
	tx, err := o.Store.NewTransaction("control", m, artifacts, nil)
	if err != nil {
		return result, err
	}
	if err := o.Store.CommitTransaction(tx); err != nil {
		return result, err
	}
	gate := &FrozenGate{store: o.Store, expected: records, executables: record.Executables}
	result.Gate = gate
	result.Record = record
	if record.Backend == "gashki" {
		frozen := result.Settings.Settings.GashkiConfig
		var text string
		err := gate.Call("gashki config", func() error {
			var e error
			text, e = gashkiQuery(ctx, o, record.Executables["gashki"], frozen, []string{"config", "show", "--toml"}, gate.Check)
			return e
		})
		if err != nil {
			return result, err
		}
		mechanism, err := parseGashkiRecord([]byte(text))
		if err != nil || !reflect.DeepEqual(mechanism, *record.Mechanism) {
			return result, safeError("ARTIFACT_CHANGED", "Frozen Gashki mechanism differs", nil)
		}
		var raw string
		err = gate.Call("gashki config", func() error {
			var e error
			raw, e = gashkiQuery(ctx, o, record.Executables["gashki"], frozen, []string{"config", "get", "state_dir", "--json"}, gate.Check)
			return e
		})
		if err != nil {
			return result, err
		}
		state, err := stateDirValue(raw)
		if err != nil || state != result.StateDir {
			return result, safeError("ARTIFACT_CHANGED", "Frozen Gashki state_dir differs", nil)
		}
	}
	return result, nil
}

// FrozenGate retains trusted observations. It never reloads an altered manifest.
type FrozenGate struct {
	store       *store.Store
	expected    map[string]store.FileObservation
	executables map[string]ExecutableBinding
}

func (g *FrozenGate) Check() error {
	for path, before := range g.expected {
		after, err := g.store.Observe(path)
		if err != nil || before != after {
			return safeError("ARTIFACT_CHANGED", "Frozen settings record changed", map[string]any{"actor": "external_or_unknown", "path": path, "expected_hash": before.Hash, "observed_hash": after.Hash})
		}
	}
	for _, binding := range g.executables {
		_, err := config.LookupExecutable(binding.Path)
		if err != nil {
			return safeError("DEPENDENCY_MISSING", "Saved executable is unavailable", map[string]any{"path": binding.Path})
		}
	}
	return nil
}
func (g *FrozenGate) Call(kind string, call func() error) error {
	if err := g.Check(); err != nil {
		return err
	}
	switch kind {
	case "direct launch", "gashki config", "gashki spawn", "gashki send", "gashki wait", "gashki observe", "gashki status", "gashki kill":
	default:
		return safeError("INVALID_INPUT", "Unknown external call kind", nil)
	}
	return call()
}

// PrepareAndCall releases a dependent callback only after preparation and the
// frozen-record check succeed. Concrete backends use the same gate later.
func PrepareAndCall(ctx context.Context, options PrepareOptions, kind string, call func(Prepared) error) (Prepared, error) {
	p, err := Prepare(ctx, options)
	if err != nil {
		return p, err
	}
	err = p.Gate.Call(kind, func() error { return call(p) })
	return p, err
}
func (p Prepared) CheckResume(current IdentityContext, currentServer *ServerContext, bindings map[string]ExecutableBinding) error {
	if err := p.Gate.Check(); err != nil {
		return err
	}
	if err := CompareIdentity(p.Record.Caller, current, p.Record.Server, currentServer); err != nil {
		return err
	}
	if !reflect.DeepEqual(p.Record.Executables, bindings) {
		return safeError("IDENTITY_CONFLICT", "Active executable binding changed", map[string]any{"reason": "path or version differs"})
	}
	return nil
}
func (p Prepared) ResumeCall(kind string, current IdentityContext, currentServer *ServerContext, bindings map[string]ExecutableBinding, call func() error) error {
	if err := p.CheckResume(current, currentServer, bindings); err != nil {
		return err
	}
	return p.Gate.Call(kind, call)
}
func (p Prepared) IncreaseCap(newCap int) error {
	if newCap < 1 || newCap > 9999 {
		return safeError("INVALID_INPUT", "Round cap is out of range", nil)
	}
	m, _, err := p.Gate.store.LoadSnapshot()
	if err != nil {
		return err
	}
	old := m["max_rounds"]
	var oldCap int
	_, _ = fmt.Sscan(fmt.Sprint(old), &oldCap)
	if newCap <= oldCap {
		return safeError("INVALID_INPUT", "Round cap must increase", nil)
	}
	m["max_rounds"] = newCap
	amendments, _ := m["amendments"].([]any)
	m["amendments"] = append(amendments, map[string]any{"revision": m.Revision() + 1, "max_rounds": newCap, "at": time.Now().UTC().Format(time.RFC3339Nano)})
	tx, err := p.Gate.store.NewTransaction("control", m, nil, nil)
	if err != nil {
		return err
	}
	return p.Gate.store.CommitTransaction(tx)
}

// DecodePreparation reads only the preparation record referenced by a retained,
// validated checkpoint. The caller supplies that checkpoint before any turn.
func DecodePreparation(s *store.Store, m store.Snapshot) (Prepared, error) {
	raw := m.Object("preparation")
	path, _ := raw["path"].(string)
	if path == "" {
		return Prepared{}, safeError("STATE_INVALID", "Preparation receipt is absent", nil)
	}
	_, b, err := s.ReadRecord("preparation", path)
	if err != nil {
		return Prepared{}, err
	}
	if contract.HashBytes(b) != raw["sha256"] {
		return Prepared{}, safeError("STATE_INVALID", "Preparation receipt changed", nil)
	}
	observed, err := s.Observe(path)
	if err != nil {
		return Prepared{}, err
	}
	expectedBytes, _ := contract.Canonical(raw)
	observedBytes, _ := contract.Canonical(recordObservation(observed, path))
	if !bytes.Equal(expectedBytes, observedBytes) {
		return Prepared{}, safeError("STATE_INVALID", "Preparation receipt identity changed", map[string]any{"path": path})
	}
	value, err := input.Record(bytes.NewReader(b), store.RecordLimit)
	if err != nil {
		return Prepared{}, err
	}
	if err := contract.Validate("preparation", value); err != nil {
		return Prepared{}, err
	}
	var record PreparationRecord
	if err := json.Unmarshal(b, &record); err != nil {
		return Prepared{}, err
	}
	if record.RunID != m.String("run_id") {
		return Prepared{}, safeError("STATE_INVALID", "Preparation run binding differs", nil)
	}
	expected := make(map[string]store.FileObservation)
	for _, key := range []string{"volley", "gashki"} {
		configRecord, _ := m.Object("config_records")[key].(map[string]any)
		if configRecord["type"] == "absent" {
			continue
		}
		name := key + ".config.toml"
		obs, err := s.Observe(name)
		if err != nil {
			return Prepared{}, err
		}
		if !reflect.DeepEqual(recordObservation(obs, filepath.Join(s.Path, name)), configRecord) {
			a, _ := contract.Canonical(recordObservation(obs, filepath.Join(s.Path, name)))
			z, _ := contract.Canonical(configRecord)
			if !bytes.Equal(a, z) {
				return Prepared{}, safeError("ARTIFACT_CHANGED", "Frozen checkpoint record differs", map[string]any{"path": name})
			}
		}
		expected[name] = obs
	}
	gate := &FrozenGate{s, expected, record.Executables}
	state := ""
	if record.Mechanism != nil {
		state = record.Mechanism.StateDir
	}
	return Prepared{Record: record, Gate: gate, StateDir: state}, nil
}

// ResumeWithRunner rechecks identity, billing inputs, and the active executable
// version through bounded metadata calls before releasing a dependent action.
func (p Prepared) ResumeWithRunner(ctx context.Context, env []string, server *ServerContext, runner process.Runner, kind string, call func() error) error {
	if err := p.Gate.Check(); err != nil {
		return err
	}
	caller := CaptureIdentity(env)
	if err := CompareIdentity(p.Record.Caller, caller, p.Record.Server, server); err != nil {
		return err
	}
	roots := ResolveRoots(caller, server)
	if err := GuardBilling(ctx, roots, env, p.Gate.store.Path, p.Record.Billing, nil); err != nil {
		return err
	}
	names := make([]string, 0, len(p.Record.Executables))
	for name := range p.Record.Executables {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := make(map[string]ExecutableBinding)
	for _, name := range names {
		saved := p.Record.Executables[name]
		binding, err := bindExecutable(ctx, PrepareOptions{Store: p.Gate.store, Runner: runner, Env: env}, saved.Path, p.Gate.Check)
		if err != nil {
			return err
		}
		bindings[name] = binding
	}
	return p.ResumeCall(kind, caller, server, bindings, call)
}
