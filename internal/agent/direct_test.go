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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/prompt"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

func TestDirectChild(t *testing.T) {
	start := -1
	for i, arg := range os.Args {
		if arg == "--direct-child" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return
	}
	provider := os.Args[start]
	args := os.Args[start+1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stdout, "owned direct stub 1.0")
		os.Exit(0)
	}
	variant := os.Getenv("F_DIRECT_VARIANT")
	input, err := io.ReadAll(os.Stdin)
	if err != nil || len(input) != 0 {
		os.Exit(92)
	}
	cwd, _ := os.Getwd()
	capture := map[string]any{"provider": provider, "argv": args, "cwd": cwd, "stdin_bytes": len(input)}
	b, _ := json.Marshal(capture)
	id := os.Getenv("F_DIRECT_TURN")
	if err := os.WriteFile(filepath.Join(os.Getenv("F_DIRECT_CAPTURE"), id+".json"), b, 0600); err != nil {
		os.Exit(93)
	}
	f, err := os.OpenFile(os.Getenv("F_DIRECT_COUNTER"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		os.Exit(94)
	}
	_, _ = f.WriteString(provider + "\n")
	_ = f.Close()
	// Keep the owned child alive until the shared runner can record identity.
	time.Sleep(100 * time.Millisecond)
	if variant == "hold-before-event" {
		time.Sleep(10 * time.Second)
	}
	if variant == "fail-resume" {
		fmt.Fprintln(os.Stderr, "owned resume refused")
		os.Exit(23)
	}
	if provider == "codex" {
		session := "11111111-2222-4333-8444-555555555555"
		if len(args) > 2 && args[0] == "exec" && args[1] == "resume" {
			session = args[2]
		}
		if variant == "bad-id" {
			session = "not-a-uuid"
		}
		if variant == "wrong-id" {
			session = "99999999-2222-4333-8444-555555555555"
		}
		if variant != "no-session" {
			fmt.Fprintf(os.Stdout, "{\"type\":\"thread.started\",\"thread_id\":%s}\n", strconv.Quote(session))
		}
		if variant == "duplicate-thread" {
			fmt.Fprintf(os.Stdout, "{\"type\":\"thread.started\",\"thread_id\":%s}\n", strconv.Quote(session))
		}
		if variant == "hold-after-event" {
			time.Sleep(10 * time.Second)
		}
		if variant == "malformed" {
			fmt.Fprintln(os.Stdout, "{invalid event}")
		}
		if variant == "partial-json" {
			fmt.Fprint(os.Stdout, "{\"type\":\"turn.completed\"}")
		}
		if variant != "missing-completion" && variant != "partial-json" {
			fmt.Fprintln(os.Stdout, "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
		}
		if variant != "missing-reply" {
			for i, arg := range args {
				if arg == "--output-last-message" && i+1 < len(args) {
					b := []byte("Owned final reply.\nVERDICT: APPROVE\n")
					if variant == "invalid-reply" {
						b = []byte{0xff}
					}
					if err := os.WriteFile(args[i+1], b, 0600); err != nil {
						os.Exit(95)
					}
				}
			}
		}
	} else if variant != "missing-reply" {
		if variant == "invalid-reply" {
			_, _ = os.Stdout.Write([]byte{0xff})
		} else {
			fmt.Fprintln(os.Stdout, "Owned final reply.\nVERDICT: APPROVE")
		}
	}
	if os.Getenv("F_DIRECT_ROLE") == "planner" {
		spec := []byte("# SPEC\nOwned planner output.\n")
		if variant == "invalid-spec" {
			spec = []byte{0xff}
		}
		if variant == "empty-spec" {
			spec = []byte(" \n\t")
		}
		if variant != "missing-spec" {
			if err := os.WriteFile("SPEC.md", spec, 0600); err != nil {
				os.Exit(96)
			}
		}
	}
	os.Exit(0)
}

type directFixture struct {
	Adapter          *Direct
	Options          PrepareOptions
	Request          review.TurnRequest
	Registrations    []PrimitiveRegistration
	Counter, Capture string
}

func newDirectFixture(t *testing.T, provider, role string, persistent, explicit bool) *directFixture {
	t.Helper()
	o, _ := prepareFixture(t, "cli")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		wrapper := "#!/bin/sh\nexec " + strconv.Quote(exe) + " -test.run=^TestDirectChild$ -- --direct-child " + name + " \"$@\"\n"
		path := o.Resolved.Settings.ClaudeBin
		if name == "codex" {
			path = o.Resolved.Settings.CodexBin
		}
		if err := os.WriteFile(path, []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
	}
	o.Resolved.Settings.Persistent = persistent
	if explicit {
		o.Resolved.Settings.ClaudeModel = "owned-model[1m]"
		o.Resolved.Settings.ClaudeEffort = "high"
		o.Resolved.Settings.CodexModel = "owned-model"
		o.Resolved.Settings.CodexEffort = "high"
	}
	o.Runner = process.UnixRunner{}
	o.Env = append(o.Env, "GORACE=atexit_sleep_ms=0")
	p, err := Prepare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := store.NewID()
	capture := t.TempDir()
	f := &directFixture{Options: o, Counter: filepath.Join(capture, "launches.txt"), Capture: capture}
	f.Request = review.TurnRequest{RunID: p.Record.RunID, TurnID: turn, Purpose: "draft", Role: role, Provider: provider, Round: 1, Attempt: 1, Workspace: o.Store.Path, Prompt: []byte("Owned prompt {{UNKNOWN}}\n"), ExpectedArtifacts: []string{"SPEC.md"}, Timeout: 3 * time.Second}
	if role == "critic" {
		f.Request.Purpose = "critique"
		f.Request.ExpectedArtifacts = []string{"rounds/r01.critique.md"}
	}
	f.Request.PromptHash = contract.HashBytes(f.Request.Prompt)
	env := append(append([]string{}, o.Env...), "F_DIRECT_ROLE="+role, "F_DIRECT_TURN="+turn, "F_DIRECT_CAPTURE="+capture, "F_DIRECT_COUNTER="+f.Counter)
	f.Adapter, err = NewDirect(DirectOptions{Prepared: p, Store: o.Store, Runner: process.UnixRunner{}, Env: env, InheritedChecked: true, Register: func(r PrimitiveRegistration) error { f.Registrations = append(f.Registrations, r); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		artifacts := map[string]any{}
		started := 0
		_ = filepath.WalkDir(filepath.Join(o.Store.Path, "state", "turns"), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(o.Store.Path, path)
			artifacts[rel] = map[string]any{"sha256": contract.HashBytes(b), "bytes": len(b)}
			if entry.Name() == "process-exit.json" {
				var result process.Result
				if json.Unmarshal(b, &result) == nil && result.Started {
					started++
				}
			}
			return nil
		})
		captures := []json.RawMessage{}
		entries, _ := os.ReadDir(f.Capture)
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".json" {
				b, err := os.ReadFile(filepath.Join(f.Capture, entry.Name()))
				if err == nil {
					captures = append(captures, json.RawMessage(b))
				}
			}
		}
		b, _ := json.Marshal(map[string]any{"fixture": "F-DIRECT", "provider": provider, "role": role, "persistent": persistent, "explicit_model_effort": explicit, "owned_started_processes": started, "stub_argv_captures": f.calls(), "captured_argv": captures, "artifact_hashes": artifacts})
		t.Logf("F-DIRECT evidence: %s", b)
	})
	return f
}
func (f *directFixture) calls() int {
	b, _ := os.ReadFile(f.Counter)
	return bytes.Count(b, []byte("\n"))
}
func (f *directFixture) variant(name string) {
	f.Adapter.options.Env = append(f.Adapter.options.Env, "F_DIRECT_VARIANT="+name)
}
func (f *directFixture) next(session string) {
	id, _ := store.NewID()
	f.Request.TurnID = id
	f.Request.SessionID = session
	f.Request.Round++
	f.Adapter.options.Env = append(f.Adapter.options.Env, "F_DIRECT_TURN="+id)
}
func argumentValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestADIRECT01ActualProcesses(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, role := range []string{"planner", "critic"} {
			for _, persistent := range []bool{false, true} {
				for _, explicit := range []bool{false, true} {
					t.Run(fmt.Sprintf("A-DIRECT-01/%s/%s/persistent=%t/explicit=%t", provider, role, persistent, explicit), func(t *testing.T) {
						f := newDirectFixture(t, provider, role, persistent, explicit)
						p, err := f.Adapter.Prepare(context.Background(), f.Request)
						if err != nil {
							t.Fatal(err)
						}
						if provider == "claude" && persistent {
							if !validUUID(p.IntendedIdentity) {
								t.Fatal("Intended UUID")
							}
							if _, err := f.Options.Store.ReadText(sessionPath(f.Request, p.IntendedIdentity)); err != nil {
								t.Fatal("Identity not saved before launch", err)
							}
						}
						out, err := f.Adapter.Perform(context.Background(), p)
						if err != nil || out.Kind != review.Completed || f.calls() != 1 {
							t.Fatal(out, err, "calls", f.calls())
						}
						var captured struct {
							Provider   string
							Argv       []string
							Cwd        string
							StdinBytes int `json:"stdin_bytes"`
						}
						b, err := os.ReadFile(filepath.Join(f.Capture, f.Request.TurnID+".json"))
						if err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(b, &captured); err != nil {
							t.Fatal(err)
						}
						if captured.Cwd != f.Options.Store.Path || captured.StdinBytes != 0 || captured.Provider != provider {
							t.Fatal("cwd/stdin/provider binding", captured)
						}
						if out.Reply.Kind != "found" || len(out.ArtifactHashes) != 1 {
							t.Fatal("Output/artifact facts")
						}
						if out.Reply.Root != f.Options.Store.Path || out.Reply.Source == "" || out.Reply.Format != provider+"-owned-output-v1" || out.Reply.Version == "" {
							t.Fatal("Reply source and format facts are missing", out.Reply)
						}
						if provider == "claude" {
							if argumentValue(p.Argv, "--permission-mode") != "dontAsk" || argumentValue(p.Argv, "--output-format") != "text" {
								t.Fatal("Claude print mode")
							}
							if strings.Count(strings.Join(p.Argv, "\n"), "--settings") != 1 {
								t.Fatal("Multiple settings objects")
							}
						}
						if provider == "codex" {
							want := "read-only"
							if role == "planner" {
								want = "workspace-write"
							}
							if argumentValue(p.Argv, "--sandbox") != want || !validUUID(out.Completion.SessionID) {
								t.Fatal("Codex sandbox/session")
							}
						}
						if explicit && provider == "claude" {
							var s ClaudeSettings
							_ = json.Unmarshal([]byte(argumentValue(p.Argv, "--settings")), &s)
							if s.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "0" {
								t.Fatal("Merged 1m override")
							}
						}
						if persistent {
							f.next(out.Completion.SessionID)
							next, err := f.Adapter.Prepare(context.Background(), f.Request)
							if err != nil {
								t.Fatal(err)
							}
							if provider == "claude" && argumentValue(next.Argv, "--resume") != f.Request.SessionID {
								t.Fatal("Claude exact resume")
							}
							if provider == "codex" && (next.Argv[2] != "resume" || next.Argv[3] != f.Request.SessionID || argumentValue(next.Argv, "--sandbox") != "" || argumentValue(next.Argv, "-c") != "sandbox_mode="+strconv.Quote(map[bool]string{true: "workspace-write", false: "read-only"}[role == "planner"])) {
								t.Fatal("Codex exact resume", next.Argv)
							}
							if _, err := f.Adapter.Perform(context.Background(), next); err != nil || f.calls() != 2 {
								t.Fatal("Resume result", err, f.calls())
							}
						}
					})
				}
			}
		}
	}
}

func TestADIRECT02InterruptionsAndProtocol(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run("before-launch/"+provider, func(t *testing.T) {
			f := newDirectFixture(t, provider, "planner", true, false)
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := f.Adapter.Perform(ctx, p); err == nil || f.calls() != 0 {
				t.Fatal("Canceled turn launched", err)
			}
		})
		t.Run("lost-initial/"+provider, func(t *testing.T) {
			f := newDirectFixture(t, provider, "planner", true, false)
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			dir := "state/turns/" + f.Request.TurnID
			if err := f.Adapter.save(dir+"/delivery-intent.json", map[string]any{"request_hash": "lost"}); err != nil {
				t.Fatal(err)
			}
			_, err = f.Adapter.Perform(context.Background(), p)
			requireErrorCode(t, err, "TURN_UNCERTAIN")
			if f.calls() != 0 {
				t.Fatal("Lost initial relaunched")
			}
			again, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil || again.IntendedIdentity != p.IntendedIdentity {
				t.Fatal("Replacement identity", err)
			}
		})
		t.Run("saved-result/"+provider, func(t *testing.T) {
			f := newDirectFixture(t, provider, "planner", true, false)
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			first, err := f.Adapter.Perform(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			second, err := f.Adapter.Perform(context.Background(), p)
			if err != nil || second.Reply.Hash != first.Reply.Hash || f.calls() != 1 {
				t.Fatal("Completed turn relaunched", err, f.calls())
			}
		})
		t.Run("failed-resume/"+provider, func(t *testing.T) {
			f := newDirectFixture(t, provider, "planner", true, false)
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.Adapter.Perform(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			f.next(out.Completion.SessionID)
			f.variant("fail-resume")
			p, err = f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.Adapter.Perform(context.Background(), p)
			requireErrorCode(t, err, "UPSTREAM_FAILURE")
			_, err = f.Adapter.Perform(context.Background(), p)
			requireErrorCode(t, err, "UPSTREAM_FAILURE")
			if p.IntendedIdentity != out.Completion.SessionID || f.calls() != 2 {
				t.Fatal("Failed resume replaced", f.calls())
			}
		})
	}
	for _, variant := range []string{"malformed", "partial-json", "duplicate-thread", "missing-completion", "bad-id", "no-session"} {
		t.Run("protocol/"+variant, func(t *testing.T) {
			f := newDirectFixture(t, "codex", "planner", true, false)
			f.variant(variant)
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.Adapter.Perform(context.Background(), p)
			if err == nil || out.Kind == review.Completed {
				t.Fatal("Protocol was accepted", out, err)
			}
			raw, err := f.Options.Store.ReadText("state/turns/" + f.Request.TurnID + "/stdout.pending")
			if err != nil || len(raw) == 0 {
				t.Fatal("Protocol evidence missing", err)
			}
			_, _ = f.Adapter.Perform(context.Background(), p)
			if f.calls() != 1 {
				t.Fatal("Protocol failure relaunched")
			}
		})
	}
	for _, variant := range []string{"hold-before-event", "hold-after-event"} {
		t.Run("interrupt/"+variant, func(t *testing.T) {
			f := newDirectFixture(t, "codex", "planner", true, false)
			f.variant(variant)
			f.Request.Timeout = 250 * time.Millisecond
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.Adapter.Perform(context.Background(), p)
			if err == nil || out.Kind == review.Completed {
				t.Fatal("Interrupted turn completed", err)
			}
			_, _ = f.Adapter.Perform(context.Background(), p)
			if f.calls() != 1 {
				t.Fatal("Interrupted turn relaunched")
			}
			_, readErr := f.Options.Store.ReadText("state/turns/" + f.Request.TurnID + "/session-created.json")
			if (readErr == nil) != (variant == "hold-after-event") {
				t.Fatal("Immediate session event record", readErr)
			}
		})
	}
}

func TestADIRECT04ArtifactValidation(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, role := range []string{"planner", "critic"} {
			for _, variant := range []string{"missing-reply", "missing-spec", "invalid-spec", "empty-spec"} {
				if role == "critic" && variant != "missing-reply" {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/%s", provider, role, variant), func(t *testing.T) {
					f := newDirectFixture(t, provider, role, false, false)
					f.variant(variant)
					p, err := f.Adapter.Prepare(context.Background(), f.Request)
					if err != nil {
						t.Fatal(err)
					}
					out, err := f.Adapter.Perform(context.Background(), p)
					if role == "planner" && variant == "missing-reply" {
						if err != nil || out.Kind != review.Completed || out.Reply.Kind != "missing" {
							t.Fatal("Optional reply blocked spec", out, err)
						}
					} else {
						requireErrorCode(t, err, "ARTIFACT_MISSING")
						if out.Kind == review.Completed {
							t.Fatal("Missing required artifact advanced")
						}
					}
				})
			}
		}
	}
}

func TestADIRECT03PermissionGrammar(t *testing.T) {
	root := "/owned/review [literal]*? (space)"
	settings := newDirectFixture(t, "claude", "planner", false, true).Adapter.options.Prepared.Settings.Settings
	for _, role := range []string{"planner", "critic"} {
		q := ArgumentOptions{Request: review.TurnRequest{Role: role, Provider: "claude", Workspace: root, Prompt: []byte("prompt")}, Settings: settings, Executable: "/owned/tools/claude", ReplyTemp: root + "/state/turns/final.pending", Skills: prompt.SkillPlan{ReadRoots: []string{"/owned/skills/link", "/owned/skills/target"}, SystemInstruction: "Read skills at /owned/skills/link."}, CommittedHistory: []string{"rounds/r01.critique.md", "rounds/r01.spec.md"}, InheritedChecked: true}
		args, s, err := BuildDirectArguments(q)
		if err != nil {
			t.Fatal(err)
		}
		if argumentValue(args, "--tools") == "" || argumentValue(args, "--allowedTools") != "" {
			t.Fatal("Availability flag")
		}
		for _, rule := range s.Permissions.Allow {
			if rule == "Edit" || rule == "Write" || strings.HasPrefix(rule, "Write(") {
				t.Fatal("Unscoped write permission")
			}
			if role == "critic" && strings.HasPrefix(rule, "Edit(") {
				t.Fatal("Critic write permission")
			}
		}
		if !strings.Contains(strings.Join(s.Permissions.Allow, "\n"), `\[literal\]\*\?`) {
			t.Fatal("Permission pattern broadened")
		}
		if role == "planner" {
			for _, name := range []string{"state/**)", "volley.config.toml)", "gashki.config.toml)", "BRIEF.md)", "CONSTRAINTS.md)", "rounds/r01.critique.md)", "rounds/r01.spec.md)"} {
				if !strings.Contains(strings.Join(s.Permissions.Deny, "\n"), name) {
					t.Fatal("Protected deny missing", name)
				}
			}
		}
		for _, bad := range []string{"Edit", "Write", "Edit(//**)", "Write(//owned/**)", "NotebookEdit"} {
			q.InheritedAllow = []string{bad}
			if _, _, err := BuildDirectArguments(q); err == nil {
				t.Fatal("Broader inherited write accepted", bad)
			}
		}
		q.InheritedAllow = nil
		q.InheritedChecked = false
		if _, _, err := BuildDirectArguments(q); err == nil {
			t.Fatal("Unknown inherited permissions accepted")
		}
	}
	for _, tools := range [][]string{{"Read", "Bash"}, {"Read", "unknown"}, {"Read", "Read"}, {"Read", "Write"}, {"Read", "Edit"}} {
		if err := directTools("critic", tools); err == nil {
			t.Fatal("Invalid critic tools", tools)
		}
	}
	t.Log("Owned stubs prove argument construction. Vendor permission enforcement is unverified.")
}

type directGuardSpy struct{ Calls int }

func (s *directGuardSpy) Execute(ctx context.Context, _ review.TurnRequest, operation review.Operation) (review.TurnOutcome, error) {
	s.Calls++
	return operation(ctx)
}
func TestADIRECT05GateAndRegistration(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, role := range []string{"planner", "critic"} {
			t.Run(provider+"/"+role, func(t *testing.T) {
				f := newDirectFixture(t, provider, role, false, false)
				p, err := f.Adapter.Prepare(context.Background(), f.Request)
				if err != nil {
					t.Fatal(err)
				}
				guard := &directGuardSpy{}
				if _, err := f.Adapter.Execute(context.Background(), p, guard); err != nil {
					t.Fatal(err)
				}
				if guard.Calls != 1 || len(f.Registrations) == 0 {
					t.Fatal("Primitive bypassed guard or registration")
				}
				found := false
				for _, registration := range f.Registrations {
					if registration.Operation == "direct launch" {
						found = true
						if len(registration.ControllerPaths) < 7 {
							t.Fatal("Controller sinks missing")
						}
						if (len(registration.AgentPaths) == 1) != (provider == "codex") {
							t.Fatal("Agent final output sink")
						}
					}
				}
				if !found {
					t.Fatal("Launch registration missing")
				}
			})
			t.Run("changed-config/"+provider+"/"+role, func(t *testing.T) {
				f := newDirectFixture(t, provider, role, false, false)
				p, err := f.Adapter.Prepare(context.Background(), f.Request)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.Options.Store.Path, "volley.config.toml"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = f.Adapter.Perform(context.Background(), p)
				requireErrorCode(t, err, "ARTIFACT_CHANGED")
				if f.calls() != 0 {
					t.Fatal("Changed settings reached process")
				}
			})
			t.Run("failed-operation/"+provider+"/"+role, func(t *testing.T) {
				f := newDirectFixture(t, provider, role, false, false)
				variant := "missing-spec"
				if role == "critic" {
					variant = "missing-reply"
				}
				f.variant(variant)
				p, err := f.Adapter.Prepare(context.Background(), f.Request)
				if err != nil {
					t.Fatal(err)
				}
				guard := &directGuardSpy{}
				out, err := f.Adapter.Execute(context.Background(), p, guard)
				requireErrorCode(t, err, "ARTIFACT_MISSING")
				if guard.Calls != 1 || out.Kind != review.Failed || len(f.Registrations) == 0 {
					t.Fatal("Failed primitive bypassed guarded executor")
				}
			})
		}
	}
}

func TestDirectIdentityFaults(t *testing.T) {
	for _, site := range []string{"artifact-stage.file-fsync.before", "artifact-stage.file-fsync.after", "artifact-stage.publish.before", "artifact-stage.publish.after", "artifact-stage.dir-fsync.before", "artifact-stage.dir-fsync.after"} {
		t.Run("A-DIRECT-02/identity-fault/"+site, func(t *testing.T) {
			f := newDirectFixture(t, "codex", "planner", true, false)
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			var hit atomic.Bool
			prior := f.Adapter.options.Register
			f.Adapter.options.Register = func(r PrimitiveRegistration) error {
				if r.Operation == "record session identity" {
					f.Options.Store.Fault = func(s string) error {
						if s == site && hit.CompareAndSwap(false, true) {
							return fmt.Errorf("owned identity fault")
						}
						return nil
					}
				}
				return prior(r)
			}
			out, err := f.Adapter.Perform(context.Background(), p)
			if err == nil || !hit.Load() || out.Kind == review.Completed {
				t.Fatal("Identity fault advanced", out, err, hit.Load())
			}
			f.Options.Store.Fault = nil
			_, _ = f.Adapter.Perform(context.Background(), p)
			if f.calls() != 1 {
				t.Fatal("Identity fault duplicated launch")
			}
		})
	}
}

func TestDirectPermissionSourceInspection(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "settings.json")
	for name, text := range map[string]string{
		"bypass":              `{"permissions":{"defaultMode":"bypassPermissions"}}`,
		"duplicate":           `{"permissions":{"allow":[],"allow":[]}}`,
		"invalid-rule-type":   `{"permissions":{"allow":[1]}}`,
		"invalid-permissions": `{"permissions":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(source, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectPermissionSources([]string{source}); err == nil {
				t.Fatal("Invalid inherited source accepted")
			}
		})
	}
	if err := os.WriteFile(source, []byte(`{"apiKeyHelper":"private-marker-not-retained","permissions":{"allow":["Read(//owned/**)"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := InspectPermissionSources([]string{source, filepath.Join(root, "absent.json")})
	if err != nil || len(p.Allow) != 1 || len(p.Sources) != 2 {
		t.Fatal(p, err)
	}
	if strings.Contains(strings.Join(p.Allow, "\n"), "private-marker") {
		t.Fatal("Source credential retained")
	}
	if err := os.WriteFile(source, []byte(`{"permissions":{"allow":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkInputs(p.Sources); err == nil {
		t.Fatal("Changed source observation accepted")
	}
	f := newDirectFixture(t, "claude", "planner", false, false)
	q, err := f.Adapter.Prepare(context.Background(), f.Request)
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(f.Options.Store.Path, ".claude")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(`{"permissions":{"allow":["Edit"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.Adapter.Perform(context.Background(), q)
	requireErrorCode(t, err, "ARTIFACT_CHANGED")
	if f.calls() != 0 {
		t.Fatal("Changed inherited permissions launched")
	}
}

func TestDirectRecoveredArtifactAndSessionConflicts(t *testing.T) {
	f := newDirectFixture(t, "claude", "planner", true, false)
	p, err := f.Adapter.Prepare(context.Background(), f.Request)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.Adapter.Perform(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Options.Store.Path, "SPEC.md"), []byte("changed after completion"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.Adapter.Perform(context.Background(), p)
	requireErrorCode(t, err, "ARTIFACT_CHANGED")
	if f.calls() != 1 {
		t.Fatal("Changed completed spec duplicated process")
	}
	f.next(out.Completion.SessionID)
	f.Adapter.options.Prepared.Record.RequestedModel = map[string]string{"claude": "changed-model", "codex": ""}
	_, err = f.Adapter.Prepare(context.Background(), f.Request)
	requireErrorCode(t, err, "IDENTITY_CONFLICT")
	if f.calls() != 1 {
		t.Fatal("Changed session configuration launched")
	}
}

func TestDirectCurrentBillingIndicator(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newDirectFixture(t, provider, "planner", false, false)
			p, err := f.Adapter.Prepare(context.Background(), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			f.Adapter.options.Env = append(f.Adapter.options.Env, "CODEX_API_KEY=owned-not-a-credential")
			_, err = f.Adapter.Perform(context.Background(), p)
			requireErrorCode(t, err, "BILLING_REFUSED")
			if f.calls() != 0 {
				t.Fatal("New billing indicator reached process")
			}
			if strings.Contains(err.Error(), "owned-not-a-credential") {
				t.Fatal("Indicator value in diagnostic")
			}
		})
	}
}

func TestDirectWrongResumeIdentity(t *testing.T) {
	f := newDirectFixture(t, "codex", "planner", true, false)
	p, err := f.Adapter.Prepare(context.Background(), f.Request)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.Adapter.Perform(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	f.next(out.Completion.SessionID)
	f.variant("wrong-id")
	p, err = f.Adapter.Prepare(context.Background(), f.Request)
	if err != nil {
		t.Fatal(err)
	}
	out, err = f.Adapter.Perform(context.Background(), p)
	requireErrorCode(t, err, "IDENTITY_CONFLICT")
	if out.Kind != review.IdentityConflict {
		t.Fatal("Identity conflict classification", out.Kind)
	}
	_, err = f.Adapter.Perform(context.Background(), p)
	requireErrorCode(t, err, "IDENTITY_CONFLICT")
	if f.calls() != 2 {
		t.Fatal("Conflicting resume replaced session")
	}
}

func TestDirectInvalidReply(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, role := range []string{"planner", "critic"} {
			t.Run(provider+"/"+role, func(t *testing.T) {
				f := newDirectFixture(t, provider, role, false, false)
				f.variant("invalid-reply")
				p, err := f.Adapter.Prepare(context.Background(), f.Request)
				if err != nil {
					t.Fatal(err)
				}
				out, err := f.Adapter.Perform(context.Background(), p)
				if role == "planner" {
					if err != nil || out.Kind != review.Completed || out.ArtifactHashes["SPEC.md"] == "" {
						t.Fatal("Optional invalid reply blocked valid spec", err)
					}
				} else {
					requireErrorCode(t, err, "ARTIFACT_MISSING")
					if out.Kind == review.Completed {
						t.Fatal("Invalid critique advanced")
					}
				}
				if out.Reply.Kind != "read-failed" {
					t.Fatal("Invalid reply classification", out.Reply)
				}
				if _, err := f.Options.Store.ReadText("state/turns/" + f.Request.TurnID + "/final.txt"); !os.IsNotExist(err) {
					t.Fatal("Invalid reply was promoted", err)
				}
				path := "state/turns/" + f.Request.TurnID + "/stdout.pending"
				if provider == "codex" {
					path = "state/turns/" + f.Request.TurnID + "/final.pending"
				}
				raw, _, err := f.Options.Store.ReadRaw(path)
				if err != nil || !bytes.Equal(raw, []byte{0xff}) {
					t.Fatal("Invalid raw reply evidence lost", err)
				}
			})
		}
	}
}
