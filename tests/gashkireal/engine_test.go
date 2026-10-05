package gashkireal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/engine"
	"github.com/TheEditor/volley/internal/gashki"
	"github.com/TheEditor/volley/internal/human"
	"github.com/TheEditor/volley/internal/ops"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
)

func engineFixture(t *testing.T, planner, placement string, plan map[string]any) (*fixture, engine.Request, engine.Options) {
	f := newFixture(t)
	put(t, filepath.Join(f.root, "stub/engine-plan.json"), jsonBytes(plan))
	ws := filepath.Join(f.root, "ws")
	put(t, filepath.Join(ws, "BRIEF.md"), []byte("Create a small local plan.\n"))
	if placement == "here" {
		f.env = append(f.env, "TMUX="+f.socketPath+",1,0", "TMUX_PANE="+f.caller)
	}
	path := filepath.Join(f.root, "volley.toml")
	put(t, path, []byte(fmt.Sprintf("planner=%q\nbackend='gashki'\nmax_rounds=4\nwait_timeout='30s'\npoll_interval='100ms'\nclosing_pass=false\nsecond_opinion=false\nplacement=%q\ngashki_bin=%q\ngashki_config=%q\n", planner, placement, filepath.Join(f.dir, "gashki-release"), f.config)))
	r, _, err := config.Resolve(config.ResolveOptions{Cwd: f.root, Home: filepath.Join(f.root, "home"), NamedFile: path, Workspace: ws, Mutating: true})
	if err != nil {
		t.Fatal(err)
	}
	manifest := object(t, read(t, filepath.Join(f.dir, "fixture.json")))
	proof := &gashki.SourceProof{SourceCommit: pin, ArchiveHash: text(manifest, "source_archive_sha256"), BinaryHash: text(member(member(manifest, "binaries"), "gashki-release"), "sha256")}
	return f, engine.Request{Workspace: ws, Resolved: &r}, engine.Options{Env: f.env, TmuxPath: f.tmux, GashkiSourceProof: proof}
}

func TestEngineGashkiCursorResume(t *testing.T) {
	f, request, options := engineFixture(t, "claude", "normal", map[string]any{})
	options.Hook = func(name string, s *store.Store) error {
		if name == "engine.gashki.delivered" {
			r, _ := contract.Load()
			return r.Error("TURN_UNCERTAIN", "Owned controller stopped after confirmed delivery")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, err := engine.Run(ctx, request, options)
	var typed *contract.Error
	if !errors.As(err, &typed) || typed.Code != "TURN_UNCERTAIN" {
		t.Fatal("interruption", err)
	}
	options.Hook = nil
	request.Resume = true
	request.Resolved = nil
	data, err := engine.Run(ctx, request, options)
	if err != nil || data["status"] != "approved" {
		t.Fatal("resume", data, err)
	}
	calls := string(read(t, filepath.Join(f.root, "stub/engine.calls")))
	if calls != "claude planner draft\ncodex critic critique\n" {
		t.Fatal("duplicate logical delivery", calls)
	}
	t.Log("A-ALL-03/A-ALL-04: same saved cursor; no duplicate provider turn")
}

func TestEngineGashkiInterruptedWait(t *testing.T) {
	f, request, options := engineFixture(t, "claude", "normal", map[string]any{})
	put(t, filepath.Join(f.root, "stub/engine.hold-draft"), []byte("hold\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	options.Hook = func(name string, _ *store.Store) error {
		if name != "engine.gashki.delivered" {
			return nil
		}
		go func() {
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				paths, _ := filepath.Glob(filepath.Join(request.Workspace, "state/control/gk-call-*-intent.json"))
				for _, path := range paths {
					b, err := os.ReadFile(path)
					var call gashki.CallIntent
					if err == nil && json.Unmarshal(b, &call) == nil && call.Verb == "wait" && strings.Contains(strings.Join(call.Args, " "), "--since=") {
						cancel()
						return
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
		}()
		return nil
	}
	_, err := engine.Run(ctx, request, options)
	if adapterCode(err) != "CONTROLLER_INTERRUPTED" {
		t.Fatal("interrupted wait", err)
	}
	put(t, filepath.Join(f.root, "stub/engine.continue"), []byte("continue\n"))
	options.Hook = nil
	request.Resume, request.Resolved = true, nil
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()
	data, err := engine.Run(ctx2, request, options)
	if err != nil || data["status"] != "approved" {
		t.Fatal("saved wait recovery", data, err)
	}
	if calls := string(read(t, filepath.Join(f.root, "stub/engine.calls"))); calls != "claude planner draft\ncodex critic critique\n" {
		t.Fatal("duplicate delivery", calls)
	}
	t.Log("A-ALL-03/A-ALL-04: settled interrupted native wait resumes the same cursor without a new provider turn")
}

type lostSendRunner struct{ sends int }

func (r *lostSendRunner) Run(ctx context.Context, q process.Request) (process.Result, error) {
	for _, arg := range q.Args {
		if arg == "send" && strings.Contains(filepath.Base(q.Path), "gashki") && r.sends < 2 {
			r.sends++
			q.Stdout = io.Discard
			break
		}
	}
	return (process.UnixRunner{}).Run(ctx, q)
}

func TestEngineGashkiMissingReceipt(t *testing.T) {
	f, request, options := engineFixture(t, "claude", "normal", map[string]any{})
	options.Runner = &lostSendRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	data, err := engine.Run(ctx, request, options)
	if adapterCode(err) != "SEND_UNCERTAIN" || data["status"] != "handover" {
		t.Fatal("missing receipt", data, err)
	}
	s, err := store.Open(request.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := s.LoadSnapshot()
	_ = s.Close()
	if err != nil || m.Object("recovery")["missing_receipt"] != true || m.Object("recovery")["bindings_verified"] != true {
		t.Fatal("checked facts", m, err)
	}
	o := ops.Options{Env: f.env}
	status, err := o.Handle(ctx, ops.Request{Command: "status", Selector: request.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(status["recommended_action"]), "resume") {
		t.Fatal("checked recovery advice", status)
	}
	options.Runner = process.UnixRunner{}
	request.Resume, request.Resolved = true, nil
	data, err = engine.Run(ctx, request, options)
	if err != nil || data["status"] != "approved" {
		t.Fatal("same-key recovery", data, err)
	}
	if calls := string(read(t, filepath.Join(f.root, "stub/engine.calls"))); calls != "claude planner draft\ncodex critic critique\n" {
		t.Fatal("duplicate logical delivery", calls)
	}
	if pastes := string(read(t, filepath.Join(f.root, "stub/claude.keys"))); strings.Count(pastes, "<Paste>\n") != 1 {
		t.Fatal("duplicate physical paste", pastes)
	}
	t.Log("A-GK-05/A-ALL-04: source-bound missing-receipt facts, checked same-key resume, one physical planner paste")
}

func TestEngineGashkiMissingReceiptRefusals(t *testing.T) {
	for _, variant := range []string{"identity", "input", "config", "unverified-source"} {
		t.Run(variant, func(t *testing.T) {
			f, request, options := engineFixture(t, "claude", "normal", map[string]any{})
			options.Runner = &lostSendRunner{}
			if variant == "unverified-source" {
				options.GashkiSourceProof = nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, err := engine.Run(ctx, request, options)
			if adapterCode(err) != "SEND_UNCERTAIN" {
				t.Fatal("lost send", err)
			}
			switch variant {
			case "identity":
				for i, entry := range options.Env {
					if strings.HasPrefix(entry, "HOME=") {
						options.Env[i] = "HOME=" + filepath.Join(f.root, "changed-home")
					}
				}
			case "config":
				put(t, filepath.Join(request.Workspace, "gashki.config.toml"), []byte("changed frozen config\n"))
			case "input":
				put(t, filepath.Join(request.Workspace, "BRIEF.md"), []byte("Changed binding input.\n"))
			}
			options.Runner = process.UnixRunner{}
			request.Resume, request.Resolved = true, nil
			_, err = engine.Run(ctx, request, options)
			if err == nil {
				t.Fatal("unverified recovery accepted")
			}
			s, err := store.Open(request.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			m, _, err := s.LoadSnapshot()
			_ = s.Close()
			if err != nil || variant != "config" && (m.Object("recovery")["bindings_verified"] == true || m.Object("recovery")["missing_receipt"] == true) {
				t.Fatal("stale recovery facts", m, err)
			}
			status, err := (ops.Options{Env: f.env}).Handle(ctx, ops.Request{Command: "status", Selector: request.Workspace})
			if variant == "config" {
				// A changed immutable config invalidates the checked store chain.
				// It cannot accept a control write or produce recovery advice.
				var invalid *store.Error
				if !errors.As(err, &invalid) || invalid.Code != "STATE_INVALID" || len(status) != 0 {
					t.Fatal("invalid frozen authority produced advice", status, err)
				}
			} else if err != nil || strings.Contains(fmt.Sprint(status["recommended_action"]), "'resume'") {
				t.Fatal("unverified resume advice", status, err)
			}
			if calls := string(read(t, filepath.Join(f.root, "stub/engine.calls"))); calls != "claude planner draft\n" {
				t.Fatal("replacement turn", calls)
			}
			if keys := string(read(t, filepath.Join(f.root, "stub/claude.keys"))); strings.Count(keys, "<Paste>\n") != 1 {
				t.Fatal("replacement paste", keys)
			}
			t.Log("A-GK-07: changed or unverified bindings remove recovery advice; invalid immutable authority refuses inspection; no recovery paste")
		})
	}
}

func TestEngineGashkiSmoke(t *testing.T) {
	f, request, options := engineFixture(t, "claude", "normal", map[string]any{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, err := engine.Run(ctx, request, options)
	if err != nil || data["status"] != "approved" {
		t.Fatalf("engine result: %v %v", data, err)
	}
	calls := string(read(t, filepath.Join(f.root, "stub/engine.calls")))
	if calls != "claude planner draft\ncodex critic critique\n" {
		t.Fatal("calls", calls)
	}
	s, err := store.Open(request.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, _, err := s.LoadSnapshot()
	if err != nil || m.Object("cleanup")["pending"] != false {
		t.Fatal("cleanup", m, err)
	}
	if len(m.Object("cleanup")["actions"].([]any)) != 2 {
		t.Fatal("owned pane cleanup actions", m.Object("cleanup"))
	}
	t.Log("A-ALL-01/A-ALL-06: real release Gashki; two owned provider turns; no live calls")
}

func TestInstalledGashki(t *testing.T) {
	binary := option("--volley")
	if binary == "" {
		t.Skip("installed proof requires an explicit built Volley binary")
	}
	f, request, _ := engineFixture(t, "claude", "normal", map[string]any{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(args ...string) map[string]any {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env, cmd.Dir = f.env, f.root
		var stderr strings.Builder
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		if err != nil {
			t.Fatal("installed command", args, err, stderr.String(), string(stdout))
		}
		result := object(t, stdout)
		if err = contract.Validate("envelope", result); err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(f.root, "installed-"+args[0]+".json"), stdout)
		return member(result, "data")
	}
	data := run("run", request.Workspace, "--config", filepath.Join(f.root, "volley.toml"), "--json")
	if data["status"] != "approved" {
		t.Fatal(data)
	}
	if err := contract.Validate("data-run", data); err != nil {
		t.Fatal(err)
	}
	calls := string(read(t, filepath.Join(f.root, "stub/engine.calls")))
	if data := run("status", request.Workspace, "--json"); data["status"] != "approved" {
		t.Fatal(data)
	}
	data = run("runs", "resume", request.Workspace, "--json")
	if data["status"] != "approved" || string(read(t, filepath.Join(f.root, "stub/engine.calls"))) != calls {
		t.Fatal("installed cached approval", data)
	}
	t.Log("A-ALL-01/A-ALL-04/A-ALL-06: installed release executable, real Gashki, checked envelopes, cached final with no new provider call")
}

func TestEngineGashkiRolePlacement(t *testing.T) {
	for _, variant := range []struct{ planner, placement string }{{"claude", "here"}, {"codex", "normal"}, {"codex", "here"}} {
		t.Run(variant.planner+"/"+variant.placement, func(t *testing.T) {
			f, request, options := engineFixture(t, variant.planner, variant.placement, map[string]any{})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			data, err := engine.Run(ctx, request, options)
			if err != nil || data["status"] != "approved" {
				t.Fatal(data, err)
			}
			critic := "claude"
			if variant.planner == critic {
				critic = "codex"
			}
			if calls := string(read(t, filepath.Join(f.root, "stub/engine.calls"))); calls != variant.planner+" planner draft\n"+critic+" critic critique\n" {
				t.Fatal("provider assignments", calls)
			}
			t.Log("A-ALL-09 reduced matrix: both role assignments and both actual pane placements")
		})
	}
}

type engineCrashInput struct {
	Request engine.Request
	Env     []string
	Tmux    string
	Proof   *gashki.SourceProof
	Seam    string
}

func TestEngineGashkiControllerChild(t *testing.T) {
	path := option("--engine-crash")
	if path == "" {
		return
	}
	var in engineCrashInput
	if err := json.Unmarshal(read(t, path), &in); err != nil {
		t.Fatal(err)
	}
	kill := func(name string) error {
		if name == in.Seam {
			if err := os.WriteFile(path+".kill", []byte(name), 0600); err != nil {
				return err
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
		return nil
	}
	options := engine.Options{Env: in.Env, TmuxPath: in.Tmux, GashkiSourceProof: in.Proof, Hook: func(name string, _ *store.Store) error { return kill(name) }, Fault: kill}
	_, err := engine.Run(context.Background(), in.Request, options)
	t.Fatal("owned controller did not reach its kill seam", err)
}

func crashEngine(t *testing.T, f *fixture, request engine.Request, options engine.Options, seam string) {
	t.Helper()
	path := filepath.Join(f.root, "engine-crash-"+seam+".json")
	put(t, path, jsonBytes(engineCrashInput{request, options.Env, options.TmuxPath, options.GashkiSourceProof, seam}))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestEngineGashkiControllerChild$", "--", "--engine-crash", path)
	cmd.Env, cmd.Dir = f.env, f.root
	output, err := cmd.CombinedOutput()
	if err == nil || cmd.ProcessState == nil {
		t.Fatal("controller death absent", string(output), err)
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL || string(read(t, path+".kill")) != seam {
		t.Fatal("controller death differs", status, string(output), err)
	}
}

func TestEngineGashkiIntegrated(t *testing.T) {
	plan := map[string]any{"critiques": []string{"Add a detail.\nVERDICT: REVISE\n"}, "question_purpose": "revision"}
	f, request, options := engineFixture(t, "codex", "normal", plan)
	request.Resolved.Settings.ClosingPass = true
	request.Resolved.Settings.SecondOpinion = true
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	crashEngine(t, f, request, options, "engine.gashki.delivered")
	s, err := store.Open(request.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := s.LoadSnapshot()
	if err != nil || m.Object("current_turn")["cursor"] == "" || m.Object("current_turn")["pane_uuid"] == "" {
		t.Fatal("checked delivery evidence", m, err)
	}
	_ = s.Close()
	request.Resume, request.Resolved = true, nil
	_, err = engine.Run(ctx, request, options)
	if adapterCode(err) != "ANSWER_REQUIRED" {
		t.Fatal("revision question", err)
	}
	answer := "  Exact A.\r\nB: preserve  two spaces.\n"
	put(t, filepath.Join(request.Workspace, "HUMAN.md"), []byte(answer))
	crashEngine(t, f, request, options, "human.application.commit.after")
	data, err := engine.Run(ctx, request, options)
	if err != nil || data["status"] != "approved" {
		t.Fatal("final", data, err)
	}
	s, err = store.Open(request.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, _, err = s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	apps, _ := m["applications"].([]any)
	if len(apps) != 1 {
		t.Fatal("one logical application", apps)
	}
	app := apps[0].(map[string]any)
	if app["planner_delivered"] != true || app["critic_delivered"] != true {
		t.Fatal("both deliveries", app)
	}
	a, err := human.LoadApplication(s, fmt.Sprint(app["receipt_hash"]))
	if err != nil || string(read(t, filepath.Join(request.Workspace, a.AnswerPath))) != answer {
		t.Fatal("exact answer", err)
	}
	if contract.HashBytes(read(t, filepath.Join(request.Workspace, "SPEC.md"))) != m.Object("approval")["spec_hash"] || m.Object("cleanup")["pending"] != false {
		t.Fatal("final approval and cleanup", m.Object("approval"), m.Object("cleanup"))
	}
	calls := string(read(t, filepath.Join(f.root, "stub/engine.calls")))
	want := "codex planner draft\nclaude critic critique\ncodex planner revision\ncodex planner directive\nclaude critic critique\ncodex critic advisory\ncodex planner closing\nclaude critic confirmation\n"
	if calls != want {
		t.Fatal("no duplicate turns and full transition sequence", calls)
	}
	if _, err = engine.Run(ctx, request, options); err != nil || string(read(t, filepath.Join(f.root, "stub/engine.calls"))) != calls {
		t.Fatal("cached final", err)
	}
	newWorkspace := filepath.Join(f.root, "seeded")
	if err = os.Mkdir(newWorkspace, 0700); err != nil {
		t.Fatal(err)
	}
	r, _, err := config.Resolve(config.ResolveOptions{Cwd: f.root, Home: filepath.Join(f.root, "home"), NamedFile: filepath.Join(request.Workspace, "volley.config.toml"), Workspace: newWorkspace, Mutating: true, Defaults: map[string]any{"planner": "claude", "closing_pass": false, "max_rounds": 9}})
	if err != nil || r.Settings.Planner != "codex" || !r.Settings.ClosingPass || r.Settings.MaxRounds != 4 || r.Sources["planner"].Source != "file" {
		t.Fatal("saved settings after changed defaults", r, err)
	}
	seed := engine.Request{Workspace: newWorkspace, Seed: filepath.Join(request.Workspace, "SPEC.md"), Resolved: &r}
	data, err = engine.Run(ctx, seed, options)
	if err != nil || data["status"] != "approved" {
		t.Fatal("seeded fresh run", data, err)
	}
	newStore, err := store.Open(newWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	defer newStore.Close()
	nm, _, err := newStore.LoadSnapshot()
	if err != nil || nm.String("run_id") == m.String("run_id") || reflect.DeepEqual(nm["panes"], m["panes"]) {
		t.Fatal("fresh run and pane identities", nm, err)
	}
	argv := func(ws string) map[string][]string {
		result := map[string][]string{}
		paths, _ := filepath.Glob(filepath.Join(ws, "state/turns/*/gk-prepared.json"))
		for _, path := range paths {
			v := object(t, read(t, path))
			q := member(v, "Request")
			role := text(q, "Role")
			if text(q, "Purpose") == "advisory" {
				continue
			}
			if _, ok := result[role]; ok {
				continue
			}
			for _, arg := range v["Argv"].([]any) {
				result[role] = append(result[role], strings.ReplaceAll(arg.(string), ws, "WORKSPACE"))
			}
		}
		return result
	}
	if !reflect.DeepEqual(argv(request.Workspace), argv(newWorkspace)) {
		t.Fatal("tool-controlled argv drift", argv(request.Workspace), argv(newWorkspace))
	}
	t.Log("A-ALL-01..08 shared workflow: actual SIGKILL, saved cursor, exact answer application recovery, closing and confirmation, fresh seed, saved settings and argv parity")
}

func TestEngineGashkiPaneAnswer(t *testing.T) {
	for _, variant := range []struct{ planner, mode string }{{"claude", "answer"}, {"codex", "answer"}, {"claude", "clarification"}, {"codex", "spec"}, {"claude", "questions"}} {
		t.Run(variant.planner+"/"+variant.mode, func(t *testing.T) {
			answer := "A: keep this exact line.\nB: two spaces  stay.\n"
			plan := map[string]any{"critiques": []string{"Add a detail.\nVERDICT: REVISE\n"}, "question_purpose": "revision", "answer_mode": variant.mode, "answer_text": answer}
			f, request, options := engineFixture(t, variant.planner, "normal", plan)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			_, err := engine.Run(ctx, request, options)
			if adapterCode(err) != "ANSWER_REQUIRED" {
				t.Fatal("question gate", err)
			}
			baseline := string(read(t, filepath.Join(f.root, "stub/engine.calls")))
			if strings.Count(baseline, "\n") != 3 {
				t.Fatal("question turn count", baseline)
			}
			var pane gashki.PaneBinding
			entries, err := filepath.Glob(filepath.Join(request.Workspace, "state/control/gk-spawn-*-planner-pane.json"))
			if err != nil || len(entries) != 1 {
				t.Fatal(entries, err)
			}
			if err := json.Unmarshal(read(t, entries[0]), &pane); err != nil {
				t.Fatal(err)
			}
			known := map[string]bool{}
			entries, err = filepath.Glob(filepath.Join(request.Workspace, "state/control/gk-call-*-intent.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range entries {
				known[path] = true
			}
			waitObserved := make(chan bool, 1)
			options.Hook = func(name string, s *store.Store) error {
				if name != "engine.answer.wait" {
					return nil
				}
				f.tm("send-keys", "-t", pane.Target, "-l", "__owned_answer__")
				f.tm("send-keys", "-t", pane.Target, "Enter")
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(filepath.Join(f.root, "stub/engine.answer-working")); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if _, err := os.Stat(filepath.Join(f.root, "stub/engine.answer-working")); err != nil {
					return err
				}
				go func() {
					observed := false
					defer func() {
						_ = os.WriteFile(filepath.Join(f.root, "stub/engine.answer-continue"), []byte("settle\n"), 0600)
						waitObserved <- observed
					}()
					deadline := time.Now().Add(10 * time.Second)
					for time.Now().Before(deadline) {
						entries, _ := filepath.Glob(filepath.Join(request.Workspace, "state/control/gk-call-*-intent.json"))
						for _, path := range entries {
							if known[path] {
								continue
							}
							b, err := os.ReadFile(path)
							if err != nil {
								continue
							}
							var call gashki.CallIntent
							if json.Unmarshal(b, &call) == nil && call.Verb == "wait" && strings.Contains(strings.Join(call.Args, " "), "--since=") {
								observed = true
								return
							}
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(10 * time.Millisecond):
						}
					}
				}()
				return nil
			}
			request.Resume = true
			request.Resolved = nil
			data, err := engine.Run(ctx, request, options)
			if variant.mode == "answer" {
				if err != nil || data["status"] != "approved" {
					t.Fatal("answer", data, err)
				}
				if !<-waitObserved {
					t.Fatal("controller did not wait for qualified answer completion")
				}
				paths, _ := filepath.Glob(filepath.Join(request.Workspace, "state/human/archive/*.answer.md"))
				matched := false
				for _, path := range paths {
					if string(read(t, path)) == answer {
						matched = true
					}
				}
				if !matched {
					t.Fatal("exact answer archive absent", paths)
				}
			} else if variant.mode == "clarification" {
				if adapterCode(err) != "ANSWER_REQUIRED" {
					t.Fatal("clarification released gate", err)
				}
				if !<-waitObserved {
					t.Fatal("clarification turn did not settle")
				}
				if string(read(t, filepath.Join(f.root, "stub/engine.calls"))) != baseline {
					t.Fatal("loop turn overlapped clarification")
				}
			} else {
				if adapterCode(err) != "ANSWER_CONFLICT" && adapterCode(err) != "PLANNER_MUTATION" {
					t.Fatal("answer edit conflict", err)
				}
				cancel()
				<-waitObserved
				if string(read(t, filepath.Join(f.root, "stub/engine.calls"))) != baseline {
					t.Fatal("loop turn after answer edit")
				}
			}
			t.Log("A-HUMAN-04: real owned pane answer turn; qualified completion before consumption; no live vendor calls")
		})
	}
}

func TestEngineGashkiProtectedFiles(t *testing.T) {
	for _, planner := range []string{"claude", "codex"} {
		for _, variant := range []struct {
			name, path, purpose string
			calls               int
		}{
			{"state", "state/manifest.json", "draft", 1},
			{"config", "gashki.config.toml", "draft", 1},
			{"prior-critique", "rounds/r01.critique.md", "revision", 3},
			{"future-output", "rounds/r04.critique.md", "draft", 1},
		} {
			t.Run(planner+"/"+variant.name, func(t *testing.T) {
				plan := map[string]any{"mutation_role": "planner", "mutation_purpose": variant.purpose, "mutation_path": variant.path}
				if variant.purpose == "revision" {
					plan["critiques"] = []string{"Add a detail.\nVERDICT: REVISE\n"}
				}
				f, request, options := engineFixture(t, planner, "normal", plan)
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				_, err := engine.Run(ctx, request, options)
				var typed *contract.Error
				if !errors.As(err, &typed) || typed.Code != "PLANNER_MUTATION" || typed.Exit != 8 {
					t.Fatal("mutation result", err)
				}
				calls := strings.Split(strings.TrimSpace(string(read(t, filepath.Join(f.root, "stub/engine.calls")))), "\n")
				if len(calls) != variant.calls {
					t.Fatal("later model call after mutation", calls)
				}
				t.Log("A-GK-13: protected mutation detected; no later provider turn; permission enforcement unverified")
			})
		}
	}
}

func TestEngineGashkiAllowedHistory(t *testing.T) {
	f, request, options := engineFixture(t, "codex", "normal", map[string]any{"mutation_role": "planner", "mutation_purpose": "draft", "mutation_path": "rounds/agent-notes.md"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, err := engine.Run(ctx, request, options)
	if err != nil || data["status"] != "approved" {
		t.Fatal(data, err)
	}
	if len(read(t, filepath.Join(f.root, "ws/rounds/agent-notes.md"))) == 0 {
		t.Fatal("history absent")
	}
	t.Log("A-GK-13: allowed new history and exact controller sinks coexist")
}

func TestEngineGashkiImpasseRetention(t *testing.T) {
	f, request, options := engineFixture(t, "claude", "normal", map[string]any{"critiques": []string{"Add a detail.\nVERDICT: REVISE\n"}})
	request.Resolved.Settings.MaxRounds = 1
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	data, err := engine.Run(ctx, request, options)
	if adapterCode(err) != "REVIEW_IMPASSE" || data["status"] != "impasse" {
		t.Fatal("impasse", data, err)
	}
	s, err := store.Open(request.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := s.LoadSnapshot()
	_ = s.Close()
	if err != nil || len(m.Object("retention")["panes"].([]any)) != 2 || len(m.Object("cleanup")["actions"].([]any)) != 0 || m.Object("cleanup")["pending"] != false {
		t.Fatal("retain resumable panes", m, err)
	}
	request.Resume, request.Resolved, request.Explicit = true, nil, map[string]any{"max_rounds": 2}
	data, err = engine.Run(ctx, request, options)
	if err != nil || data["status"] != "approved" {
		t.Fatal("resume retained panes", data, err)
	}
	if calls := string(read(t, filepath.Join(f.root, "stub/engine.calls"))); calls != "claude planner draft\ncodex critic critique\nclaude planner revision\ncodex critic critique\n" {
		t.Fatal("retained role sessions", calls)
	}
	t.Log("A-GK-12/A-ALL-06: impasse retains resumable owned panes; approval settles their cleanup")
}
