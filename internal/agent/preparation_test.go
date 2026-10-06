//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/store"
)

type metadataRunner struct {
	Version                    string
	Calls                      []process.Request
	StateDir, SocketPath, Home string
	ServerVars                 map[string]*string
	Hook                       func(process.Request)
	Fail                       string
	Malformed                  bool
}

func TestNativeExecutableAbove128MiB(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "native-vendor")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0700)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file reproduces the installed native executable's size without
	// allocating a large byte slice or filling temporary storage.
	if err = f.Truncate(129 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runner := &metadataRunner{}
	binding, err := bindExecutable(context.Background(), PrepareOptions{Store: s, Runner: runner}, path, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.Calls) != 1 || binding.Version != "owned metadata stub 1.0" {
		t.Fatal("Large executable did not complete the version binding")
	}
	gate := &FrozenGate{store: s, executables: map[string]ExecutableBinding{"claude": binding}}
	if err = gate.Check(); err != nil {
		t.Fatal(err)
	}
	f, err = os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte{1}, (129<<20)-1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = gate.Check(); err == nil {
		t.Fatal("Changed large executable was accepted")
	}
}

func (r *metadataRunner) Run(_ context.Context, q process.Request) (process.Result, error) {
	r.Calls = append(r.Calls, q)
	if r.Hook != nil {
		r.Hook(q)
	}
	outcome := process.Result{Outcome: process.Exited, Exit: 0, Settled: true, Started: true}
	args := strings.Join(q.Args, " ")
	if r.Fail != "" && strings.Contains(args, r.Fail) {
		outcome.Exit = 1
		return outcome, nil
	}
	output := ""
	switch {
	case reflect.DeepEqual(q.Args, []string{"--version"}):
		output = "owned metadata stub 1.0\n"
		if r.Version != "" {
			output = r.Version
		}
	case strings.Contains(args, "config show --toml"):
		output = fmt.Sprintf("state_dir = %q\nready_timeout = '30s'\nwait_timeout = '10m'\n", r.StateDir)
		if r.Malformed {
			output = "not TOML"
		}
	case strings.Contains(args, "config get state_dir --json"):
		data := map[string]any{"key": "state_dir", "value": r.StateDir}
		canonical, _ := contract.Canonical(data)
		b, _ := json.Marshal(map[string]any{"ok": true, "tool_version": "owned metadata stub 1.0", "data": data, "meta": map[string]any{"contract_version": "2", "request_id": "aaaaaaaaaaaaaaaaaaaaaaaaaa", "ts_iso": "2026-10-04T00:00:00Z", "elapsed_ms": 0, "data_hash": contract.HashBytes(canonical)}, "warnings": []any{}, "commands": []any{}, "errors": []any{}})
		output = string(b)
	case strings.Contains(args, "display-message"):
		output = r.SocketPath + "\n"
	case strings.Contains(args, "show-environment"):
		key := q.Args[len(q.Args)-1]
		value, known := r.ServerVars[key]
		if !known {
			outcome.Exit = 1
		} else if value == nil {
			output = "-" + key + "\n"
		} else {
			output = key + "=" + *value + "\n"
		}
	default:
		return outcome, fmt.Errorf("Unexpected owned stub operation %s", args)
	}
	if q.Stdout != nil {
		_, err := q.Stdout.Write([]byte(output))
		return outcome, err
	}
	return outcome, nil
}
func syntheticSnapshot(t *testing.T, s *store.Store) store.Snapshot {
	t.Helper()
	b, err := contract.Schema("manifest")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	_ = json.Unmarshal(b, &schema)
	var fixture func(map[string]any) any
	fixture = func(d map[string]any) any {
		if v, ok := d["const"]; ok {
			return v
		}
		if v, ok := d["enum"].([]any); ok {
			return v[0]
		}
		if a, ok := d["anyOf"].([]any); ok {
			for _, v := range a {
				m := v.(map[string]any)
				if m["type"] == "null" {
					return nil
				}
			}
			return fixture(a[0].(map[string]any))
		}
		switch d["type"] {
		case "object":
			m := map[string]any{}
			p := d["properties"].(map[string]any)
			for _, key := range d["required"].([]any) {
				m[key.(string)] = fixture(p[key.(string)].(map[string]any))
			}
			return m
		case "array":
			return []any{}
		case "integer":
			return 0
		case "boolean":
			return false
		case "string":
			return ""
		}
		return nil
	}
	m := store.Snapshot(fixture(schema).(map[string]any))
	run, _ := store.NewID()
	root, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	rootObs := statIdentity(root)
	m["ownership"] = map[string]any{"device": rootObs[0], "inode": rootObs[1]}
	m["run_id"] = run
	m["canonical_workspace"] = s.Path
	m["workspace"] = s.Path
	m["max_rounds"] = 3
	return m
}
func prepareFixture(t *testing.T, backend string) (PrepareOptions, *metadataRunner) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireOwner(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	m := syntheticSnapshot(t, s)
	tx, err := s.NewTransaction("initialize", m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitTransaction(tx); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex", "gashki", "tmux"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("owned stub "+name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "volley-source.toml")
	gsource := filepath.Join(root, "gashki-source.toml")
	if err := os.WriteFile(gsource, []byte("# original caller file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configText := fmt.Sprintf("backend = %q\nclaude_bin = %q\ncodex_bin = %q\ngashki_bin = %q\ngashki_config = %q\nmax_rounds = 3\n", backend, filepath.Join(bin, "claude"), filepath.Join(bin, "codex"), filepath.Join(bin, "gashki"), gsource)
	if err := os.WriteFile(source, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := config.Resolve(config.ResolveOptions{Cwd: root, Home: home, NamedFile: source, Workspace: workspace, Mutating: true})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "owned-socket-identity")
	if err := os.WriteFile(socket, []byte("socket fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &metadataRunner{StateDir: filepath.Join(root, "queried-state"), SocketPath: socket, Home: home, ServerVars: map[string]*string{"HOME": &home}}
	return PrepareOptions{Resolved: resolved, Store: s, Runner: runner, Env: []string{"HOME=" + home, "PATH=" + bin, "GASHKI_STATE_DIR=/must-not-be-used"}, TmuxPath: filepath.Join(bin, "tmux"), Lookup: func(name string) (string, error) {
		if !filepath.IsAbs(name) {
			return "", errors.New("test refuses PATH fallback")
		}
		return name, nil
	}}, runner
}
func requireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *contract.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func TestAREC01Preparation(t *testing.T) {
	for _, backend := range []string{"cli", "gashki"} {
		t.Run(backend, func(t *testing.T) {
			o, runner := prepareFixture(t, backend)
			sourceBefore, _ := os.ReadFile(o.Resolved.SelectedFile)
			gsourceBefore, _ := os.ReadFile(o.Resolved.Settings.GashkiConfig)
			p, err := Prepare(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"volley.config.toml", "state/control"} {
				st, err := os.Stat(filepath.Join(o.Store.Path, name))
				if err != nil {
					t.Fatal(err)
				}
				if !st.IsDir() && st.Mode().Perm() != 0600 {
					t.Fatal("record is not private")
				}
			}
			sourceAfter, _ := os.ReadFile(o.Resolved.SelectedFile)
			gsourceAfter, _ := os.ReadFile(o.Resolved.Settings.GashkiConfig)
			if string(sourceBefore) != string(sourceAfter) || string(gsourceBefore) != string(gsourceAfter) {
				t.Fatal("caller file changed")
			}
			if err := p.Gate.Check(); err != nil {
				t.Fatal(err)
			}
			m, _, err := o.Store.LoadSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			saved, err := DecodePreparation(o.Store, m)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved.Record, p.Record) {
				t.Fatal("saved preparation differs")
			}
			if backend == "gashki" {
				if p.StateDir != runner.StateDir || p.Record.Mechanism.Socket != nil {
					t.Fatal("wrong mechanism binding")
				}
				frozenCalls := 0
				for _, call := range runner.Calls {
					if strings.Contains(strings.Join(call.Args, " "), filepath.Join(o.Store.Path, "gashki.config.toml")) {
						frozenCalls++
					}
				}
				if frozenCalls != 2 {
					t.Fatal("later config calls did not use frozen file")
				}
			}
			if p.Record.ObservedModel["claude"] != nil || p.Record.ObservedEffort["codex"] != nil {
				t.Fatal("vendor default claimed observed")
			}
			if err := p.IncreaseCap(5); err != nil {
				t.Fatal(err)
			}
			r, _, err := config.Resolve(config.ResolveOptions{Cwd: o.Store.Path, Home: runner.Home, NamedFile: filepath.Join(o.Store.Path, "volley.config.toml"), Workspace: filepath.Join(t.TempDir(), "fresh")})
			if err != nil || r.Settings.MaxRounds != 3 {
				t.Fatalf("saved cap changed: %d %v", r.Settings.MaxRounds, err)
			}
		})
	}
	t.Run("changed-before-preparation", func(t *testing.T) {
		o, runner := prepareFixture(t, "cli")
		if err := os.WriteFile(o.Resolved.SelectedFile, []byte("max_rounds = 4\n"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Prepare(context.Background(), o)
		requireErrorCode(t, err, "ARTIFACT_CHANGED")
		if len(runner.Calls) != 0 {
			t.Fatal("callback before stable input check")
		}
	})
	t.Run("conflicting-record", func(t *testing.T) {
		o, _ := prepareFixture(t, "cli")
		path := filepath.Join(o.Store.Path, "volley.config.toml")
		if err := os.WriteFile(path, []byte("different"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Prepare(context.Background(), o)
		requireErrorCode(t, err, "OUTPUT_CONFLICT")
		b, _ := os.ReadFile(path)
		if string(b) != "different" {
			t.Fatal("existing record overwritten")
		}
	})
	t.Run("gashki-status-and-toml", func(t *testing.T) {
		for _, bad := range []bool{false, true} {
			o, runner := prepareFixture(t, "gashki")
			if bad {
				runner.Malformed = true
			} else {
				runner.Fail = "show --toml"
			}
			_, err := Prepare(context.Background(), o)
			requireErrorCode(t, err, "INVALID_CONFIG")
			if _, err := os.Stat(filepath.Join(o.Store.Path, "volley.config.toml")); !os.IsNotExist(err) {
				t.Fatal("invalid source was frozen")
			}
		}
	})
}
func TestAREC01RecordFailures(t *testing.T) {
	o, _ := prepareFixture(t, "gashki")
	var sites []string
	o.Store.Fault = func(site string) error { sites = append(sites, site); return nil }
	if _, err := Prepare(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.Store.Fault = nil
	// Record all distinct before/after write, hash-bound promotion and commit sites.
	unique := map[string]bool{}
	for _, site := range sites {
		unique[site] = true
	}
	for site := range unique {
		t.Run(site, func(t *testing.T) {
			o, runner := prepareFixture(t, "gashki")
			source, _ := os.ReadFile(o.Resolved.SelectedFile)
			fired := false
			o.Store.Fault = func(name string) error {
				if name == site && !fired {
					fired = true
					return errors.New("owned record fault")
				}
				return nil
			}
			dependent := 0
			p, err := PrepareAndCall(context.Background(), o, "gashki spawn", func(Prepared) error { dependent++; return nil })
			_ = p
			if err == nil || !fired {
				t.Fatal("record fault did not stop preparation")
			}
			if dependent != 0 {
				t.Fatal("dependent callback after failure")
			}
			after, _ := os.ReadFile(o.Resolved.SelectedFile)
			if string(source) != string(after) {
				t.Fatal("caller source mutated")
			}
			for _, call := range runner.Calls {
				if strings.Contains(strings.Join(call.Args, " "), "spawn") {
					t.Fatal("spawn in preparation")
				}
			}
		})
	}
}
func TestAREC04FrozenGate(t *testing.T) {
	kinds := []string{"direct launch", "gashki config", "gashki spawn", "gashki send", "gashki wait", "gashki observe", "gashki status", "gashki kill"}
	for _, name := range []string{"volley.config.toml", "gashki.config.toml"} {
		for _, kind := range kinds {
			t.Run(name+"/"+kind, func(t *testing.T) {
				o, _ := prepareFixture(t, "gashki")
				p, err := Prepare(context.Background(), o)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(o.Store.Path, name), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				calls := 0
				err = p.Gate.Call(kind, func() error { calls++; return nil })
				requireErrorCode(t, err, "ARTIFACT_CHANGED")
				if calls != 0 {
					t.Fatal("callback after changed record")
				}
			})
		}
	}
	t.Run("manifest-cannot-replace-expectation", func(t *testing.T) {
		o, _ := prepareFixture(t, "cli")
		p, err := Prepare(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(o.Store.Path, "volley.config.toml"), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(o.Store.Path, "state/manifest.json"), []byte("forged"), 0600); err != nil {
			t.Fatal(err)
		}
		requireErrorCode(t, p.Gate.Check(), "ARTIFACT_CHANGED")
	})
}

func TestUntrustedCheckpointIsNotAdoptedForControlWrite(t *testing.T) {
	o, _ := prepareFixture(t, "cli")
	p, err := Prepare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := o.Store.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	m["status"] = "approved"
	tampered, err := contract.Canonical(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.Store.Path, "state/manifest.json")
	if err := os.WriteFile(path, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.IncreaseCap(5); err == nil {
		t.Fatal("changed checkpoint adopted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(tampered) {
		t.Fatal("changed checkpoint was overwritten")
	}
}
func TestActiveExecutablesResolveOnce(t *testing.T) {
	for _, backend := range []string{"cli", "gashki"} {
		t.Run(backend, func(t *testing.T) {
			o, _ := prepareFixture(t, backend)
			calls := map[string]int{}
			lookup := o.Lookup
			o.Lookup = func(name string) (string, error) { calls[filepath.Base(name)]++; return lookup(name) }
			if _, err := Prepare(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			if backend == "cli" {
				if calls["claude"] != 1 || calls["codex"] != 1 || calls["gashki"] != 0 {
					t.Fatal(calls)
				}
			} else if calls["gashki"] != 1 || len(calls) != 1 {
				t.Fatal(calls)
			}
		})
	}
}

func TestMatchingExistingRecordsRequirePrivateMode(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0644} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			o, _ := prepareFixture(t, "cli")
			b, err := o.Resolved.TOML()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(o.Store.Path, "volley.config.toml")
			if err := os.WriteFile(path, b, mode); err != nil {
				t.Fatal(err)
			}
			p, err := Prepare(context.Background(), o)
			if mode == 0600 {
				if err != nil {
					t.Fatal(err)
				}
				if err := p.Gate.Check(); err != nil {
					t.Fatal(err)
				}
			} else {
				requireErrorCode(t, err, "OUTPUT_CONFLICT")
			}
		})
	}
}
