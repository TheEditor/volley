//go:build darwin || linux

package gashki

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
)

type SourceProof struct{ SourceCommit, ArchiveHash, BinaryPath string }
type ClientOptions struct {
	RunID          string
	Store          *store.Store
	Runner         process.Runner
	Binary, Config string
	Env            []string
	// Gate checks frozen records, executable identity, billing sources and the
	// effective identity before each subprocess. Registration names exact files.
	Gate     func(context.Context, string) error
	Register func([]string) error
	// OnWrite confirms exact controller bytes after a successful owned write.
	OnWrite func(string, store.FileObservation) error
	// BeforeMutation must commit and validate the outer engine intent. It runs
	// after the primitive intent is durable and before releasing the process.
	BeforeMutation func(context.Context, CallIntent) error
	SourceProof    *SourceProof
}
type Client struct {
	options       ClientOptions
	Protocol      *Protocol
	sourceChecked bool
	stateDir      string
	socketName    *string
	preflight     bool
	configHash    string
}
type CallIntent struct {
	ID           string   `json:"id"`
	Verb         string   `json:"verb"`
	Args         []string `json:"args"`
	InputHash    string   `json:"input_hash"`
	ConfigHash   string   `json:"config_hash"`
	Binary       string   `json:"binary"`
	ContractHash string   `json:"contract_hash"`
}
type CheckedCall struct {
	ID              string         `json:"id"`
	Intent          CallIntent     `json:"intent"`
	Process         process.Result `json:"process"`
	RawPath         string         `json:"raw_path"`
	StderrPath      string         `json:"stderr_path"`
	RawHash         string         `json:"raw_hash"`
	StderrHash      string         `json:"stderr_hash"`
	ValidationError string         `json:"validation_error"`
	RunnerError     string         `json:"runner_error"`
	Missing         bool           `json:"missing"`
	Response        Response       `json:"response"`
}
type processReturn struct {
	Process     process.Result `json:"process"`
	RawHash     string         `json:"raw_hash"`
	StderrHash  string         `json:"stderr_hash"`
	RunnerError string         `json:"runner_error"`
}
type CallRequest struct {
	ID, Verb string
	Args     []string
	Input    []byte
	Budget   Budget
}

func NewClient(o ClientOptions) (*Client, error) {
	if !validID(o.RunID) || o.Store == nil || o.Runner == nil || o.Gate == nil || o.Register == nil || o.BeforeMutation == nil || !filepath.IsAbs(o.Binary) || !filepath.IsAbs(o.Config) || len(o.Env) == 0 {
		return nil, fmt.Errorf("Incomplete checked Gashki client context")
	}
	if e := o.Gate(context.Background(), "gashki config"); e != nil {
		return nil, e
	}
	if e := checkExecutable(o.Binary); e != nil {
		return nil, e
	}
	o.Env = append([]string{}, o.Env...)
	p, e := LoadProtocol()
	if e != nil {
		return nil, e
	}
	c := &Client{options: o, Protocol: p}
	c.configHash, e = c.ConfigHash()
	if e != nil {
		return nil, e
	}
	if o.SourceProof != nil {
		proof := *o.SourceProof
		c.sourceChecked = proof.SourceCommit == SourcePin && proof.BinaryPath == o.Binary && proof.ArchiveHash == "7c67c1c039bf9fff4db5d6be1f2ee03f897b9e88a0262111f75b7c0fcea267b8"
	}
	return c, nil
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("Gashki command is not an available executable file")
	}
	return nil
}
func validID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}
func callPaths(id string) []string {
	base := "state/control/gk-call-" + id
	return []string{base + "-intent.json", base + "-start.json", base + "-stdout.raw", base + "-stderr.raw", base + "-exit.json", base + "-result.json"}
}
func ownedJSON(v any) ([]byte, error) { return contract.Canonical(v) }
func (c *Client) save(path string, v any) error {
	b, e := ownedJSON(v)
	if e != nil {
		return e
	}
	return c.writeText(path, b)
}

func (c *Client) writeText(path string, b []byte) error {
	if _, e := c.options.Store.StagePrivateText(path, b); e != nil {
		return e
	}
	return c.authorize(path, contract.HashBytes(b))
}

func (c *Client) authorize(path, expected string) error {
	if c.options.OnWrite == nil {
		return nil
	}
	_, observed, e := c.options.Store.ReadRaw(path)
	if e != nil {
		return e
	}
	if observed.Hash != expected {
		return NativeError(decision("ARTIFACT_CHANGED", "controller_sink_changed"), map[string]any{"path": path})
	}
	return c.options.OnWrite(path, observed)
}
func decodeClosed(b []byte, v any) error {
	if _, e := parse(b); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	return nil
}
func (c *Client) ConfigHash() (string, error) {
	b, e := c.options.Store.ReadText("gashki.config.toml")
	if e != nil {
		return "", e
	}
	if filepath.Join(c.options.Store.Path, "gashki.config.toml") != c.options.Config {
		return "", fmt.Errorf("Gashki config is not the committed workspace record")
	}
	h := contract.HashBytes(b)
	if c.configHash != "" && h != c.configHash {
		return "", NativeError(decision("ARTIFACT_CHANGED", "frozen_gashki_config_changed"), nil)
	}
	return h, nil
}
func (c *Client) StateDir() string { return c.stateDir }
func (c *Client) SocketName() *string {
	if c.socketName == nil {
		return nil
	}
	s := *c.socketName
	return &s
}
func (c *Client) SourceChecked() bool { return c.sourceChecked }

type limitedSink struct {
	Writer io.Writer
	Count  int64
	Digest hash.Hash
}

func (s *limitedSink) write(b []byte) (int, error) {
	n, e := s.Writer.Write(b)
	if s.Digest != nil && n > 0 {
		_, _ = s.Digest.Write(b[:n])
	}
	s.Count += int64(n)
	return n, e
}

func (s *limitedSink) Write(b []byte) (int, error) {
	if s.Count+int64(len(b)) > ResponseLimit {
		remaining := int64(ResponseLimit) - s.Count
		written := 0
		if remaining > 0 {
			n, e := s.write(b[:remaining])
			written = n
			if e != nil {
				return n, e
			}
		}
		return written, fmt.Errorf("Gashki output exceeds its bound")
	}
	return s.write(b)
}
func mutating(verb string) bool { return verb == "spawn" || verb == "send" || verb == "kill" }
func gateKind(verb string) string {
	if strings.HasPrefix(verb, "config ") || verb == "version" || verb == "capabilities" || verb == "schema" {
		return "gashki config"
	}
	return "gashki " + verb
}

func (c *Client) Call(ctx context.Context, q CallRequest) (CheckedCall, error) {
	var result CheckedCall
	if !validID(q.ID) || c.Protocol.Verb(q.Verb) == nil && q.Verb != "version" {
		return result, fmt.Errorf("Invalid Gashki call identity or operation")
	}
	parts := strings.Split(q.Verb, " ")
	if q.Verb == "version" {
		parts = []string{"--version"}
	}
	if len(q.Args) < len(parts) {
		return result, fmt.Errorf("Generated Gashki operation is incomplete")
	}
	for i, part := range parts {
		if q.Args[i] != part {
			return result, fmt.Errorf("Generated Gashki operation and declared verb differ")
		}
	}
	if !c.preflight && mutating(q.Verb) {
		return result, NativeError(decision("UPSTREAM_FAILURE", "preflight_not_complete"), nil)
	}
	for _, a := range q.Args {
		if a == "--config" || strings.HasPrefix(a, "--config=") || a == "--state-dir" || strings.HasPrefix(a, "--state-dir=") || a == "--tmux-socket" || strings.HasPrefix(a, "--tmux-socket=") || a == "--no-verify" || a == "--no-idempotency-key" {
			return result, fmt.Errorf("Prepared Gashki call cannot override its bindings")
		}
	}
	if e := c.options.Gate(ctx, gateKind(q.Verb)); e != nil {
		return result, e
	}
	if e := checkExecutable(c.options.Binary); e != nil {
		return result, NativeError(decision("DEPENDENCY_MISSING", "gashki_executable_unavailable"), nil)
	}
	h, e := c.ConfigHash()
	if e != nil {
		return result, e
	}
	intent := CallIntent{q.ID, q.Verb, append([]string{}, q.Args...), contract.HashBytes(q.Input), h, c.options.Binary, c.Protocol.Hash}
	paths := callPaths(q.ID)
	// A recorded return can be re-read. A launch intent without a recorded
	// return never releases this same process a second time.
	if b, e := c.options.Store.ReadText(paths[5]); e == nil {
		if e := decodeClosed(b, &result); e != nil {
			return result, e
		}
		if !reflectIntent(result.Intent, intent) {
			return result, NativeError(decision("IDEMPOTENCY_CONFLICT", "call_identity_reused"), nil)
		}
		return c.recoverCall(result)
	} else if !os.IsNotExist(e) {
		return result, e
	}
	if _, e := c.options.Store.ReadText(paths[0]); e == nil {
		return result, NativeError(decision("TURN_UNCERTAIN", "subprocess_return_not_recorded"), map[string]any{"call_id": q.ID})
	} else if !os.IsNotExist(e) {
		return result, e
	}
	if e := c.options.Register(paths); e != nil {
		return result, e
	}
	if e := c.save(paths[0], intent); e != nil {
		return result, e
	}
	if mutating(q.Verb) {
		if e := c.options.BeforeMutation(ctx, intent); e != nil {
			return result, e
		}
	}
	out, e := c.options.Store.BeginOutput(paths[2])
	if e != nil {
		return result, e
	}
	errout, e := c.options.Store.BeginOutput(paths[3])
	if e != nil {
		if finish := c.options.Store.FinishOutput(paths[2], out); finish != nil {
			return result, finish
		}
		if checked := c.authorize(paths[2], contract.HashBytes(nil)); checked != nil {
			return result, checked
		}
		return result, e
	}
	result = CheckedCall{ID: q.ID, Intent: intent, RawPath: paths[2], StderrPath: paths[3]}
	outSink, errSink := &limitedSink{Writer: out, Digest: sha256.New()}, &limitedSink{Writer: errout, Digest: sha256.New()}
	request := process.Request{Path: c.options.Binary, Args: append([]string{"--config=" + c.options.Config, "--json"}, q.Args...), Cwd: c.options.Store.Path, Env: c.options.Env, Timeout: q.Budget.Limit, ElapsedBefore: q.Budget.Elapsed(), Stdout: outSink, Stderr: errSink, OnStart: func(id process.Identity) error { return c.save(paths[1], id) }}
	request.Input = append([]byte{}, q.Input...)
	if q.Budget.Limit == 0 {
		request.ElapsedBefore = 0
	}
	if e := c.options.Gate(ctx, gateKind(q.Verb)); e != nil {
		// No process started. Confirm the two exact empty controller files so
		// the protected-file guard preserves the actual gate failure.
		for i, file := range []*os.File{out, errout} {
			path := paths[2+i]
			if finish := c.options.Store.FinishOutput(path, file); finish != nil {
				return result, finish
			}
			if checked := c.authorize(path, contract.HashBytes(nil)); checked != nil {
				return result, checked
			}
		}
		return result, e
	}
	result.Process, e = c.options.Runner.Run(ctx, request)
	if e != nil {
		result.RunnerError = e.Error()
	}
	e1, e2 := c.options.Store.FinishOutput(paths[2], out), c.options.Store.FinishOutput(paths[3], errout)
	if e1 != nil {
		return result, e1
	}
	if e2 != nil {
		return result, e2
	}
	raw, _, e := c.options.Store.ReadRaw(paths[2])
	if e != nil {
		return result, e
	}
	stderr, _, e := c.options.Store.ReadRaw(paths[3])
	if e != nil {
		return result, e
	}
	result.RawHash = contract.HashBytes(raw)
	result.StderrHash = contract.HashBytes(stderr)
	if result.RawHash != hex.EncodeToString(outSink.Digest.Sum(nil)) || result.StderrHash != hex.EncodeToString(errSink.Digest.Sum(nil)) {
		return result, NativeError(decision("ARTIFACT_CHANGED", "captured_upstream_output_changed"), nil)
	}
	if e := c.authorize(paths[2], result.RawHash); e != nil {
		return result, e
	}
	if e := c.authorize(paths[3], result.StderrHash); e != nil {
		return result, e
	}
	if e := c.save(paths[4], processReturn{result.Process, result.RawHash, result.StderrHash, result.RunnerError}); e != nil {
		return result, e
	}
	result.Missing = len(bytes.TrimSpace(raw)) == 0
	if result.Process.Outcome == process.Exited && result.Process.Settled && result.RunnerError == "" {
		result.Response, e = c.Protocol.Check(q.Verb, raw, result.Process.Exit)
		if e != nil {
			result.ValidationError = e.Error()
		}
	} else {
		result.ValidationError = "Gashki subprocess did not return a checked exit"
	}
	// A public response can contain floats. Store its raw binding and derive
	// the decoded response on read; it is not an integer-only owned record.
	saved := result
	saved.Response = Response{}
	if e := c.save(paths[5], saved); e != nil {
		return result, e
	}
	return result, nil
}
func reflectIntent(a, b CallIntent) bool {
	x, e1 := ownedJSON(a)
	y, e2 := ownedJSON(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}
func (c *Client) recoverCall(result CheckedCall) (CheckedCall, error) {
	paths := callPaths(result.ID)
	if !validID(result.ID) || result.Intent.ID != result.ID || result.RawPath != paths[2] || result.StderrPath != paths[3] {
		return result, NativeError(decision("STATE_INVALID", "call_record_path_binding_changed"), nil)
	}
	for _, pair := range []struct{ path, hash string }{{result.RawPath, result.RawHash}, {result.StderrPath, result.StderrHash}} {
		b, _, e := c.options.Store.ReadRaw(pair.path)
		if e != nil || contract.HashBytes(b) != pair.hash {
			return result, NativeError(decision("ARTIFACT_CHANGED", "upstream_raw_record_changed"), nil)
		}
		if e := c.options.Store.SyncRetained(pair.path); e != nil {
			return result, e
		}
	}
	b, e := c.options.Store.ReadText(paths[0])
	if e != nil {
		return result, e
	}
	var intent CallIntent
	if e := decodeClosed(b, &intent); e != nil || !reflectIntent(intent, result.Intent) {
		return result, NativeError(decision("STATE_INVALID", "call_intent_binding_changed"), nil)
	}
	b, e = c.options.Store.ReadText(paths[4])
	if e != nil {
		return result, e
	}
	var fact processReturn
	if e := decodeClosed(b, &fact); e != nil {
		return result, e
	}
	x, _ := ownedJSON(fact)
	y, _ := ownedJSON(processReturn{result.Process, result.RawHash, result.StderrHash, result.RunnerError})
	if !bytes.Equal(x, y) {
		return result, NativeError(decision("STATE_INVALID", "process_return_binding_changed"), nil)
	}
	raw, _, e := c.options.Store.ReadRaw(result.RawPath)
	if e != nil {
		return result, e
	}
	if result.Process.Outcome == process.Exited && result.Process.Settled && result.RunnerError == "" {
		response, err := c.Protocol.Check(result.Intent.Verb, raw, result.Process.Exit)
		if err == nil && result.ValidationError == "" {
			result.Response = response
		} else if err == nil {
			return result, NativeError(decision("STATE_INVALID", "stored_protocol_failure_changed"), nil)
		} else if result.ValidationError == "" {
			return result, err
		}
	}
	return result, nil
}

func (c *Client) FreshCall(ctx context.Context, verb string, input []byte, budget Budget, args ...string) (CheckedCall, error) {
	id, e := store.NewID()
	if e != nil {
		return CheckedCall{}, e
	}
	return c.Call(ctx, CallRequest{id, verb, args, input, budget})
}

// LoadCall uses a durable return record to settle an earlier owned subprocess.
// An absent start identity or PID does not substitute for that return record.
func (c *Client) LoadCall(id string) (CheckedCall, bool, error) {
	var result CheckedCall
	if !validID(id) {
		return result, false, fmt.Errorf("Invalid saved call identity")
	}
	paths := callPaths(id)
	b, e := c.options.Store.ReadText(paths[0])
	if os.IsNotExist(e) {
		return result, false, nil
	}
	if e != nil {
		return result, true, e
	}
	var intent CallIntent
	if e := decodeClosed(b, &intent); e != nil {
		return result, true, e
	}
	if intent.ID != id || intent.Binary != c.options.Binary || intent.ContractHash != c.Protocol.Hash || intent.ConfigHash != c.configHash {
		return result, true, NativeError(decision("IDEMPOTENCY_CONFLICT", "saved_call_binding_changed"), nil)
	}
	if b, e := c.options.Store.ReadText(paths[5]); e == nil {
		if e := decodeClosed(b, &result); e != nil {
			return result, true, e
		}
		result, e = c.recoverCall(result)
		return result, true, e
	} else if !os.IsNotExist(e) {
		return result, true, e
	}
	b, e = c.options.Store.ReadText(paths[4])
	if e != nil {
		return result, true, NativeError(decision("TURN_UNCERTAIN", "owned_subprocess_return_unsettled"), map[string]any{"call_id": id})
	}
	var fact processReturn
	if e := decodeClosed(b, &fact); e != nil {
		return result, true, e
	}
	result = CheckedCall{ID: id, Intent: intent, Process: fact.Process, RawPath: paths[2], StderrPath: paths[3], RawHash: fact.RawHash, StderrHash: fact.StderrHash, RunnerError: fact.RunnerError}
	raw, _, e := c.options.Store.ReadRaw(paths[2])
	if e != nil {
		return result, true, e
	}
	result.Missing = len(bytes.TrimSpace(raw)) == 0
	if fact.Process.Outcome == process.Exited && fact.Process.Settled && fact.RunnerError == "" {
		result.Response, e = c.Protocol.Check(intent.Verb, raw, fact.Process.Exit)
		if e != nil {
			result.ValidationError = e.Error()
		}
	} else {
		result.ValidationError = "Gashki subprocess did not return a checked exit"
	}
	result, e = c.recoverCall(result)
	return result, true, e
}
func (c *Client) Preflight(ctx context.Context) error {
	b := Budget{Limit: 10 * time.Second, Started: time.Now()}
	call := func(verb string, args ...string) (Response, error) {
		r, e := c.FreshCall(ctx, verb, nil, b, args...)
		if e != nil {
			return r.Response, e
		}
		if r.ValidationError != "" || !r.Response.OK {
			return r.Response, NativeError(decision("UPSTREAM_FAILURE", "preflight_response_unchecked"), map[string]any{"call_id": r.ID})
		}
		return r.Response, nil
	}
	if _, e := call("version", "--version"); e != nil {
		return e
	}
	caps, e := call("capabilities", "capabilities")
	if e != nil {
		return e
	}
	schemas, e := call("schema", "schema")
	if e != nil {
		return e
	}
	if e := c.Protocol.RequiredSubset(caps.Data, schemas.Data); e != nil {
		return e
	}
	show, e := call("config show", "config", "show")
	if e != nil {
		return e
	}
	state, e := call("config get", "config", "get", "state_dir")
	if e != nil {
		return e
	}
	if Map(state.Data)["key"] != "state_dir" || !reflect.DeepEqual(Map(state.Data)["value"], Map(Map(show.Data)["config"])["state_dir"]) {
		return fmt.Errorf("Gashki state query binding differs")
	}
	c.stateDir = String(Map(state.Data)["value"])
	if !filepath.IsAbs(c.stateDir) {
		return fmt.Errorf("Gashki state root is not absolute")
	}
	socket, e := call("config get", "config", "get", "tmux_socket")
	if e != nil {
		return e
	}
	if Map(socket.Data)["key"] != "tmux_socket" || !reflect.DeepEqual(Map(socket.Data)["value"], Map(Map(show.Data)["config"])["tmux_socket"]) {
		return fmt.Errorf("Gashki socket query binding differs")
	}
	if v := Map(socket.Data)["value"]; v != nil {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("Gashki socket selection is invalid")
		}
		c.socketName = &s
	}
	c.preflight = true
	return nil
}
