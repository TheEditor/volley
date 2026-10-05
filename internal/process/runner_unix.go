//go:build darwin || linux

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const TermGrace = 5 * time.Second
const DrainGrace = 250 * time.Millisecond

type UnixRunner struct{}
type observation struct {
	Identity
	Parent int
	Zombie bool
}

func Inspect(saved Identity) (bool, error) {
	if !saved.Verified || saved.PID <= 1 || saved.PGID <= 1 || saved.Start == "" {
		return false, nil
	}
	all, err := platformProcesses()
	if err != nil {
		return false, err
	}
	for _, p := range all {
		if p.PID == saved.PID {
			return p.Identity == saved && !p.Zombie, nil
		}
	}
	return false, nil
}

// RecoverIdentity does not infer completion from an absent PID. Recovery needs
// a saved completion or explicit abandonment before a replacement turn.
func RecoverIdentity(saved Identity) (Result, error) {
	valid, err := Inspect(saved)
	result := Result{Outcome: Uncertain, Started: true, Identity: saved, Exit: -1, Signals: []string{}, ObservedMembers: []Identity{}}
	result.Identity.Verified = valid
	if valid {
		result.RemainingMembers = 1
	}
	return result, err
}

// SignalVerified requires current kernel start identity. An absent process or
// a different identity is uncertain. No signal is sent in that case.
func SignalVerified(saved Identity, signal syscall.Signal) (bool, error) {
	valid, err := Inspect(saved)
	if err != nil || !valid {
		return false, err
	}
	if saved.PID != saved.PGID {
		return false, fmt.Errorf("Saved owner is not a group leader")
	}
	if err := unix.Kill(-saved.PGID, signal); err != nil {
		return false, err
	}
	return true, nil
}

func (UnixRunner) Run(ctx context.Context, req Request) (result Result, err error) {
	began := time.Now()
	result.Outcome = NotStarted
	result.Exit = -1
	result.Signals = []string{}
	result.ObservedMembers = []Identity{}
	defer func() {
		result.Duration = time.Since(began)
		result.BudgetConsumed = req.ElapsedBefore + result.Duration
	}()
	if req.Timeout < 0 || req.ElapsedBefore < 0 {
		return result, fmt.Errorf("Negative process budget")
	}
	if req.UserTTY != nil && len(req.Input) > 0 {
		return result, fmt.Errorf("Editor terminal and owned tool input cannot be combined")
	}
	if req.Timeout > 0 && req.ElapsedBefore >= req.Timeout {
		result.Outcome = DeadlineExpired
		result.TimedOut = true
		result.Settled = true
		return result, nil
	}
	if ctx.Err() != nil {
		result.Outcome = Interrupted
		result.Interrupted = true
		result.Settled = true
		return result, ctx.Err()
	}
	for _, v := range append(append([]string{req.Path, req.Cwd}, req.Args...), req.Env...) {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return result, fmt.Errorf("Process argument contains invalid UTF-8 or NUL")
		}
	}
	cmd := exec.Command(req.Path, req.Args...)
	cmd.Dir = req.Cwd
	cmd.Env = req.Env
	cmd.Stdin = nil
	if req.UserTTY != nil {
		if !IsTerminal(req.UserTTY) {
			return result, fmt.Errorf("Editor input must be a terminal")
		}
		cmd.Stdin = req.UserTTY
	} else if len(req.Input) > 0 {
		cmd.Stdin = bytes.NewReader(req.Input)
	}
	cmd.Stdout = req.Stdout
	cmd.Stderr = req.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = io.Discard
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = DrainGrace
	if err := cmd.Start(); err != nil {
		return result, err
	}
	result.Started = true
	known := make(map[int]Identity)
	all, observeErr := platformProcesses()
	for _, p := range all {
		if p.PID == cmd.Process.Pid && p.PGID == cmd.Process.Pid {
			result.Identity = p.Identity
			known[p.PID] = p.Identity
		}
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	if observeErr != nil || !result.Identity.Verified {
		// The current exec handle owns the direct child. Killing that handle is
		// distinct from trusting a saved PID. Descendants remain uncertain.
		_ = cmd.Process.Kill()
		select {
		case <-wait:
		case <-time.After(DrainGrace):
		}
		result.Outcome = Uncertain
		return result, fmt.Errorf("Cannot verify process start identity")
	}
	// refresh admits group members only while a previously verified member still
	// anchors this group. This prevents killing a reused PGID after leader exit.
	refresh := func() (live int, anchored bool, err error) {
		all, err := platformProcesses()
		if err != nil {
			return 0, false, err
		}
		for _, p := range all {
			if owner, ok := known[p.PID]; ok && owner == p.Identity && p.PGID == result.Identity.PGID {
				anchored = true
			}
		}
		for _, p := range all {
			if p.PGID != result.Identity.PGID {
				continue
			}
			if !anchored {
				return 0, false, fmt.Errorf("Process group identity changed")
			}
			known[p.PID] = p.Identity
			if !p.Zombie {
				live++
			}
		}
		return live, anchored, nil
	}
	record := func(live int) {
		result.RemainingMembers = live
		result.ObservedMembers = make([]Identity, 0, len(known))
		for _, id := range known {
			result.ObservedMembers = append(result.ObservedMembers, id)
		}
		sort.Slice(result.ObservedMembers, func(i, j int) bool { return result.ObservedMembers[i].PID < result.ObservedMembers[j].PID })
	}
	stop := func() error {
		live, anchored, e := refresh()
		record(live)
		if e != nil {
			return e
		}
		if live == 0 {
			return nil
		}
		if !anchored {
			return fmt.Errorf("No verified group owner")
		}
		if e := unix.Kill(-result.Identity.PGID, unix.SIGTERM); e != nil && e != unix.ESRCH {
			return e
		}
		result.Signals = append(result.Signals, "TERM")
		// Fixed grace for processes which retain the group after TERM.
		until := time.Now().Add(TermGrace)
		for time.Now().Before(until) {
			live, _, e = refresh()
			record(live)
			if e != nil {
				return e
			}
			if live == 0 {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		live, anchored, e = refresh()
		record(live)
		if e != nil {
			return e
		}
		if live > 0 {
			if !anchored {
				return fmt.Errorf("Group ownership became uncertain")
			}
			if e := unix.Kill(-result.Identity.PGID, unix.SIGKILL); e != nil && e != unix.ESRCH {
				return e
			}
			result.Signals = append(result.Signals, "KILL")
		}
		until = time.Now().Add(DrainGrace)
		for {
			live, _, e = refresh()
			record(live)
			if e != nil {
				return e
			}
			if live == 0 {
				return nil
			}
			if !time.Now().Before(until) {
				return fmt.Errorf("Process group did not settle")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	defer func() {
		if p := recover(); p != nil {
			_ = stop()
			select {
			case <-wait:
			case <-time.After(DrainGrace):
			}
			panic(p)
		}
	}()
	startErr := error(nil)
	if req.OnStart != nil {
		startErr = req.OnStart(result.Identity)
	}
	if startErr != nil {
		err = stop()
		if err == nil {
			result.Settled = true
		}
		result.Outcome = Uncertain
		select {
		case <-wait:
		case <-time.After(DrainGrace):
		}
		return result, startErr
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var timer *time.Timer
	var deadline <-chan time.Time
	if req.Timeout > 0 {
		remaining := req.Timeout - req.ElapsedBefore - time.Since(began)
		if remaining < 0 {
			remaining = 0
		}
		timer = time.NewTimer(remaining)
		defer timer.Stop()
		deadline = timer.C
	}
	var waitErr error
	waited := false
	cancelled := false
	for !waited && !cancelled {
		select {
		case waitErr = <-wait:
			waited = true
			result.Outcome = Exited
		case <-ctx.Done():
			result.Interrupted = true
			result.Outcome = Interrupted
			cancelled = true
		case <-deadline:
			result.TimedOut = true
			result.Outcome = DeadlineExpired
			cancelled = true
		case <-ticker.C:
			live, _, e := refresh()
			record(live)
			if e != nil {
				err = e
				result.Outcome = Uncertain
				cancelled = true
			}
		}
	}
	if stopErr := stop(); stopErr != nil {
		err = stopErr
		result.Outcome = Uncertain
	} else {
		result.Settled = true
	}
	if !waited {
		select {
		case waitErr = <-wait:
			waited = true
		case <-time.After(DrainGrace):
			result.Settled = false
			result.Outcome = Uncertain
			err = fmt.Errorf("Direct child wait did not settle")
		}
	}
	if waited && cmd.ProcessState != nil {
		result.Exit = cmd.ProcessState.ExitCode()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.Signal = status.Signal().String()
		}
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		// A pipe owner outlived the bounded drain. Its completion is not proven.
		result.Outcome = Uncertain
		result.Settled = false
	}
	// Nonzero child status is a typed exit fact, not an infrastructure error.
	if waitErr != nil {
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) && !errors.Is(waitErr, exec.ErrWaitDelay) && err == nil {
			err = waitErr
		}
	}
	return result, err
}
