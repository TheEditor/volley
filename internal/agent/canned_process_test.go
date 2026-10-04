//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/process"
)

type cannedFixture struct {
	StateDir, SocketPath string
	ServerVars           map[string]*string
	CallPath             string
}

func TestOwnedMetadataProcess(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "--" {
			marker = i + 1
			break
		}
	}
	if marker < 0 {
		return
	}
	args := os.Args[marker:]
	if len(args) < 2 {
		os.Exit(2)
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		os.Exit(2)
	}
	var fixture cannedFixture
	if json.Unmarshal(b, &fixture) != nil {
		os.Exit(2)
	}
	log, err := os.OpenFile(fixture.CallPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	entry, _ := json.Marshal(map[string]any{"provider": args[1], "args": args[2:]})
	_, _ = log.Write(append(entry, '\n'))
	_ = log.Sync()
	_ = log.Close()
	r := &metadataRunner{StateDir: fixture.StateDir, SocketPath: fixture.SocketPath, ServerVars: fixture.ServerVars}
	result, err := r.Run(context.Background(), process.Request{Path: args[1], Args: args[2:], Stdout: os.Stdout})
	if err != nil {
		os.Exit(2)
	}
	os.Exit(result.Exit)
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func TestAREC01OwnedMetadataProcesses(t *testing.T) {
	for _, backend := range []string{"cli", "gashki"} {
		t.Run(backend, func(t *testing.T) {
			o, r := prepareFixture(t, backend)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			fixturePath := filepath.Join(filepath.Dir(o.Store.Path), "metadata-fixture.json")
			callPath := fixturePath + ".calls"
			b, _ := json.Marshal(cannedFixture{r.StateDir, r.SocketPath, r.ServerVars, callPath})
			if err := os.WriteFile(fixturePath, b, 0600); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Dir(o.TmuxPath)
			for _, name := range []string{"claude", "codex", "gashki", "tmux"} {
				script := fmt.Sprintf("#!/bin/sh\nexec %s -test.run=^TestOwnedMetadataProcess$ -- %s %s \"$@\"\n", shellQuote(exe), shellQuote(fixturePath), shellQuote(name))
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			o.Runner = process.UnixRunner{}
			callbacks := 0
			p, err := PrepareAndCall(context.Background(), o, "direct launch", func(p Prepared) error { callbacks++; return p.Gate.Check() })
			if err != nil || callbacks != 1 {
				t.Fatalf("%d %v", callbacks, err)
			}
			calls, err := os.ReadFile(callPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				var call map[string]any
				if json.Unmarshal([]byte(line), &call) != nil {
					t.Fatal("bad call receipt")
				}
				args, _ := json.Marshal(call["args"])
				if strings.Contains(string(args), "spawn") || strings.Contains(string(args), "exec") || strings.Contains(string(args), "--print") {
					t.Fatal("agent turn in metadata fixture")
				}
			}
			if backend == "gashki" && p.StateDir != r.StateDir {
				t.Fatal("queried state binding differed")
			}
			if p.Record.Caller.ClaudeRoot.Present {
				t.Fatal("absent caller root exported")
			}
			fresh, _, err := config.Resolve(config.ResolveOptions{Cwd: o.Store.Path, Home: r.Home, NamedFile: filepath.Join(o.Store.Path, "volley.config.toml")})
			if err != nil || fresh.Settings.Backend != backend {
				t.Fatalf("%+v %v", fresh.Settings, err)
			}
			t.Logf("owned metadata processes=%d dependent recording callbacks=%d vendor conversations=0", len(strings.Split(strings.TrimSpace(string(calls)), "\n")), callbacks)
		})
	}
}
