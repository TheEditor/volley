//go:build darwin || linux

// Package delivery publishes explicit output and local feedback records.
package delivery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"golang.org/x/sys/unix"
)

func failure(code, message string) *contract.Error {
	r, _ := contract.Load()
	return r.Error(code, message)
}

type Receipt struct {
	Target    string `json:"target"`
	Path      string `json:"path"`
	Hash      string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	Duplicate bool   `json:"duplicate"`
}

func read(path string) ([]byte, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, nil, failure("OUTPUT_CONFLICT", "Output target must not be a symlink")
		}
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, failure("OUTPUT_CONFLICT", "Output target must be a regular file")
	}
	if info.Size() > 32<<20 {
		return nil, nil, failure("OUTPUT_CONFLICT", "Existing output exceeds comparison limit")
	}
	b := make([]byte, info.Size())
	n, err := f.ReadAt(b, 0)
	if err != nil && n != len(b) {
		return nil, nil, err
	}
	return b, info, nil
}

// File preserves an existing target unless its exact bytes match or force was
// explicit. New publication uses an exclusive link; replacement uses rename.
func File(ctx context.Context, path string, b []byte, force bool, protected []string) (Receipt, error) {
	receipt := Receipt{Target: "file", Hash: contract.HashBytes(b), Bytes: len(b)}
	abs, err := filepath.Abs(path)
	if err != nil {
		return receipt, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return receipt, err
	}
	path = filepath.Join(parent, filepath.Base(abs))
	receipt.Path = path
	if filepath.Base(path) == "manifest.json" && filepath.Base(parent) == "state" {
		return receipt, failure("OUTPUT_CONFLICT", "Output cannot replace a workspace manifest")
	}

	old, info, err := read(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return receipt, err
	}
	for _, source := range protected {
		if source == "" {
			continue
		}
		sourceAbs, e := filepath.Abs(source)
		if e != nil {
			return receipt, e
		}
		if real, e := filepath.EvalSymlinks(filepath.Dir(sourceAbs)); e == nil {
			sourceAbs = filepath.Join(real, filepath.Base(sourceAbs))
		}
		sourceInfo, _ := os.Stat(sourceAbs)
		if path == sourceAbs || exists && sourceInfo != nil && os.SameFile(info, sourceInfo) {
			return receipt, failure("OUTPUT_CONFLICT", "Output cannot replace a workspace manifest or source config")
		}
	}
	if exists && bytes.Equal(old, b) {
		receipt.Duplicate = true
		return receipt, nil
	}
	if exists && !force {
		return receipt, failure("OUTPUT_CONFLICT", "Existing output differs; use a new path or explicit --force")
	}
	if err = ctx.Err(); err != nil {
		return receipt, err
	}
	f, err := os.CreateTemp(parent, ".volley-delivery-")
	if err != nil {
		return receipt, err
	}
	candidate := f.Name()
	defer os.Remove(candidate)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return receipt, err
	}
	if closeErr != nil {
		return receipt, closeErr
	}
	now, current, e := read(path)
	if exists {
		if e != nil || !os.SameFile(info, current) || !bytes.Equal(old, now) {
			return receipt, failure("OUTPUT_CONFLICT", "Output changed before publication")
		}
		err = os.Rename(candidate, path)
	} else {
		if e == nil || !os.IsNotExist(e) {
			return receipt, failure("OUTPUT_CONFLICT", "Output appeared before publication")
		}
		err = os.Link(candidate, path)
	}
	if err != nil {
		if os.IsExist(err) {
			return receipt, failure("OUTPUT_CONFLICT", "Output appeared during publication")
		}
		return receipt, err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return receipt, err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return receipt, err
	}
	return receipt, nil
}
func Feedback(ctx context.Context, state, text, key string) (map[string]any, error) {
	if !filepath.IsAbs(state) {
		return nil, failure("INVALID_INPUT", "Feedback requires an absolute state root")
	}
	if len(text) > input.Limit || strings.ContainsRune(text, 0) || strings.ContainsRune(key, 0) || len(key) > 1024 {
		return nil, failure("INVALID_INPUT", "Feedback text or key is invalid")
	}
	hash := contract.HashBytes([]byte(text))
	if key == "" {
		key = hash
	}
	id := contract.HashBytes([]byte(key))
	root := filepath.Join(state, "feedback")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(root); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, failure("STATE_INVALID", "Feedback directory is unsafe")
	}
	path := filepath.Join(root, id+".json")
	record := map[string]any{"record_version": 1, "id": id, "key": key, "text": text, "sha256": hash, "bytes": len(text)}
	b, err := contract.Canonical(record)
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	receipt, err := File(ctx, path, b, false, nil)
	if err != nil {
		if e, ok := err.(*contract.Error); ok && e.Code == "OUTPUT_CONFLICT" {
			conflict := failure("IDEMPOTENCY_CONFLICT", "Feedback key is bound to different text")
			return nil, conflict
		}
		return nil, err
	}
	return map[string]any{"id": id, "path": receipt.Path, "sha256": hash, "bytes": len(text), "duplicate": receipt.Duplicate}, nil
}
func Null(b []byte) Receipt {
	return Receipt{Target: "null", Path: "", Hash: contract.HashBytes(b), Bytes: len(b)}
}
