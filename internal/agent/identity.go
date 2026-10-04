//go:build darwin || linux

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/process"
)

type Variable struct {
	Known   bool    `json:"known"`
	Present bool    `json:"present"`
	Value   *string `json:"value"`
	Reason  string  `json:"reason"`
}
type IdentityContext struct {
	Home       Variable `json:"home"`
	ClaudeRoot Variable `json:"claude_root"`
	CodexRoot  Variable `json:"codex_root"`
}
type ServerContext struct {
	Context      IdentityContext `json:"context"`
	Socket       *string         `json:"socket"`
	Window       string          `json:"window"`
	Reason       string          `json:"reason"`
	SocketDevice uint64          `json:"socket_device"`
	SocketInode  uint64          `json:"socket_inode"`
	SocketKind   string          `json:"socket_kind"`
}
type EffectiveRoots struct {
	Claude       *string `json:"claude"`
	Codex        *string `json:"codex"`
	ClaudeReason string  `json:"claude_reason"`
	CodexReason  string  `json:"codex_reason"`
}

func envVariable(env []string, name, source string) Variable {
	v := Variable{Known: true, Reason: source + ": absent"}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && key == name {
			copyValue := value
			v = Variable{true, true, &copyValue, source + ": present"}
		}
	}
	return v
}
func CaptureIdentity(env []string) IdentityContext {
	return IdentityContext{envVariable(env, "HOME", "caller"), envVariable(env, "CLAUDE_CONFIG_DIR", "caller"), envVariable(env, "CODEX_HOME", "caller")}
}
func unknownIdentity(reason string) IdentityContext {
	v := Variable{Reason: reason}
	return IdentityContext{v, v, v}
}
func ResolveRoots(caller IdentityContext, server *ServerContext) EffectiveRoots {
	home := caller.Home
	claude, codex := caller.ClaudeRoot, caller.CodexRoot
	if server != nil {
		home = server.Context.Home
		if !claude.Present {
			claude = server.Context.ClaudeRoot
		}
		if !codex.Present {
			codex = server.Context.CodexRoot
		}
	}
	resolve := func(variable Variable, subdir string) (*string, string) {
		if !variable.Known {
			return nil, "Effective vendor root is unknown"
		}
		path := ""
		if variable.Present {
			if variable.Value == nil || !filepath.IsAbs(*variable.Value) {
				return nil, "Present vendor root is empty or not absolute"
			}
			path = *variable.Value
		} else {
			if !home.Known || !home.Present || home.Value == nil || !filepath.IsAbs(*home.Value) {
				return nil, "Effective HOME is unknown"
			}
			path = filepath.Join(*home.Value, subdir)
		}
		physical, err := config.Physical(path)
		if err != nil {
			return nil, "Effective root cannot be resolved"
		}
		return &physical, "Checked effective root"
	}
	cr, creason := resolve(claude, ".claude")
	co, coreason := resolve(codex, ".codex")
	return EffectiveRoots{cr, co, creason, coreason}
}
func safeError(code, message string, evidence map[string]any) error {
	registry, err := contract.Load()
	if err != nil {
		return err
	}
	e := registry.Error(code, message)
	e.Evidence = evidence
	return e
}

// ProbeServer reads only tmux metadata. Missing variables are known absent;
// failed or malformed observations stay unknown with a reason.
func ProbeServer(ctx context.Context, runner process.Runner, path string, env []string, cwd, socket, window string, before func() error) ServerContext {
	result := ServerContext{Context: unknownIdentity("Server metadata unavailable"), Window: window, Reason: "Server metadata unavailable"}
	prefix := []string{}
	if socket != "" {
		prefix = []string{"-L", socket}
	}
	call := func(args ...string) (string, process.Result, error) {
		if before != nil {
			if err := before(); err != nil {
				return "", process.Result{}, err
			}
		}
		return runText(ctx, runner, process.Request{Path: path, Args: append(append([]string(nil), prefix...), args...), Cwd: cwd, Env: env, Timeout: 10 * time.Second})
	}
	raw, outcome, err := call("display-message", "-p", "#{socket_path}")
	if err != nil || !normalExit(outcome) || !filepath.IsAbs(strings.TrimSuffix(raw, "\n")) {
		return result
	}
	physical, err := filepath.EvalSymlinks(strings.TrimSuffix(raw, "\n"))
	if err != nil {
		return result
	}
	result.Socket = &physical
	socketInfo, err := os.Lstat(physical)
	if err != nil {
		result.Socket = nil
		return result
	}
	stat, ok := socketInfo.Sys().(*syscall.Stat_t)
	if !ok {
		result.Socket = nil
		return result
	}
	result.SocketDevice = uint64(stat.Dev)
	result.SocketInode = uint64(stat.Ino)
	result.SocketKind = "file"
	if socketInfo.Mode()&os.ModeSocket != 0 {
		result.SocketKind = "socket"
	}
	scope := []string{"-g"}
	if window != "" {
		scope = []string{"-t", window}
	}
	read := func(name string) Variable {
		out, r, err := call(append(append([]string{"show-environment"}, scope...), name)...)
		if err == nil && r.Outcome == process.Exited && r.Exit == 1 && r.Settled && window != "" {
			out, r, err = call("show-environment", "-g", name)
		}
		if err != nil || r.Outcome != process.Exited || !r.Settled {
			return Variable{Reason: "Server metadata read failed"}
		}
		if r.Exit == 1 {
			return Variable{Known: true, Reason: "server: absent"}
		}
		if r.Exit != 0 {
			return Variable{Reason: "Server metadata read failed"}
		}
		out = strings.TrimSuffix(out, "\n")
		if out == "-"+name {
			return Variable{Known: true, Reason: "server: removed"}
		}
		key, value, ok := strings.Cut(out, "=")
		if !ok || key != name || strings.ContainsAny(value, "\r\n\x00") {
			return Variable{Reason: "Server metadata is malformed"}
		}
		return Variable{true, true, &value, "server: present"}
	}
	result.Context = IdentityContext{read("HOME"), read("CLAUDE_CONFIG_DIR"), read("CODEX_HOME")}
	afterSocket, err := os.Lstat(physical)
	if err != nil || !os.SameFile(socketInfo, afterSocket) {
		return ServerContext{Context: unknownIdentity("Server binding changed during observation"), Window: window, Reason: "Server binding changed during observation"}
	}
	result.Reason = "Read-only tmux environment metadata"
	return result
}
func CompareIdentity(saved, current IdentityContext, savedServer, currentServer *ServerContext) error {
	if !reflect.DeepEqual(saved, current) || !reflect.DeepEqual(savedServer, currentServer) {
		return safeError("IDENTITY_CONFLICT", "Identity context or server binding changed", map[string]any{"actor": "external_or_unknown", "reason": "presence, root, HOME, or selected server differs"})
	}
	return nil
}
func variableRecord(v Variable) map[string]any {
	return map[string]any{"present": v.Present, "value": v.Value, "reason": v.Reason}
}
func (c IdentityContext) manifestRecord() map[string]any {
	return map[string]any{"home": c.Home.Value, "claude_root": variableRecord(c.ClaudeRoot), "codex_root": variableRecord(c.CodexRoot)}
}
func (r EffectiveRoots) RequireKnown() error {
	if r.Claude == nil || r.Codex == nil {
		return safeError("BILLING_REFUSED", "Effective vendor identity roots are unknown", map[string]any{"indicator": "effective_roots", "reason": fmt.Sprintf("Claude: %s; Codex: %s", r.ClaudeReason, r.CodexReason)})
	}
	return nil
}
