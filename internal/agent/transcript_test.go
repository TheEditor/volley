//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

const replySession = "11111111-2222-4333-8444-555555555555"

type replyFakeClock struct {
	now   time.Time
	tick  func(time.Time)
	waits int
}

func (c *replyFakeClock) Now() time.Time { return c.now }
func (c *replyFakeClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	c.waits++
	if c.tick != nil {
		c.tick(c.now)
	}
	return nil
}
func replyRoot(t *testing.T) string {
	t.Helper()
	for i, arg := range os.Args {
		if arg == "--reply-evidence" && i+1 < len(os.Args) {
			base := os.Args[i+1]
			if !filepath.IsAbs(base) {
				t.Fatal("Evidence root must be absolute")
			}
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			p, err := os.MkdirTemp(base, "case-")
			if err != nil {
				t.Fatal(err)
			}
			t.Log("owned reply evidence " + p)
			return p
		}
	}
	return t.TempDir()
}
func replyFixture(t *testing.T, provider string) (TranscriptRequest, *replyFakeClock, string, []byte, string) {
	t.Helper()
	owned := replyRoot(t)
	workspace := filepath.Join(owned, "workspace spaces [x]")
	root := filepath.Join(owned, "vendor")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(owned)
	if err != nil {
		t.Fatal(err)
	}
	workspace = filepath.Join(physical, "workspace spaces [x]")
	root = filepath.Join(physical, "vendor")
	prompt := filepath.Join(workspace, "state", "prompts", "abcdefghijklmnopqrstuvwxyz-turn2.md")
	q := TranscriptRequest{Provider: provider, Root: root, Workspace: workspace, SessionID: replySession, PromptPath: prompt, CheckedCompletion: true, CompletedAt: time.Unix(100, 0)}
	dir := filepath.Join(root, "sessions", "2026", "10", "04")
	if provider == "claude" {
		dir = filepath.Join(root, "projects", projectEscape.ReplaceAllString(workspace, "-"))
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, replySession+".jsonl")
	b, err := os.ReadFile("../../testdata/transcripts/" + provider + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// Replace JSON string tokens, so special characters keep exact boundaries.
	for token, text := range map[string]string{"__WORKSPACE__": workspace, "__POINTER__": PointerText(prompt)} {
		replacement, _ := json.Marshal(text)
		b = []byte(strings.ReplaceAll(string(b), strconvJSON(token), string(replacement)))
	}
	return q, &replyFakeClock{now: q.CompletedAt}, file, b, owned
}
func strconvJSON(s string) string { b, _ := json.Marshal(s); return string(b) }
func replyWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func replyEvidence(t *testing.T, owned string, q TranscriptRequest, r ReplyCapture, c *replyFakeClock) {
	t.Helper()
	hashes := map[string]string{}
	filepath.WalkDir(owned, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, statErr := d.Info()
		if statErr == nil && info.Mode().IsRegular() {
			if b, err := os.ReadFile(p); err == nil {
				rel, _ := filepath.Rel(owned, p)
				hashes[rel] = contract.HashBytes(b)
			}
		}
		return nil
	})
	record := map[string]any{"case": t.Name(), "acceptance": "A-REPLY-01/A-REPLY-02", "tier": "A", "fixture": "F-FILES", "provider": q.Provider, "reply": r.Reply, "text": string(r.Text), "matches": r.Matches, "elapsed_ms": c.now.Sub(q.CompletedAt).Milliseconds(), "waits": c.waits, "artifact_hashes": hashes}
	b, err := contract.Canonical(record)
	if err != nil {
		t.Fatal(err)
	}
	replyWrite(t, filepath.Join(owned, "evidence.json"), b)
}
func TestTranscriptExactTurn(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			q, c, file, b, owned := replyFixture(t, provider)
			replyWrite(t, file, b)
			other := strings.ReplaceAll(string(b), strconvJSON(PointerText(q.PromptPath)), strconvJSON(PointerText(q.PromptPath+".other")))
			if provider == "claude" {
				other = strings.ReplaceAll(other, replySession, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
				q.SessionID = ""
			}
			latest := filepath.Join(filepath.Dir(file), "newest.jsonl")
			replyWrite(t, latest, []byte(other))
			os.Chtimes(latest, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
			r, err := CaptureTranscript(context.Background(), q, c)
			if err != nil || r.Reply.Kind != "found" || string(r.Text) != "Matched final reply.\n" || r.Matches != 1 || r.Reply.Source != file || r.Reply.Root != q.Root || r.Reply.Version == "" {
				t.Fatalf("%+v %v", r, err)
			}
			s, err := store.Open(q.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.Prepare(); err != nil {
				t.Fatal(err)
			}
			if err = s.AcquireOwner(context.Background(), time.Second); err != nil {
				t.Fatal(err)
			}
			id, _ := store.NewID()
			var paths []string
			saved, err := SaveCapturedReply(s, id, r, func(path string) error { paths = append(paths, path); return nil })
			if err != nil {
				t.Fatal(err)
			}
			schemaBytes, err := contract.Schema("receipt")
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(schemaBytes, &schema); err != nil {
				t.Fatal(err)
			}
			var sample func(map[string]any) any
			sample = func(s map[string]any) any {
				if c, ok := s["const"]; ok {
					return c
				}
				if enum, ok := s["enum"].([]any); ok {
					return enum[0]
				}
				if choices, ok := s["anyOf"].([]any); ok {
					return sample(choices[0].(map[string]any))
				}
				switch s["type"] {
				case "object":
					m := map[string]any{}
					props := s["properties"].(map[string]any)
					for _, key := range s["required"].([]any) {
						m[key.(string)] = sample(props[key.(string)].(map[string]any))
					}
					return m
				case "array":
					return []any{}
				case "integer":
					return 0
				case "boolean":
					return false
				case "null":
					return nil
				default:
					return ""
				}
			}
			receipt := sample(schema).(map[string]any)
			receipt["reply"] = saved.Record()
			if err := contract.Validate("receipt", receipt); err != nil {
				t.Fatal("Reply metadata does not fit the turn receipt", err)
			}
			text, err := s.ReadText(saved.Path)
			if err != nil || string(text) != string(r.Text) || len(paths) != 2 {
				t.Fatalf("Saved %q %v paths %v", text, err, paths)
			}
			raw, err := s.ReadText("state/turns/" + id + "/reply-observation.json")
			if err != nil || strings.Contains(string(raw), "Private unrelated history") {
				t.Fatal("Unrelated history copied")
			}
			replyEvidence(t, owned, q, r, c)
		})
	}
}
func TestTranscriptVariants(t *testing.T) {
	variants := []string{"duplicate-file", "duplicate-turn", "unknown-record", "unknown-block", "wrong-session", "wrong-workspace", "lookalike-path", "next-user-no-completion", "no-final", "partial-record", "sidechain", "completion-error", "wrong-completion-turn", "missing", "symlink", "fifo"}
	for _, provider := range []string{"claude", "codex"} {
		for _, variant := range variants {
			t.Run(provider+"/"+variant, func(t *testing.T) {
				q, c, file, b, owned := replyFixture(t, provider)
				want := "unsupported-format"
				lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
				switch variant {
				case "wrong-completion-turn":
					if provider == "codex" {
						b = []byte(strings.ReplaceAll(string(b), `"type":"task_complete","turn_id":"fixture-turn"`, `"type":"task_complete","turn_id":"different-turn"`))
					} else {
						b = []byte(strings.ReplaceAll(string(b), `"type":"text"`, `"type":"future_text"`))
					}
				case "duplicate-file":
					q.SessionID = ""
					replyWrite(t, filepath.Join(filepath.Dir(file), "second.jsonl"), b)
				case "duplicate-turn":
					b = append(b, b...)
				case "unknown-record":
					b = []byte(strings.Join(append(lines[:2], append([]string{`{"type":"future_record","payload":{}}`}, lines[2:]...)...), "\n") + "\n")
				case "unknown-block":
					if provider == "claude" {
						b = []byte(strings.ReplaceAll(string(b), `"type":"text"`, `"type":"future_text"`))
					} else {
						b = []byte(strings.ReplaceAll(string(b), `"type":"input_text"`, `"type":"future_text"`))
						want = "unsupported-format"
					}
				case "wrong-session":
					b = []byte(strings.ReplaceAll(string(b), replySession, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"))
				case "wrong-workspace":
					b = []byte(strings.ReplaceAll(string(b), strconvJSON(q.Workspace), strconvJSON(q.Workspace+"-other")))
				case "lookalike-path":
					b = []byte(strings.ReplaceAll(string(b), strconvJSON(PointerText(q.PromptPath)), strconvJSON(PointerText(q.PromptPath+".other"))))
					want = "missing"
				case "next-user-no-completion":
					if provider == "codex" {
						b = []byte(strings.Join(append(lines[:4], lines[5:]...), "\n") + "\n")
						want = "pending"
					} else {
						b = []byte(lines[0] + "\n" + lines[4] + "\n" + lines[5] + "\n")
						want = "pending"
					}
				case "no-final":
					if provider == "claude" {
						b = []byte(lines[0] + "\n")
					} else {
						b = []byte(lines[0] + "\n" + lines[1] + "\n")
					}
					want = "pending"
				case "partial-record":
					b = append([]byte(lines[0]+"\n"), []byte(`{"type":`)...)
					if provider == "codex" {
						b = append([]byte(lines[0]+"\n"+lines[1]+"\n"), []byte(`{"type":`)...)
					}
				case "sidechain":
					if provider == "claude" {
						b = []byte(strings.ReplaceAll(string(b), `"type":"assistant"`, `"isSidechain":true,"type":"assistant"`))
					} else {
						b = []byte(strings.ReplaceAll(string(b), `"type":"task_started"`, `"type":"future_event"`))
					}
				case "completion-error":
					if provider == "codex" {
						b = []byte(strings.ReplaceAll(string(b), `"type":"task_complete"`, `"error":{"message":"Failed"},"type":"task_complete"`))
					} else {
						b = []byte(strings.ReplaceAll(string(b), `"type":"text"`, `"type":"error"`))
					}
				case "missing":
					want = "missing"
					b = nil
				case "symlink":
					if err := os.Symlink(filepath.Join(owned, "outside.jsonl"), file); err != nil {
						t.Fatal(err)
					}
					replyWrite(t, filepath.Join(owned, "outside.jsonl"), b)
					b = nil
					want = "read-failed"
				case "fifo":
					if err := syscall.Mkfifo(file, 0600); err != nil {
						t.Fatal(err)
					}
					b = nil
					want = "read-failed"
				}
				if b != nil {
					replyWrite(t, file, b)
				}
				r, err := CaptureTranscript(context.Background(), q, c)
				if err != nil || r.Reply.Kind != want || len(r.Text) != 0 {
					t.Fatalf("want %s got %+v err %v", want, r, err)
				}
				warnings, err := ReplyRequirement(r.Reply, true, false)
				if err != nil || fmt.Sprint(warnings) != "[REPLY_UNAVAILABLE]" {
					t.Fatalf("Planner: %v %v", warnings, err)
				}
				if _, err := ReplyRequirement(r.Reply, true, true); err == nil {
					t.Fatal("Required critique waived")
				}
				if _, err := ReplyRequirement(r.Reply, false, false); err == nil {
					t.Fatal("Invalid spec waived")
				}
				if c.now.Sub(q.CompletedAt) > ReplyCaptureInterval {
					t.Fatal("Capture exceeded deadline")
				}
				replyEvidence(t, owned, q, r, c)
			})
		}
	}
}
func TestTranscriptCaptureInterval(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, variant := range []string{"within", "after", "absent", "read-failed", "late-resume", "pending-then-final"} {
			t.Run(provider+"/"+variant, func(t *testing.T) {
				q, c, file, b, owned := replyFixture(t, provider)
				want := "missing"
				if variant == "late-resume" {
					c.now = q.CompletedAt.Add(ReplyCaptureInterval)
					replyWrite(t, file, b)
				}
				if variant == "read-failed" {
					os.RemoveAll(q.Root)
					replyWrite(t, q.Root, []byte("not a directory"))
					want = "read-failed"
				}
				if variant == "pending-then-final" {
					lines := strings.Split(string(b), "\n")
					initial := lines[0] + "\n"
					if provider == "codex" {
						initial += lines[1] + "\n"
					}
					replyWrite(t, file, []byte(initial))
					want = "found"
				}
				c.tick = func(now time.Time) {
					elapsed := now.Sub(q.CompletedAt)
					if (variant == "within" || variant == "pending-then-final") && elapsed >= 1500*time.Millisecond || variant == "after" && elapsed > 2*time.Second {
						replyWrite(t, file, b)
					}
				}
				if variant == "within" {
					want = "found"
				}
				r, err := CaptureTranscript(context.Background(), q, c)
				if err != nil || r.Reply.Kind != want {
					t.Fatalf("%+v %v", r, err)
				}
				if want == "found" && (string(r.Text) != "Matched final reply.\n" || c.now.Sub(q.CompletedAt) != 1500*time.Millisecond) {
					t.Fatal("Wrong found text or time")
				}
				if variant == "after" || variant == "absent" {
					if c.now.Sub(q.CompletedAt) != 2*time.Second {
						t.Fatal("Wrong interval")
					}
				}
				replyEvidence(t, owned, q, r, c)
			})
		}
	}
}
func TestTranscriptRequiresCheckedContext(t *testing.T) {
	q, c, _, _, _ := replyFixture(t, "claude")
	for _, change := range []func(*TranscriptRequest){func(q *TranscriptRequest) { q.CheckedCompletion = false }, func(q *TranscriptRequest) { q.Root = "relative" }, func(q *TranscriptRequest) { q.SessionID = "bad" }, func(q *TranscriptRequest) { q.CompletedAt = c.now.Add(time.Second) }} {
		altered := q
		change(&altered)
		if _, err := CaptureTranscript(context.Background(), altered, c); err == nil {
			t.Fatal("Unchecked context accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CaptureTranscript(ctx, q, c); err == nil {
		t.Fatal("Cancellation ignored")
	}
	q.Check = func(context.Context) error { return fmt.Errorf("Frozen record changed") }
	if _, err := CaptureTranscript(context.Background(), q, c); err == nil {
		t.Fatal("Gate ignored")
	}
	if _, err := ReplyRequirement(review.Reply{Kind: "found"}, false, true); err == nil {
		t.Fatal("Invalid required artifact accepted")
	}
}

type failedTranscriptReader struct{ io.Reader }

func (r failedTranscriptReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if err == io.EOF {
		return n, fmt.Errorf("Owned read failure")
	}
	return n, err
}
func TestTranscriptReadFailure(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			q, c, _, b, owned := replyFixture(t, provider)
			r := parseTranscript(failedTranscriptReader{strings.NewReader(string(b))}, q, func() bool { return false })
			if r.Reply.Kind != "read-failed" || len(r.Text) != 0 {
				t.Fatalf("%+v", r)
			}
			replyEvidence(t, owned, q, r, c)
		})
	}
}
