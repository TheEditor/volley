//go:build darwin || linux

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/TheEditor/volley/internal/input"
)

type limitedWriter struct {
	sink  io.Writer
	bytes int
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	if w.bytes+len(b) > 32<<20 {
		return 0, fmt.Errorf("Direct output exceeds its byte limit")
	}
	n, err := w.sink.Write(b)
	w.bytes += n
	return n, err
}

type codexWriter struct {
	sink       io.Writer
	bytes      int
	pending    []byte
	session    string
	threads    int
	completed  bool
	conflict   bool
	err        error
	onIdentity func(string) error
}

func (w *codexWriter) Write(b []byte) (int, error) {
	if w.bytes+len(b) > 32<<20 {
		return 0, fmt.Errorf("Codex output exceeds its byte limit")
	}
	n, err := w.sink.Write(b)
	w.bytes += n
	if err != nil {
		return n, err
	}
	if w.err != nil {
		return n, nil
	}
	w.pending = append(w.pending, b[:n]...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		line := w.pending[:end]
		w.event(line)
		w.pending = w.pending[end+1:]
		if w.err != nil {
			w.pending = nil
			return n, nil
		}
	}
	if len(w.pending) > input.Limit {
		w.err = fmt.Errorf("Codex event exceeds its byte limit")
		w.pending = nil
	}
	return n, nil
}
func (w *codexWriter) event(line []byte) {
	v, err := input.Record(bytes.NewReader(line), input.Limit)
	if err != nil {
		w.err = fmt.Errorf("Malformed Codex event: %w", err)
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		w.err = fmt.Errorf("Codex event must be an object")
		return
	}
	kind, ok := m["type"].(string)
	if !ok {
		w.err = fmt.Errorf("Codex event type is missing")
		return
	}
	switch kind {
	case "thread.started":
		id, ok := m["thread_id"].(string)
		w.threads++
		if ok && validUUID(id) && w.session != "" && w.session != id {
			w.conflict = true
		}
		if !ok || !validUUID(id) || w.threads != 1 || w.completed || w.session != "" && w.session != id {
			w.err = fmt.Errorf("Codex thread identity is invalid or differs")
			return
		}
		if err := w.onIdentity(id); err != nil {
			w.err = err
			return
		}
		w.session = id
	case "turn.completed":
		if w.completed {
			w.err = fmt.Errorf("Duplicate Codex completion event")
			return
		}
		w.completed = true
		usage, ok := m["usage"].(map[string]any)
		if !ok {
			w.err = fmt.Errorf("Codex completion usage is invalid")
			return
		}
		for _, name := range []string{"input_tokens", "output_tokens"} {
			n, ok := usage[name].(json.Number)
			if !ok {
				w.err = fmt.Errorf("Codex completion usage is invalid")
				return
			}
			value, err := n.Int64()
			if err != nil || value < 0 {
				w.err = fmt.Errorf("Codex completion usage is invalid")
				return
			}
		}
	case "turn.started":
	case "item.started", "item.updated", "item.completed":
		item, ok := m["item"].(map[string]any)
		if !ok {
			w.err = fmt.Errorf("Codex item event is invalid")
			return
		}
		for _, name := range []string{"id", "type"} {
			value, ok := item[name].(string)
			if !ok || value == "" {
				w.err = fmt.Errorf("Codex item event is invalid")
				return
			}
		}
	case "turn.failed", "error":
		w.err = fmt.Errorf("Codex reported an upstream failure")
	default:
		w.err = fmt.Errorf("Unsupported Codex event type %q", kind)
	}
}
func (w *codexWriter) finish() {
	if w.err == nil && len(w.pending) != 0 {
		w.err = fmt.Errorf("Incomplete final Codex event line")
	}
}
