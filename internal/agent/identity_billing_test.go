//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/process"
)

func TestAREC02Identity(t *testing.T) {
	t.Run("absent-empty-present", func(t *testing.T) {
		home := t.TempDir()
		root := filepath.Join(home, "custom")
		for _, env := range [][]string{{"HOME=" + home}, {"HOME=" + home, "CLAUDE_CONFIG_DIR="}, {"HOME=" + home, "CLAUDE_CONFIG_DIR=" + root, "CODEX_HOME=" + root}} {
			caller := CaptureIdentity(env)
			roots := ResolveRoots(caller, nil)
			b, _ := json.Marshal(caller)
			if strings.Contains(string(b), "OPENAI_API_KEY") {
				t.Fatal("identity copied credential context")
			}
			if len(env) == 1 {
				expected, _ := config.Physical(filepath.Join(home, ".claude"))
				if caller.ClaudeRoot.Present || caller.ClaudeRoot.Value != nil || roots.Claude == nil || *roots.Claude != expected {
					t.Fatalf("%+v %+v", caller, roots)
				}
			} else if len(env) == 2 {
				if !caller.ClaudeRoot.Present || caller.ClaudeRoot.Value == nil || *caller.ClaudeRoot.Value != "" || roots.Claude != nil {
					t.Fatalf("%+v %+v", caller, roots)
				}
				requireErrorCode(t, roots.RequireKnown(), "BILLING_REFUSED")
			} else if expected, _ := config.Physical(root); roots.Claude == nil || *roots.Claude != expected {
				t.Fatalf("%+v", roots)
			}
		}
	})
	t.Run("server-roots-and-presence", func(t *testing.T) {
		o, r := prepareFixture(t, "gashki")
		serverHome := filepath.Join(t.TempDir(), "server-home")
		serverClaude := filepath.Join(serverHome, "claude")
		serverCodex := filepath.Join(serverHome, "codex")
		r.ServerVars = map[string]*string{"HOME": &serverHome, "CLAUDE_CONFIG_DIR": &serverClaude, "CODEX_HOME": &serverCodex}
		p, err := Prepare(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		expectedClaude, _ := config.Physical(serverClaude)
		expectedCodex, _ := config.Physical(serverCodex)
		if p.Record.Caller.ClaudeRoot.Present || p.Record.Roots.Claude == nil || *p.Record.Roots.Claude != expectedClaude || p.Record.Roots.Codex == nil || *p.Record.Roots.Codex != expectedCodex {
			t.Fatalf("%+v", p.Record)
		}
		if p.Record.Server.Socket == nil {
			t.Fatal("server binding missing")
		}
	})
	t.Run("caller-root-wins-without-exporting-default", func(t *testing.T) {
		o, r := prepareFixture(t, "gashki")
		custom := filepath.Join(t.TempDir(), "caller-claude")
		other := filepath.Join(t.TempDir(), "server-claude")
		o.Env = append(o.Env, "CLAUDE_CONFIG_DIR="+custom)
		r.ServerVars["CLAUDE_CONFIG_DIR"] = &other
		p, err := Prepare(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := config.Physical(custom)
		if *p.Record.Roots.Claude != expected || p.Record.Caller.CodexRoot.Present {
			t.Fatalf("%+v", p.Record)
		}
		child, _ := process.ChildEnvironment(o.Env, "gashki")
		if !reflect.DeepEqual(child, o.Env) {
			t.Fatal("Gashki environment changed")
		}
	})
	t.Run("unknown-refusal-and-explicit-override", func(t *testing.T) {
		for _, override := range []bool{false, true} {
			o, r := prepareFixture(t, "gashki")
			r.Fail = "display-message"
			o.Billing = BillingSelection{override, override}
			o.Resolved.Settings.AllowAPIKey = override
			p, err := PrepareAndCall(context.Background(), o, "gashki spawn", func(p Prepared) error {
				if !override {
					t.Fatal("callback with unknown roots")
				}
				return nil
			})
			if !override {
				requireErrorCode(t, err, "BILLING_REFUSED")
			} else {
				if err != nil || p.Record.Roots.Claude != nil || p.Record.Server.Context.Home.Value != nil || p.Record.Server.Socket != nil {
					t.Fatalf("unknown context was guessed: %+v %v", p.Record, err)
				}
			}
		}
	})
	t.Run("changed-root-server-and-binary", func(t *testing.T) {
		for _, what := range []string{"root", "home", "presence", "server", "binary-path", "binary-version", "binary-hash"} {
			t.Run(what, func(t *testing.T) {
				o, _ := prepareFixture(t, "gashki")
				p, err := Prepare(context.Background(), o)
				if err != nil {
					t.Fatal(err)
				}
				caller := CaptureIdentity(o.Env)
				serverCopy := *p.Record.Server
				serverCopy.Context = p.Record.Server.Context
				bindings := make(map[string]ExecutableBinding)
				for key, b := range p.Record.Executables {
					bindings[key] = b
				}
				changed := "changed"
				switch what {
				case "root":
					caller.ClaudeRoot = Variable{true, true, &changed, "caller: present"}
				case "home":
					caller.Home.Value = &changed
				case "presence":
					empty := ""
					caller.CodexRoot = Variable{true, true, &empty, "caller: present"}
				case "server":
					serverCopy.Socket = &changed
				default:
					b := bindings["gashki"]
					if what == "binary-path" {
						b.Path = "changed"
					}
					if what == "binary-version" {
						b.Version = "changed"
					}
					if what == "binary-hash" {
						b.Hash = "changed"
					}
					bindings["gashki"] = b
				}
				count := 0
				err = p.ResumeCall("gashki spawn", caller, &serverCopy, bindings, func() error { count++; return nil })
				requireErrorCode(t, err, "IDENTITY_CONFLICT")
				if count != 0 {
					t.Fatal("callback before resume guard")
				}
			})
		}
	})
}
func writeGuardFile(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}
func TestAREC03Billing(t *testing.T) {
	for _, variant := range []string{"anthropic-env", "openai-env", "claude-helper", "codex-key", "codex-null", "claude-unreadable", "codex-unreadable", "malformed", "project-helper", "project-env", "override", "implicit-override", "unknown-root"} {
		t.Run(variant, func(t *testing.T) {
			home := t.TempDir()
			claude := filepath.Join(home, "custom-claude")
			codex := filepath.Join(home, "custom-codex")
			workspace := filepath.Join(home, "workspace")
			roots := EffectiveRoots{&claude, &codex, "checked", "checked"}
			env := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + claude, "CODEX_HOME=" + codex}
			selection := BillingSelection{}
			secret := "NEVER_COPY_THIS_SECRET"
			switch variant {
			case "anthropic-env":
				env = append(env, "ANTHROPIC_API_KEY="+secret)
			case "openai-env":
				env = append(env, "OPENAI_API_KEY="+secret)
			case "claude-helper":
				writeGuardFile(t, filepath.Join(claude, "settings.json"), `{"apiKeyHelper":"`+secret+`"}`, 0600)
			case "codex-key":
				writeGuardFile(t, filepath.Join(codex, "auth.json"), `{"OPENAI_API_KEY":"`+secret+`"}`, 0600)
			case "codex-null":
				writeGuardFile(t, filepath.Join(codex, "auth.json"), `{"OPENAI_API_KEY":null,"tokens":{"access_token":"`+secret+`"}}`, 0600)
			case "claude-unreadable":
				writeGuardFile(t, filepath.Join(claude, "settings.json"), `{}`, 0000)
			case "codex-unreadable":
				writeGuardFile(t, filepath.Join(codex, "auth.json"), `{}`, 0000)
			case "malformed":
				writeGuardFile(t, filepath.Join(claude, "settings.json"), `{"secret":"`+secret, 0600)
			case "project-helper":
				writeGuardFile(t, filepath.Join(workspace, ".claude/settings.local.json"), `{"apiKeyHelper":"`+secret+`"}`, 0600)
			case "project-env":
				writeGuardFile(t, filepath.Join(workspace, ".claude/settings.json"), `{"env":{"ANTHROPIC_API_KEY":"`+secret+`"}}`, 0600)
			case "override":
				env = append(env, "OPENAI_API_KEY="+secret)
				selection = BillingSelection{true, true}
			case "implicit-override":
				selection = BillingSelection{true, false}
			case "unknown-root":
				roots.Claude = nil
			}
			err := GuardBilling(context.Background(), roots, env, workspace, selection, nil)
			if variant == "codex-null" || variant == "override" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireErrorCode(t, err, "BILLING_REFUSED")
				encoded, _ := json.Marshal(err)
				if strings.Contains(string(encoded), secret) || strings.Contains(err.Error(), secret) {
					t.Fatal("secret appeared in diagnostic")
				}
			}
			if roots.Codex != nil && *roots.Codex != codex {
				t.Fatal("guard changed identity roots")
			}
		})
	}
	t.Run("nested-marker-removal-is-narrow", func(t *testing.T) {
		home := t.TempDir()
		base := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=", "CODEX_HOME=" + home, "CLAUDECODE=1", "ANTHROPIC_BASE_URL=do-not-record", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_AGENT_SDK_VERSION=keep", "OTHER=keep"}
		child, removed := process.ChildEnvironment(base, "cli")
		if !reflect.DeepEqual(removed, []string{"CLAUDECODE", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_ENTRYPOINT"}) {
			t.Fatal(removed)
		}
		for _, required := range []string{"CLAUDE_CONFIG_DIR=", "CODEX_HOME=" + home, "CLAUDE_AGENT_SDK_VERSION=keep", "OTHER=keep"} {
			found := false
			for _, entry := range child {
				if entry == required {
					found = true
				}
			}
			if !found {
				t.Fatal("removed", required)
			}
		}
		gashki, removed := process.ChildEnvironment(base, "gashki")
		if len(removed) != 0 || !reflect.DeepEqual(gashki, base) {
			t.Fatal("duplicated Gashki stripping")
		}
		ordinary, removed := process.ChildEnvironment([]string{"ANTHROPIC_BASE_URL=keep"}, "cli")
		if len(removed) != 0 || len(ordinary) != 1 {
			t.Fatal("stripped without nested marker")
		}
	})
}

func TestChangedExecutableBytesCannotUseStaleBinding(t *testing.T) {
	o, _ := prepareFixture(t, "cli")
	p, err := Prepare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	binding := p.Record.Executables["claude"]
	if err := os.WriteFile(binding.Path, []byte("different executable"), 0700); err != nil {
		t.Fatal(err)
	}
	count := 0
	err = p.ResumeCall("direct launch", p.Record.Caller, nil, p.Record.Executables, func() error { count++; return nil })
	requireErrorCode(t, err, "IDENTITY_CONFLICT")
	if count != 0 {
		t.Fatal("stale binding released a callback")
	}
}

func TestResumeMetadataVersionAndBillingChecks(t *testing.T) {
	for _, variant := range []string{"same", "version", "api-key", "root"} {
		t.Run(variant, func(t *testing.T) {
			o, r := prepareFixture(t, "cli")
			p, err := Prepare(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			r.Calls = nil
			env := append([]string(nil), o.Env...)
			if variant == "version" {
				r.Version = "owned metadata stub 2.0\n"
			}
			if variant == "api-key" {
				env = append(env, "OPENAI_API_KEY=fixture-secret")
			}
			if variant == "root" {
				env = append(env, "CODEX_HOME="+t.TempDir())
			}
			calls := 0
			err = p.ResumeWithRunner(context.Background(), env, nil, r, "direct launch", func() error { calls++; return nil })
			switch variant {
			case "same":
				if err != nil || calls != 1 {
					t.Fatalf("%d %v", calls, err)
				}
			case "api-key":
				requireErrorCode(t, err, "BILLING_REFUSED")
				if calls != 0 || len(r.Calls) != 0 {
					t.Fatal("callback before billing guard")
				}
			default:
				requireErrorCode(t, err, "IDENTITY_CONFLICT")
				if calls != 0 {
					t.Fatal("callback before binding guard")
				}
				if variant == "root" && len(r.Calls) != 0 {
					t.Fatal("metadata call before root guard")
				}
			}
		})
	}
}

func TestReplacedServerBindingCannotReuseSocketPath(t *testing.T) {
	o, r := prepareFixture(t, "gashki")
	p, err := Prepare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(r.SocketPath, r.SocketPath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.SocketPath, []byte("replacement binding"), 0600); err != nil {
		t.Fatal(err)
	}
	observed := ProbeServer(context.Background(), r, o.TmuxPath, o.Env, o.Store.Path, "", "", p.Gate.Check)
	count := 0
	err = p.ResumeCall("gashki spawn", p.Record.Caller, &observed, p.Record.Executables, func() error { count++; return nil })
	requireErrorCode(t, err, "IDENTITY_CONFLICT")
	if count != 0 {
		t.Fatal("server path reuse bypassed identity check")
	}
}

func TestPreparationDoesNotPersistCredentialValues(t *testing.T) {
	o, _ := prepareFixture(t, "cli")
	secret := "OWNED_TEST_SECRET_NEVER_RECORD"
	o.Env = append(o.Env, "OPENAI_API_KEY="+secret, "CLAUDECODE=1", "ANTHROPIC_BASE_URL="+secret, "CLAUDE_CODE_ENTRYPOINT=cli")
	o.Billing = BillingSelection{true, true}
	o.Resolved.Settings.AllowAPIKey = true
	o.Resolved.Sources["allow_api_key"] = config.Source{Source: "flag", Flag: "--allow-api-key", ArgumentPosition: 1, Rule: "explicit flag"}
	p, err := Prepare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Record.RemovedEnvironmentNames) != 3 {
		t.Fatal("removed names not recorded")
	}
	b, err := json.Marshal(p.Record)
	if err != nil || strings.Contains(string(b), secret) {
		t.Fatal("credential entered preparation record")
	}
	err = filepath.WalkDir(o.Store.Path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), secret) {
			t.Fatalf("credential entered owned record %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
