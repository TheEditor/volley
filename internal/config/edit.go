//go:build darwin || linux

package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/process"
	"golang.org/x/sys/unix"
)

type EditResult struct {
	Path          string `json:"path"`
	Changed       bool   `json:"changed"`
	Created       bool   `json:"created"`
	Hash          string `json:"sha256"`
	CandidatePath string `json:"candidate_path"`
}
type EditError struct {
	Code, CandidatePath string
	Err                 error
}

func (e *EditError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *EditError) Unwrap() error { return e.Err }

type EditOptions struct {
	Resolve  ResolveOptions
	LockWait time.Duration
	// BeforeCompare is a local test seam, with no environment trigger.
	BeforeCompare func(string) error
}
type originalFile struct {
	exists bool
	info   os.FileInfo
	hash   string
}

func observeTarget(path string) (originalFile, *Document, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		d, _ := Parse(path, nil)
		return originalFile{}, d, nil
	}
	if err != nil {
		return originalFile{}, nil, err
	}
	if !st.Mode().IsRegular() {
		return originalFile{}, nil, fmt.Errorf("Config mutation target must be regular and must not be a symlink")
	}
	d, err := ReadFile(path)
	if err != nil {
		return originalFile{}, nil, err
	}
	now, err := os.Lstat(path)
	if err != nil || !os.SameFile(st, now) {
		return originalFile{}, nil, fmt.Errorf("Config target changed during read")
	}
	return originalFile{true, now, contract.HashBytes(d.Bytes)}, d, nil
}
func equalOriginal(before, after originalFile) bool {
	return before.exists == after.exists && (!before.exists || os.SameFile(before.info, after.info) && before.hash == after.hash)
}
func lockConfig(ctx context.Context, path string, wait time.Duration) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("Config lock must be regular")
	}
	if wait == 0 {
		wait = time.Second
	}
	until := time.Now().Add(wait)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			file.Close()
			return nil, err
		}
		if time.Now().After(until) {
			file.Close()
			return nil, &EditError{Code: "LOCKED", Err: fmt.Errorf("Config file is locked; wait and try again")}
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func checkLock(file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(file.Name())
	if err != nil || !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		return fmt.Errorf("Config lock identity changed")
	}
	return nil
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func validateCandidate(path string, b []byte, o ResolveOptions) error {
	d, err := Parse(path, b)
	if err != nil {
		return err
	}
	r, err := contract.Load()
	if err != nil {
		return err
	}
	values := make(map[string]any)
	sources := make(map[string]Source)
	for _, s := range r.Settings {
		v, err := Validate(s, s.Default)
		if err != nil {
			return err
		}
		values[s.Key] = v
		sources[s.Key] = Source{Source: "default"}
	}
	for key, v := range d.Values {
		values[key] = v
		sources[key] = Source{Source: "file", Path: path, Line: d.Spans[key].Line}
	}
	return validateResolved(values, sources, o)
}
func prepareEdit(ctx context.Context, o EditOptions) (path string, lock *os.File, before originalFile, d *Document, err error) {
	o.Resolve.Mutating = true
	o.Resolve.AllowMissingNamed = true
	path, _, err = selectFile(o.Resolve)
	if err != nil {
		return
	}
	// Check the leaf before physical parent resolution, to refuse a target link.
	if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink != 0 {
		err = fmt.Errorf("Config mutation refuses a symlink target")
		return
	} else if e != nil && !os.IsNotExist(e) {
		err = e
		return
	}
	parent, e := Physical(filepath.Dir(path))
	if e != nil {
		err = e
		return
	}
	path = filepath.Join(parent, filepath.Base(path))
	if err = os.MkdirAll(parent, 0700); err != nil {
		return
	}
	lock, err = lockConfig(ctx, path+".lock", o.LockWait)
	if err != nil {
		return
	}
	before, d, err = observeTarget(path)
	if err == nil {
		err = validateCandidate(path, d.Bytes, o.Resolve)
	}
	if err != nil {
		_ = lock.Close()
		lock = nil
	}
	return
}
func retainError(code, path string, err error) error {
	return &EditError{Code: code, CandidatePath: path, Err: err}
}
func commitCandidate(path string, lock *os.File, before originalFile, b []byte, o EditOptions) (EditResult, error) {
	result := EditResult{Path: path, Hash: contract.HashBytes(b)}
	if before.exists && before.hash == result.Hash {
		return result, nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".volley-config-candidate-*")
	if err != nil {
		return result, err
	}
	result.CandidatePath = f.Name()
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return result, retainError("OUTPUT_CONFLICT", f.Name(), err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return result, retainError("OUTPUT_CONFLICT", f.Name(), err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return result, retainError("OUTPUT_CONFLICT", f.Name(), err)
	}
	if err := f.Close(); err != nil {
		return result, retainError("OUTPUT_CONFLICT", f.Name(), err)
	}
	if o.BeforeCompare != nil {
		if err := o.BeforeCompare(path); err != nil {
			return result, retainError("OUTPUT_CONFLICT", f.Name(), err)
		}
	}
	if err := checkLock(lock); err != nil {
		return result, retainError("OUTPUT_CONFLICT", f.Name(), err)
	}
	after, _, err := observeTarget(path)
	if err != nil || !equalOriginal(before, after) {
		return result, retainError("OUTPUT_CONFLICT", f.Name(), fmt.Errorf("Target changed during edit"))
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return result, retainError("OUTPUT_CONFLICT", f.Name(), err)
	}
	result.Changed = true
	result.Created = !before.exists
	result.CandidatePath = ""
	if err := syncDir(filepath.Dir(path)); err != nil {
		return result, err
	}
	return result, nil
}
func Patch(ctx context.Context, o EditOptions, updates map[string]any) (EditResult, error) {
	empty, err := Parse("", nil)
	if err != nil {
		return EditResult{}, err
	}
	if _, _, err := empty.Replace(updates); err != nil {
		return EditResult{}, err
	}
	path, lock, before, d, err := prepareEdit(ctx, o)
	if err != nil {
		return EditResult{}, err
	}
	defer lock.Close()
	b, _, err := d.Replace(updates)
	if err != nil {
		return EditResult{}, err
	}
	if err := validateCandidate(path, b, o.Resolve); err != nil {
		return EditResult{}, err
	}
	return commitCandidate(path, lock, before, b, o)
}
func PatchJSON(ctx context.Context, o EditOptions, r io.Reader) (EditResult, error) {
	updates, err := input.Object(r)
	if err != nil {
		return EditResult{}, err
	}
	return Patch(ctx, o, updates)
}
func Set(ctx context.Context, o EditOptions, key, value string) (EditResult, error) {
	v, err := ParseArgument(key, value)
	if err != nil {
		return EditResult{}, err
	}
	return Patch(ctx, o, map[string]any{key: v})
}

// EditorArgs implements only grouping and escaping. All other characters are
// literal argv bytes. It performs no shell expansion or command evaluation.
func EditorArgs(text string) ([]string, error) {
	if text == "" {
		text = "vi"
	}
	args := make([]string, 0)
	var word strings.Builder
	quote := byte(0)
	escaped := false
	started := false
	flush := func() {
		if started {
			args = append(args, word.String())
			word.Reset()
			started = false
		}
	}
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if escaped {
			word.WriteByte(ch)
			started = true
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteByte(ch)
			}
			started = true
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			started = true
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\v' || ch == '\f' {
			flush()
			continue
		}
		word.WriteByte(ch)
		started = true
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("EDITOR has an unmatched quote or trailing backslash")
	}
	flush()
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("EDITOR has no executable")
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("EDITOR contains NUL")
		}
	}
	return args, nil
}

type EditorOptions struct {
	EditOptions
	Terminal       bool
	CI             bool
	Editor         string
	Env            []string
	Runner         process.Runner
	Stdout, Stderr io.Writer
	UserTTY        *os.File
}

func Edit(ctx context.Context, o EditorOptions) (EditResult, error) {
	if !o.Terminal || o.CI {
		return EditResult{}, fmt.Errorf("config edit requires a terminal outside CI")
	}
	args, err := EditorArgs(o.Editor)
	if err != nil {
		return EditResult{}, err
	}
	path, lock, before, d, err := prepareEdit(ctx, o.EditOptions)
	if err != nil {
		return EditResult{}, err
	}
	defer func() {
		if lock != nil {
			_ = lock.Close()
		}
	}()
	candidate, err := os.CreateTemp(filepath.Dir(path), ".volley-editor-*")
	if err != nil {
		return EditResult{}, err
	}
	candidatePath := candidate.Name()
	if _, err := candidate.Write(d.Bytes); err != nil {
		candidate.Close()
		return EditResult{}, retainError("INVALID_CONFIG", candidatePath, err)
	}
	if err := candidate.Sync(); err != nil {
		candidate.Close()
		return EditResult{}, retainError("INVALID_CONFIG", candidatePath, err)
	}
	_ = candidate.Close()
	lockIdentity, err := lock.Stat()
	if err != nil {
		return EditResult{}, retainError("OUTPUT_CONFLICT", candidatePath, err)
	}
	if err := checkLock(lock); err != nil {
		return EditResult{}, retainError("OUTPUT_CONFLICT", candidatePath, err)
	}
	_ = lock.Close()
	lock = nil
	// The editor can remain open for a long time. Reacquire the short lock only
	// for comparison and commit, with the original inode and hash unchanged.
	runner := o.Runner
	if runner == nil {
		runner = process.UnixRunner{}
		if o.UserTTY == nil {
			tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if err != nil {
				return EditResult{}, retainError("INVALID_CONFIG", candidatePath, err)
			}
			defer tty.Close()
			o.UserTTY = tty
		}
	}
	result, err := runner.Run(ctx, process.Request{Path: args[0], Args: append(args[1:], candidatePath), Cwd: o.Resolve.Cwd, Env: o.Env, Stdout: o.Stdout, Stderr: o.Stderr, UserTTY: o.UserTTY})
	if err != nil || result.Outcome != process.Exited || result.Exit != 0 || !result.Settled {
		return EditResult{}, retainError("INVALID_CONFIG", candidatePath, fmt.Errorf("Editor failed or did not settle: %v", err))
	}
	st, err := os.Lstat(candidatePath)
	if err != nil || !st.Mode().IsRegular() {
		return EditResult{}, retainError("INVALID_CONFIG", candidatePath, fmt.Errorf("Editor candidate must be regular"))
	}
	edited, err := ReadFile(candidatePath)
	if err == nil {
		err = validateCandidate(path, edited.Bytes, o.Resolve)
	}
	if err != nil {
		return EditResult{}, retainError("INVALID_CONFIG", candidatePath, err)
	}
	lock, err = lockConfig(ctx, path+".lock", o.LockWait)
	if err != nil {
		return EditResult{}, retainError("OUTPUT_CONFLICT", candidatePath, err)
	}
	currentLock, err := lock.Stat()
	if err != nil || !os.SameFile(lockIdentity, currentLock) {
		return EditResult{}, retainError("OUTPUT_CONFLICT", candidatePath, fmt.Errorf("Config lock was replaced while editor was open"))
	}
	committed, err := commitCandidate(path, lock, before, edited.Bytes, o.EditOptions)
	if err != nil {
		return committed, retainError("OUTPUT_CONFLICT", candidatePath, err)
	}
	_ = os.Remove(candidatePath)
	return committed, nil
}
