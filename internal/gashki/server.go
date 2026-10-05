//go:build darwin || linux

package gashki

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/process"
	"golang.org/x/sys/unix"
)

type ServerBinding struct {
	SocketPath   string `json:"socket_path"`
	Device       uint64 `json:"device"`
	Inode        uint64 `json:"inode"`
	CallerWindow string `json:"caller_window"`
}
type ServerView struct {
	Server                                   ServerBinding
	UUID, Provider, Selector, Target, Window string
}
type ServerObserver interface {
	Snapshot(context.Context, *string, []string, string, Budget) (ServerView, error)
}
type TmuxObserver struct {
	Path   string
	Runner process.Runner
	Gate   func(context.Context, string) error
}

func CanonicalSocket(path string) (ServerBinding, error) {
	var binding ServerBinding
	if !filepath.IsAbs(path) {
		return binding, fmt.Errorf("Socket path is unknown")
	}
	physical, e := filepath.EvalSymlinks(path)
	if e != nil {
		return binding, e
	}
	var stat unix.Stat_t
	if e := unix.Stat(physical, &stat); e != nil {
		return binding, e
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return binding, fmt.Errorf("Socket path is not a socket")
	}
	binding.SocketPath = physical
	binding.Device = uint64(stat.Dev)
	binding.Inode = uint64(stat.Ino)
	return binding, nil
}
func (o TmuxObserver) Snapshot(ctx context.Context, socket *string, env []string, target string, budget Budget) (ServerView, error) {
	var view ServerView
	if !filepath.IsAbs(o.Path) || o.Runner == nil || o.Gate == nil {
		return view, fmt.Errorf("Incomplete tmux observer")
	}
	if e := o.Gate(ctx, "tmux metadata"); e != nil {
		return view, e
	}
	args := []string{}
	if socket != nil {
		args = append(args, "-L", *socket)
	}
	format := "#{socket_path}|#{@gashki_id}|#{@gashki_agent}|#{@gashki_group}/#{@gashki_name}|#{pane_id}|#{window_id}"
	if target == "" {
		// A detached server has no current client for display-message. List its
		// pane metadata to observe the selected server without adopting a pane.
		args = append(args, "list-panes", "-a", "-F", format)
	} else {
		args = append(args, "display-message", "-p", "-t", target, format)
	}
	var stdout, stderr bytes.Buffer
	out, errout := &limitedSink{Writer: &stdout}, &limitedSink{Writer: &stderr}
	limit, elapsed := budget.Limit, budget.Elapsed()
	if limit == 0 {
		limit = 10 * time.Second
		elapsed = 0
	}
	fact, e := o.Runner.Run(ctx, process.Request{Path: o.Path, Args: args, Env: env, Timeout: limit, ElapsedBefore: elapsed, Stdout: out, Stderr: errout})
	if e != nil || !fact.Settled || fact.Outcome != process.Exited || fact.Exit != 0 {
		return view, fmt.Errorf("Selected tmux metadata is unavailable: exit=%d outcome=%s settled=%t: %v: %s", fact.Exit, fact.Outcome, fact.Settled, e, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	fields := metadataFields(lines[0])
	if len(fields) != 6 {
		return view, fmt.Errorf("Selected tmux metadata is incomplete: %q", stdout.String())
	}
	view.Server, e = CanonicalSocket(fields[0])
	if e != nil {
		return view, e
	}
	for _, line := range lines[1:] {
		other := metadataFields(line)
		if len(other) != 6 || other[0] != fields[0] {
			return view, fmt.Errorf("Selected tmux server metadata disagrees")
		}
	}
	view.UUID, view.Provider, view.Selector, view.Target, view.Window = fields[1], fields[2], fields[3], fields[4], fields[5]
	if target != "" && view.Target != target {
		return view, fmt.Errorf("Selected tmux target did not resolve exactly")
	}
	return view, nil
}

func metadataFields(line string) []string {
	fields := strings.Split(line, "|")
	if len(fields) < 6 {
		return nil
	}
	// Pane labels and generated identities cannot contain a pipe. The socket
	// path can; retain every preceding segment as part of that path.
	start := len(fields) - 5
	return append([]string{strings.Join(fields[:start], "|")}, fields[start:]...)
}

func sameServer(a, b ServerBinding) bool {
	return a.SocketPath != "" && a.SocketPath == b.SocketPath && a.Device == b.Device && a.Inode == b.Inode
}
func environmentValue(env []string, key string) string {
	value := ""
	for _, v := range env {
		if strings.HasPrefix(v, key+"=") {
			value = strings.TrimPrefix(v, key+"=")
		}
	}
	return value
}
func selector(runID, role string) (string, error) {
	if !validID(runID) || role != "planner" && role != "critic" && role != "second" {
		return "", NativeError(decision("INTERNAL", "generated_pane_selector_invalid"), nil)
	}
	return "vly-" + runID + "/" + role, nil
}

type PaneBinding struct {
	RunID         string        `json:"run_id"`
	Role          string        `json:"role"`
	UUID          string        `json:"uuid"`
	Provider      string        `json:"provider"`
	Selector      string        `json:"selector"`
	Target        string        `json:"target"`
	ReadyCursor   string        `json:"ready_cursor"`
	Server        ServerBinding `json:"server"`
	ConfigHash    string        `json:"config_hash"`
	AgentArgsHash string        `json:"agent_args_hash"`
	SessionID     *string       `json:"session_id"`
	SessionReason string        `json:"session_reason"`
}
type PaneManager struct {
	Client         *Client
	Observer       ServerObserver
	ExpectedServer ServerBinding
}

func (m *PaneManager) selected(ctx context.Context, target string, budget Budget) (ServerView, error) {
	if m.Client == nil || m.Observer == nil || m.ExpectedServer.SocketPath == "" {
		return ServerView{}, NativeError(decision("TURN_UNCERTAIN", "server_identity_unknown"), nil)
	}
	if e := m.Client.options.Gate(ctx, "gashki config"); e != nil {
		return ServerView{}, e
	}
	if _, e := m.Client.ConfigHash(); e != nil {
		return ServerView{}, e
	}
	view, e := m.Observer.Snapshot(ctx, m.Client.socketName, m.Client.options.Env, target, budget)
	if e != nil {
		return view, NativeError(decision("SERVER_CONFLICT", "saved_server_cannot_be_verified"), map[string]any{"observation_error": e.Error()})
	}
	if !sameServer(m.ExpectedServer, view.Server) {
		return view, NativeError(decision("SERVER_CONFLICT", "selected_server_differs"), nil)
	}
	return view, nil
}
func (m *PaneManager) CheckPane(ctx context.Context, pane PaneBinding, budget Budget) error {
	if pane.RunID != m.Client.options.RunID || !sameServer(pane.Server, m.ExpectedServer) {
		return NativeError(decision("SERVER_CONFLICT", "saved_pane_server_binding_differs"), nil)
	}
	view, e := m.selected(ctx, "", budget)
	if e != nil {
		return e
	}
	if pane.Server.CallerWindow != "" {
		caller := environmentValue(m.Client.options.Env, "TMUX_PANE")
		if caller == "" {
			return NativeError(decision("PANE_CONFLICT", "here_caller_is_missing"), nil)
		}
		view, e = m.selected(ctx, caller, budget)
		if e != nil {
			return e
		}
		if view.Window != pane.Server.CallerWindow {
			return NativeError(decision("PANE_CONFLICT", "here_caller_window_changed"), nil)
		}
	}
	view, e = m.selected(ctx, pane.Target, budget)
	if e != nil {
		// Check the selected server first, then ask the public UUID lookup.
		// A missing tmux target alone cannot prove that the saved pane is gone.
		if _, again := m.selected(ctx, "", budget); again != nil {
			return again
		}
		call, err := m.Client.FreshCall(ctx, "observe", nil, budget, "observe", pane.UUID)
		if err != nil {
			return err
		}
		if call.ValidationError == "" && !call.Response.OK && (call.Response.Errors[0].Code == "NOT_FOUND" || call.Response.Errors[0].Code == "PANE_DEAD") {
			return NativeError(decision("SESSION_LOST", "saved_pane_no_longer_available"), map[string]any{"call_id": call.ID})
		}
		if call.ValidationError == "" && call.Response.OK {
			d := Map(call.Response.Data)
			if d["id"] == pane.UUID && d["name"] == pane.Selector && d["agent"] == pane.Provider && d["target"] == pane.Target && d["state"] == "dead" {
				return NativeError(decision("SESSION_LOST", "saved_pane_dead"), map[string]any{"call_id": call.ID})
			}
		}
		return NativeError(decision("TURN_UNCERTAIN", "saved_pane_metadata_unverified"), nil)
	}
	if view.UUID != pane.UUID || view.Provider != pane.Provider || view.Selector != pane.Selector || view.Target != pane.Target {
		return NativeError(decision("PANE_CONFLICT", "saved_pane_identity_changed"), map[string]any{"expected_uuid": pane.UUID, "observed_uuid": view.UUID})
	}
	return nil
}
