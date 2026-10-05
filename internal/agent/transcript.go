//go:build darwin || linux

package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

const ReplyCaptureInterval = 2 * time.Second
const transcriptBytes = 64 << 20
const discoveryBytes = 128 << 20
const transcriptFiles = 4096

// TranscriptRequest contains previously checked completion and identity facts.
// No ambient vendor root, mtime or private Gashki ledger supplies a binding.
type TranscriptRequest struct {
	Provider          string
	Root              string
	Workspace         string
	SessionID         string // Empty only when the vendor identity is not available.
	PromptPath        string
	CompletedAt       time.Time
	CheckedCompletion bool
	Check             func(context.Context) error
}
type ReplyCapture struct {
	Reply   review.Reply
	Text    []byte
	Matches int
}
type ReplyClock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}
type realReplyClock struct{}

func (realReplyClock) Now() time.Time { return time.Now() }
func (realReplyClock) Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func PointerText(path string) string {
	return "Read the file " + strconv.Quote(path) + " and do what it asks."
}

// CaptureTranscript retries only absent or incomplete evidence. A deadline is
// anchored to checked completion, so recovery does not grant another interval.
func CaptureTranscript(ctx context.Context, q TranscriptRequest, clock ReplyClock) (ReplyCapture, error) {
	if clock == nil {
		clock = realReplyClock{}
	}
	if !q.CheckedCompletion || q.CompletedAt.IsZero() || !filepath.IsAbs(q.Root) || !filepath.IsAbs(q.Workspace) || !filepath.IsAbs(q.PromptPath) || strings.ContainsAny(q.PromptPath, "\x00\n\r") || (q.Provider != "claude" && q.Provider != "codex") || (q.SessionID != "" && !validUUID(q.SessionID)) {
		return ReplyCapture{}, fmt.Errorf("Checked completion and transcript identity are required")
	}
	if clock.Now().Before(q.CompletedAt) {
		return ReplyCapture{}, fmt.Errorf("Completion time is in the future")
	}
	deadline := q.CompletedAt.Add(ReplyCaptureInterval)
	unavailable := ReplyCapture{Reply: review.Reply{Kind: "missing", Reason: "capture_interval_elapsed", Root: q.Root, PromptPath: q.PromptPath}}
	for {
		if err := ctx.Err(); err != nil {
			return unavailable, err
		}
		if !clock.Now().Before(deadline) {
			return unavailable, nil
		}
		if q.Check != nil {
			if err := q.Check(ctx); err != nil {
				return unavailable, err
			}
		}
		r := discoverTranscript(ctx, q, func() bool { return !clock.Now().Before(deadline) })
		// No reply obtained after the capture deadline can be accepted.
		if !clock.Now().Before(deadline) {
			return unavailable, nil
		}
		unavailable = r
		if r.Reply.Kind != "missing" && r.Reply.Kind != "pending" {
			return r, nil
		}
		delay := 100 * time.Millisecond
		if left := deadline.Sub(clock.Now()); left < delay {
			delay = left
		}
		if err := clock.Wait(ctx, delay); err != nil {
			return r, err
		}
	}
}
func baseCapture(q TranscriptRequest) ReplyCapture {
	return ReplyCapture{Reply: review.Reply{Kind: "missing", Reason: "prompt_not_found", Root: q.Root, PromptPath: q.PromptPath}}
}

var projectEscape = regexp.MustCompile(`[^A-Za-z0-9]`)

func discoverTranscript(ctx context.Context, q TranscriptRequest, expired func() bool) ReplyCapture {
	result := baseCapture(q)
	physical, err := filepath.EvalSymlinks(q.Root)
	if os.IsNotExist(err) {
		return result
	}
	if err != nil || physical != q.Root {
		result.Reply.Kind = "read-failed"
		result.Reply.Reason = "vendor_root_changed_or_unreadable"
		return result
	}
	root, err := os.OpenRoot(q.Root)
	if err != nil {
		result.Reply.Kind = "read-failed"
		result.Reply.Reason = "vendor_root_unreadable"
		return result
	}
	defer root.Close()
	rootIdentity, err := root.Stat(".")
	if err != nil {
		result.Reply.Kind = "read-failed"
		result.Reply.Reason = "vendor_root_unreadable"
		return result
	}
	start := "sessions"
	if q.Provider == "claude" {
		start = filepath.Join("projects", projectEscape.ReplaceAllString(q.Workspace, "-"))
	}
	var files []string
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if expired() {
			return fmt.Errorf("Capture deadline reached")
		}
		if depth > 8 {
			return fmt.Errorf("Transcript directory depth exceeds limit")
		}
		st, err := root.Lstat(dir)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Transcript directory is not regular")
		}
		f, err := root.OpenFile(dir, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		entries, err := f.ReadDir(transcriptFiles + 1)
		f.Close()
		if err != nil && err != io.EOF {
			return err
		}
		if len(entries) > transcriptFiles {
			return fmt.Errorf("Transcript directory limit exceeded")
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.Type()&os.ModeSymlink != 0 {
				if e.IsDir() || strings.HasSuffix(e.Name(), ".jsonl") {
					return fmt.Errorf("Transcript symlink refused")
				}
				continue
			}
			if e.IsDir() {
				if q.Provider == "codex" {
					if err := walk(p, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if q.Provider == "claude" && q.SessionID != "" && e.Name() != q.SessionID+".jsonl" {
				continue
			}
			files = append(files, p)
			if len(files) > transcriptFiles {
				return fmt.Errorf("Transcript file limit exceeded")
			}
		}
		return nil
	}
	if err = walk(start, 0); err != nil {
		if os.IsNotExist(err) {
			return result
		}
		result.Reply.Kind = "read-failed"
		result.Reply.Reason = "candidate_discovery_failed"
		return result
	}
	var selected ReplyCapture
	var obstruction string
	var total int64
	for _, p := range files {
		if expired() || ctx.Err() != nil {
			result.Reply.Kind = "pending"
			result.Reply.Reason = "capture_interval_elapsed"
			return result
		}
		st, err := root.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() > transcriptBytes {
			obstruction = "candidate_unreadable_or_oversized"
			continue
		}
		total += st.Size()
		if total > discoveryBytes {
			obstruction = "discovery_byte_limit"
			break
		}
		f, err := root.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			obstruction = "candidate_unreadable"
			continue
		}
		before, err := f.Stat()
		if err != nil || !before.Mode().IsRegular() || !os.SameFile(st, before) {
			f.Close()
			obstruction = "candidate_changed"
			continue
		}
		one := parseTranscript(io.LimitReader(f, transcriptBytes+1), q, expired)
		after, err := f.Stat()
		f.Close()
		now, statErr := root.Lstat(p)
		if err != nil || statErr != nil || !os.SameFile(before, after) || !os.SameFile(before, now) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
			obstruction = "candidate_changed"
			continue
		}
		if one.Reply.Kind == "unsupported-format" && one.Matches == 0 {
			obstruction = "unsupported_candidate"
			continue
		}
		if one.Reply.Kind == "read-failed" {
			obstruction = "candidate_read_failed"
			continue
		}
		if one.Matches == 0 {
			continue
		}
		result.Matches += one.Matches
		one.Reply.Source = filepath.Join(q.Root, p)
		if selected.Matches == 0 {
			selected = one
		}
	}
	if result.Matches > 1 {
		result.Reply.Kind = "unsupported-format"
		result.Reply.Reason = "ambiguous_prompt_match"
		return result
	}
	currentRoot, rootErr := os.Lstat(q.Root)
	if rootErr != nil || !os.SameFile(rootIdentity, currentRoot) {
		obstruction = "vendor_root_changed"
	}
	if obstruction != "" {
		result.Reply.Kind = "read-failed"
		if obstruction == "unsupported_candidate" {
			result.Reply.Kind = "unsupported-format"
		}
		result.Reply.Reason = obstruction
		return result
	}
	if result.Matches == 1 {
		return selected
	}
	return result
}

// Parsing keeps only this turn's text and format facts, never the history.
func parseTranscript(reader io.Reader, q TranscriptRequest, expired func() bool) ReplyCapture {
	r := baseCapture(q)
	r.Reply.Format = q.Provider + "-jsonl-v1"
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), input.Limit)
	active, ended, bad, partial := false, false, false, false
	identity := false
	var session, version string
	var turn string
	var total int
	for scanner.Scan() {
		if expired() {
			r.Reply.Kind = "pending"
			r.Reply.Reason = "capture_interval_elapsed"
			return r
		}
		raw := scanner.Bytes()
		total += len(raw) + 1
		if total > transcriptBytes {
			bad = true
			break
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		value, err := input.Record(bytes.NewReader(raw), input.Limit)
		if err != nil {
			bad = true
			continue
		}
		m, ok := value.(map[string]any)
		if !ok {
			bad = true
			continue
		}
		kind, _ := m["type"].(string)
		if q.Provider == "claude" {
			if kind != "user" && kind != "assistant" {
				switch kind {
				case "progress", "system", "attachment", "file-history-snapshot", "queue-operation", "last-prompt":
				default:
					if !ended {
						bad = true
					}
				}
				continue
			}
			id, _ := m["sessionId"].(string)
			cwd, _ := m["cwd"].(string)
			v, _ := m["version"].(string)
			if v != "" {
				if version != "" && version != v {
					bad = true
				}
				version = v
			}
			side, _ := m["isSidechain"].(bool)
			meta, _ := m["isMeta"].(bool)
			summary, _ := m["isCompactSummary"].(bool)
			team, _ := m["teamName"].(string)
			msg, ok := m["message"].(map[string]any)
			if !ok {
				if !ended {
					bad = true
				}
				continue
			}
			text, typed := msg["content"].(string)
			matches := kind == "user" && typed && text == PointerText(q.PromptPath)
			if matches {
				r.Matches++
				active = true
				ended = false
				identity = validUUID(id) && cwd == q.Workspace && (q.SessionID == "" || id == q.SessionID) && !side && !meta && !summary && team == ""
				session = id
				if !identity {
					bad = true
				}
				r.Text = nil
			}
			if !active || ended {
				continue
			}
			if id != session || cwd != q.Workspace || side || meta || summary || team != "" {
				bad = true
				continue
			}
			if kind == "user" && typed && !matches {
				ended = true
				continue
			}
			if kind == "user" && !typed {
				blocks, ok := msg["content"].([]any)
				if !ok {
					bad = true
					continue
				}
				for _, b := range blocks {
					block, ok := b.(map[string]any)
					if !ok || block["type"] != "tool_result" {
						bad = true
					}
				}
				continue
			}
			if kind == "assistant" {
				blocks, ok := msg["content"].([]any)
				if !ok {
					bad = true
					continue
				}
				var text strings.Builder
				for _, b := range blocks {
					block, ok := b.(map[string]any)
					if !ok {
						bad = true
						continue
					}
					switch block["type"] {
					case "text":
						t, ok := block["text"].(string)
						if !ok {
							bad = true
						} else {
							text.WriteString(t)
						}
					case "tool_use", "thinking", "redacted_thinking":
					default:
						bad = true
					}
				}
				if text.Len() != 0 {
					r.Text = []byte(text.String())
				}
			}
		} else {
			p, ok := m["payload"].(map[string]any)
			if !ok {
				if !ended {
					bad = true
				}
				continue
			}
			switch kind {
			case "session_meta":
				id, _ := p["id"].(string)
				cwd, _ := p["cwd"].(string)
				v, _ := p["cli_version"].(string)
				if session != "" && (id != session || v != version) {
					bad = true
				}
				session = id
				version = v
				identity = validUUID(id) && cwd == q.Workspace && (q.SessionID == "" || id == q.SessionID)
			case "response_item":
				role, _ := p["role"].(string)
				typ, _ := p["type"].(string)
				if typ != "message" {
					switch typ {
					case "function_call", "function_call_output", "reasoning", "custom_tool_call", "custom_tool_call_output":
					default:
						if !ended {
							bad = true
						}
					}
					continue
				}
				if role != "user" && role != "assistant" && role != "developer" && role != "system" {
					if !ended {
						bad = true
					}
					continue
				}
				if role == "assistant" {
					blocks, ok := p["content"].([]any)
					if !ok && !ended {
						bad = true
					}
					for _, b := range blocks {
						block, ok := b.(map[string]any)
						if !ok || block["type"] != "output_text" {
							if !ended {
								bad = true
							}
						} else if _, ok := block["text"].(string); !ok && !ended {
							bad = true
						}
					}
				}
				if role != "user" {
					continue
				}
				blocks, ok := p["content"].([]any)
				if !ok {
					bad = true
					continue
				}
				matches := 0
				for _, b := range blocks {
					block, ok := b.(map[string]any)
					if !ok {
						bad = true
						continue
					}
					text, ok := block["text"].(string)
					if block["type"] != "input_text" || !ok {
						bad = true
						continue
					}
					if text == PointerText(q.PromptPath) {
						matches++
					}
				}
				if matches > 0 {
					r.Matches += matches
					active = true
					ended = false
					r.Text = nil
					if !identity {
						bad = true
					}
				} else if active && !ended {
					ended = true
					r.Text = nil
				}
			case "event_msg":
				typ, _ := p["type"].(string)
				if typ == "task_complete" || typ == "turn_complete" {
					if active && !ended {
						ended = true
						id, ok := p["turn_id"].(string)
						if !ok || id == "" || turn == "" || id != turn {
							bad = true
						}
						text, ok := p["last_agent_message"].(string)
						if !ok || p["error"] != nil {
							bad = true
						} else {
							r.Text = []byte(text)
						}
					}
				} else if typ == "task_started" || typ == "turn_started" {
					id, ok := p["turn_id"].(string)
					if !ok || id == "" || active && !ended && turn != "" && id != turn {
						bad = true
					}
					turn = id
				} else {
					// This supported v1 subset does not silently accept new event forms.
					switch typ {
					case "task_started", "turn_started", "user_message", "agent_message", "agent_reasoning", "token_count", "exec_command_begin", "exec_command_end", "exec_command_output_delta", "mcp_tool_call_begin", "mcp_tool_call_end":
					default:
						if !ended {
							bad = true
						}
					}
				}
			case "turn_context":
			default:
				if !ended {
					bad = true
				}
			}
		}
	}
	if scanner.Err() != nil {
		bad = true
		partial = true
	}
	r.Reply.SessionID = session
	r.Reply.Version = version
	if bad {
		r.Text = nil
		r.Reply.Kind = "unsupported-format"
		r.Reply.Reason = "unsupported_or_invalid_record"
		if partial {
			r.Reply.Reason = "record_byte_limit_or_read_failure"
			if scanner.Err() != bufio.ErrTooLong {
				r.Reply.Kind = "read-failed"
				r.Reply.Reason = "transcript_read_failed"
			}
		}
		return r
	}
	if r.Matches == 0 {
		return r
	}
	if len(bytes.TrimSpace(r.Text)) == 0 || bytes.ContainsRune(r.Text, 0) {
		r.Text = nil
		r.Reply.Kind = "pending"
		r.Reply.Reason = "matched_turn_has_no_final_text"
		return r
	}
	r.Reply.Kind = "found"
	r.Reply.Reason = ""
	r.Reply.Hash = contract.HashBytes(r.Text)
	return r
}

// SaveCapturedReply stores only selected text and its bounded observation.
// The engine places the returned metadata in the authoritative turn receipt.
func SaveCapturedReply(s *store.Store, turnID string, r ReplyCapture, register func(string) error) (review.Reply, error) {
	if err := s.PrepareTurn(turnID); err != nil {
		return review.Reply{}, err
	}
	dir := "state/turns/" + turnID
	if r.Reply.Kind == "found" {
		if contract.HashBytes(r.Text) != r.Reply.Hash || len(bytes.TrimSpace(r.Text)) == 0 {
			return review.Reply{}, fmt.Errorf("Captured reply hash differs")
		}
		r.Reply.Path = dir + "/reply.txt"
		if register != nil {
			if err := register(r.Reply.Path); err != nil {
				return review.Reply{}, err
			}
		}
		if _, err := s.StagePrivateText(r.Reply.Path, r.Text); err != nil {
			return review.Reply{}, err
		}
	}
	path := dir + "/reply-observation.json"
	b, err := contract.Canonical(r.Reply)
	if err != nil {
		return review.Reply{}, err
	}
	if register != nil {
		if err := register(path); err != nil {
			return review.Reply{}, err
		}
	}
	if _, err = s.StagePrivateText(path, b); err != nil {
		return review.Reply{}, err
	}
	return r.Reply, nil
}

// ReplyRequirement keeps secondary evidence separate from required artifacts.
func ReplyRequirement(reply review.Reply, artifactValid, required bool) ([]string, error) {
	if !artifactValid || required && reply.Kind != "found" {
		return nil, safeError("ARTIFACT_MISSING", "Required turn artifact is missing", map[string]any{"reply_status": reply.Kind, "reason": reply.Reason})
	}
	if reply.Kind != "found" {
		return []string{"REPLY_UNAVAILABLE"}, nil
	}
	return nil, nil
}
