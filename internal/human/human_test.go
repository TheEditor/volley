//go:build darwin || linux

package human

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

type fakeClock struct {
	calls int
	tick  func(int)
}

func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if d != PollInterval {
		return fmt.Errorf("Wrong poll interval")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.calls++
	if c.tick != nil {
		c.tick(c.calls)
	}
	return nil
}
func ownedRoot(t *testing.T) string {
	t.Helper()
	for i, arg := range os.Args {
		if arg == "--human-evidence" && i+1 < len(os.Args) {
			base := os.Args[i+1]
			if !filepath.IsAbs(base) {
				t.Fatal("Evidence path must be absolute")
			}
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			path, err := os.MkdirTemp(base, "case-")
			if err != nil {
				t.Fatal(err)
			}
			t.Log("owned human evidence " + path)
			return path
		}
	}
	return t.TempDir()
}
func sampleSchema(t *testing.T, name string) store.Snapshot {
	t.Helper()
	b, err := contract.Schema(name)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	var sample func(map[string]any) any
	sample = func(s map[string]any) any {
		if c, ok := s["const"]; ok {
			return c
		}
		if e, ok := s["enum"].([]any); ok {
			return e[0]
		}
		if choices, ok := s["anyOf"].([]any); ok {
			for _, choice := range choices {
				m := choice.(map[string]any)
				if m["type"] == "null" {
					return nil
				}
			}
			return sample(choices[0].(map[string]any))
		}
		typ, ok := s["type"].(string)
		if !ok {
			return nil
		}
		switch typ {
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
		default:
			return ""
		}
	}
	return store.Snapshot(sample(schema).(map[string]any))
}
func fixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	root := ownedRoot(t)
	s, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err = s.AcquireOwner(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	m := sampleSchema(t, "manifest")
	run, _ := store.NewID()
	st, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	identity := st.Sys().(*syscall.Stat_t)
	m["run_id"] = run
	m["canonical_workspace"] = s.Path
	m["workspace"] = s.Path
	m["ownership"] = map[string]any{"device": uint64(identity.Dev), "inode": uint64(identity.Ino)}
	m["current_turn"] = nil
	m["status"] = "ready"
	m["phase"] = "draft"
	tx, err := s.NewTransaction("initialize", m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitTransaction(tx); err != nil {
		t.Fatal(err)
	}
	write(t, s, "SPEC.md", "Exact provisional spec.\n")
	write(t, s, "QUESTIONS.md", "1. Choose A or B.\nRecommendation: A.\n")
	return s, root
}
func write(t *testing.T, s *store.Store, path, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Path, path), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func openQuestion(t *testing.T, s *store.Store) *Generation {
	t.Helper()
	id, _ := store.NewID()
	g, err := OpenQuestion(context.Background(), s, id, 2, &fakeClock{})
	if err != nil || g == nil {
		t.Fatalf("Open %v %v", g, err)
	}
	return g
}
func errorCode(err error) string {
	var se *store.Error
	if errors.As(err, &se) {
		return se.Code
	}
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return fmt.Sprint(err)
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	if errorCode(err) != code {
		t.Fatalf("want %s got %v", code, err)
	}
}
func evidence(t *testing.T, s *store.Store, root string, facts any) {
	t.Helper()
	hashes := map[string]string{}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err == nil && info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			hashes[rel] = contract.HashBytes(b)
		}
		return nil
	})
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	b, err := contract.Canonical(map[string]any{"case": t.Name(), "tier": "A", "fixture": "F-FILES/F-PURE", "facts": facts, "manifest": m, "artifact_hashes": hashes, "test_binary_sha256": contract.HashBytes(binary)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "evidence.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestAHUMAN01Freshness(t *testing.T) {
	for _, variant := range []string{"unchanged", "changed", "same-inode-same-bytes", "new-inode-same-bytes", "partial-append", "absent", "whitespace-questions", "heading-questions"} {
		t.Run(variant, func(t *testing.T) {
			s, root := fixture(t)
			write(t, s, "HUMAN.md", "Prior exact directive.\n")
			if variant == "whitespace-questions" {
				write(t, s, "QUESTIONS.md", " \n\t")
				g, err := OpenQuestion(context.Background(), s, "turn", 2, &fakeClock{})
				if err != nil || g != nil {
					t.Fatal("Whitespace opens a gate")
				}
				evidence(t, s, root, "no gate")
				return
			}
			if variant == "heading-questions" {
				write(t, s, "QUESTIONS.md", "# Questions\n")
			}
			g := openQuestion(t, s)
			c := &fakeClock{}
			want := false
			switch variant {
			case "changed":
				write(t, s, "HUMAN.md", "Changed exact answer.\n")
				want = true
			case "same-inode-same-bytes":
				write(t, s, "HUMAN.md", g.PriorDirective)
			case "new-inode-same-bytes":
				p := filepath.Join(s.Path, "replacement")
				os.WriteFile(p, []byte(g.PriorDirective), 0600)
				os.Rename(p, filepath.Join(s.Path, "HUMAN.md"))
				want = true
			case "partial-append":
				write(t, s, "HUMAN.md", g.PriorDirective+"Partial")
				c.tick = func(int) {
					f, err := os.OpenFile(filepath.Join(s.Path, "HUMAN.md"), os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					f.WriteString(" continued\n")
					f.Close()
				}
			case "absent":
				os.Remove(filepath.Join(s.Path, "HUMAN.md"))
			}
			result, submitted, err := FileAnswer(context.Background(), s, *g, c)
			if variant == "partial-append" {
				requireCode(t, err, "ANSWER_CONFLICT")
				c.tick = nil
				result, submitted, err = FileAnswer(context.Background(), s, *g, c)
				want = true
			}
			if err != nil || submitted != want || want && !result.Selected {
				t.Fatalf("%+v submitted %v err %v", result, submitted, err)
			}
			if submitted {
				if result.Receipt.Text == "" || result.Receipt.PriorDirective != g.PriorDirective {
					t.Fatal("Exact text or legacy interpretation missing")
				}
			}
			evidence(t, s, root, map[string]any{"generation": g, "submission": result, "submitted": submitted, "polls": c.calls})
		})
	}
}
func TestAHUMAN02InputsAndSelection(t *testing.T) {
	t.Run("strict-input", func(t *testing.T) {
		s, root := fixture(t)
		good := `{"text":"  Exact\r\nwords\n","idempotency_key":"exact-key"}`
		in, err := ParseInput(strings.NewReader(good))
		if err != nil || in.Text != "  Exact\r\nwords\n" {
			t.Fatalf("%+v %v", in, err)
		}
		for _, bad := range []string{`{}`, `{"text":""}`, `{"text":" "}`, `{"text":null}`, `{"text":3}`, `{"text":"x","unknown":1}`, `{"text":"x","text":"y"}`, `{"text":"x","idempotency_key":""}`, `{"text":"x","idempotency_key":null}`, `[]`, `{"text":"x"}{}`, `{"text":"\u0000"}`, string([]byte{0xff}), `{"text":"` + strings.Repeat("x", Limit) + `"}`} {
			if _, err := ParseInput(strings.NewReader(bad)); err == nil {
				t.Fatalf("Accepted invalid input, bytes %d", len(bad))
			}
		}
		write(t, s, "answer.txt", strings.Repeat("x", Limit))
		file, err := FileInput(filepath.Join(s.Path, "answer.txt"))
		if err != nil || len(file.Text) != Limit {
			t.Fatal("Exact file byte limit", err)
		}
		write(t, s, "answer.txt", strings.Repeat("x", Limit+1))
		if _, err := FileInput(filepath.Join(s.Path, "answer.txt")); err == nil {
			t.Fatal("Oversized file accepted")
		}
		write(t, s, "answer.txt", " Exact file\r\n")
		file, err = FileInput(filepath.Join(s.Path, "answer.txt"))
		if err != nil || file.Text != " Exact file\r\n" {
			t.Fatal("File text changed")
		}
		os.Symlink(filepath.Join(s.Path, "answer.txt"), filepath.Join(s.Path, "linked-answer"))
		if _, err := FileInput(filepath.Join(s.Path, "linked-answer")); err == nil {
			t.Fatal("Symlink accepted")
		}
		text, complete, err := TerminalInput(strings.NewReader("  Exact\r\nwords\n\n"))
		if err != nil || !complete || text != "  Exact\r\nwords\n" {
			t.Fatal("Terminal framing changed text")
		}
		_, complete, err = TerminalInput(strings.NewReader("Not finished"))
		if err != nil || complete {
			t.Fatal("EOF submitted candidate")
		}
		_, complete, err = TerminalInput(strings.NewReader(""))
		if err != nil || complete {
			t.Fatal("Empty EOF submitted")
		}
		evidence(t, s, root, map[string]any{"stdin": in, "file": file, "terminal": text})
	})
	t.Run("keys-and-current-question", func(t *testing.T) {
		s, root := fixture(t)
		g := openQuestion(t, s)
		in := Input{Text: " Exact answer\n", IdempotencyKey: "stable"}
		stale, _ := store.NewID()
		_, err := Submit(context.Background(), s, g.RunID, stale, "answer", "command", in, nil, false, "")
		requireCode(t, err, "ANSWER_CONFLICT")
		first, err := Submit(context.Background(), s, g.RunID, g.ID, "answer", "command", in, nil, false, "")
		if err != nil || !first.Selected {
			t.Fatal(first, err)
		}
		second, err := Submit(context.Background(), s, g.RunID, g.ID, "answer", "command", in, nil, false, "")
		if err != nil || !second.Duplicate || second.Entry.ReceiptHash != first.Entry.ReceiptHash {
			t.Fatal("Identical retry differed", err)
		}
		changed := in
		changed.Text = "Different\n"
		_, err = Submit(context.Background(), s, g.RunID, g.ID, "answer", "command", changed, nil, false, "")
		requireCode(t, err, "IDEMPOTENCY_CONFLICT")
		loser, err := Submit(context.Background(), s, g.RunID, g.ID, "answer", "terminal", Input{Text: "Typed losing candidate.\n", IdempotencyKey: "typed"}, nil, false, "")
		if err != nil || loser.Selected {
			t.Fatal("Losing answer selected", err)
		}
		a, err := CommitApplication(context.Background(), s, first.Entry)
		if err != nil {
			t.Fatal(err)
		}
		again, err := CommitApplication(context.Background(), s, first.Entry)
		if err != nil || !reflect.DeepEqual(a, again) {
			t.Fatal("Application identity changed", err)
		}
		_, err = CommitApplication(context.Background(), s, loser.Entry)
		requireCode(t, err, "ANSWER_CONFLICT")
		answer, err := s.ReadText(a.AnswerPath)
		if err != nil || string(answer) != in.Text {
			t.Fatal("Answer archive differs")
		}
		evidence(t, s, root, map[string]any{"first": first, "duplicate": second, "loser": loser, "application": a})
	})
	t.Run("concurrent-inventory", func(t *testing.T) {
		s, root := fixture(t)
		g := openQuestion(t, s)
		before, err := s.Snapshot(context.Background(), "planner")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make(chan Submission, 3)
		errs := make(chan error, 3)
		for _, channel := range []string{"command", "legacy-file", "terminal"} {
			wg.Add(1)
			go func(channel string) {
				defer wg.Done()
				writer, err := store.Open(s.Path)
				if err != nil {
					errs <- err
					return
				}
				defer writer.Close()
				r, err := Submit(context.Background(), writer, g.RunID, g.ID, "answer", channel, Input{Text: channel + " exact\n", IdempotencyKey: channel}, nil, false, "")
				if err != nil {
					errs <- err
				} else {
					results <- r
				}
			}(channel)
		}
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		selected := 0
		var entries []Submission
		for r := range results {
			entries = append(entries, r)
			if r.Selected {
				selected++
			}
		}
		if selected != 1 {
			t.Fatal("First answer selection", selected)
		}
		mutation, err := s.Compare(context.Background(), before, nil)
		if err != nil || mutation != nil {
			t.Fatal("Committed inbox rejected by active inventory", mutation, err)
		}
		evidence(t, s, root, entries)
	})
	t.Run("steering-and-withdrawal", func(t *testing.T) {
		s, root := fixture(t)
		g := openQuestion(t, s)
		steer, err := Submit(context.Background(), s, g.RunID, "", "steer", "command", Input{Text: "Exact steering.\n", IdempotencyKey: "steer"}, nil, false, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = CommitApplication(context.Background(), s, steer.Entry)
		if err != nil {
			t.Fatal(err)
		}
		m, _, _ := s.LoadSnapshot()
		if m.String("status") != "awaiting_answer" {
			t.Fatal("Steering answered a question")
		}
		_, err = Withdraw(context.Background(), s, *g, false)
		requireCode(t, err, "ACK_REQUIRED")
		skip, err := Withdraw(context.Background(), s, *g, true)
		if err != nil || !skip.Receipt.Withdrawn || skip.Receipt.Text != "" {
			t.Fatal(skip, err)
		}
		a, err := CommitApplication(context.Background(), s, skip.Entry)
		if err != nil || !a.Withdrawn {
			t.Fatal("Withdrawal became answer")
		}
		q, err := s.ReadText(a.QuestionPath)
		if err != nil || string(q) != g.QuestionText {
			t.Fatal("Original question lost")
		}
		evidence(t, s, root, map[string]any{"steer": steer, "withdrawal": a})
	})
}

// The process dies at the storage seam, not at a simulated return value.
func TestHumanCrashChild(t *testing.T) {
	start := -1
	for i, arg := range os.Args {
		if arg == "--human-crash" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return
	}
	if len(os.Args) < start+3 {
		t.Fatal("Crash arguments")
	}
	path, mode, site := os.Args[start], os.Args[start+1], os.Args[start+2]
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AcquireOwner(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	g, err := LoadGeneration(s, fmt.Sprint(m.Object("question")["id"]))
	if err != nil {
		t.Fatal(err)
	}
	s.Fault = func(name string) error {
		if name == site {
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
			panic("SIGKILL returned")
		}
		return nil
	}
	switch mode {
	case "submit":
		_, err = Submit(context.Background(), s, g.RunID, g.ID, "answer", "legacy-file", Input{Text: "Exact answer\n", IdempotencyKey: "crash-answer"}, nil, false, g.PriorDirective)
	case "application", "removal":
		inbox, e := s.ReadInbox(context.Background(), g.RunID, time.Second)
		if e != nil || len(inbox.Entries) == 0 {
			t.Fatal(e)
		}
		a, e := CommitApplication(context.Background(), s, inbox.Entries[0])
		if e != nil {
			err = e
		} else if mode == "removal" {
			_, err = RemoveConsumedSource(s, a)
		}
	default:
		t.Fatal("Unknown crash operation")
	}
	t.Fatalf("Crash seam was not reached: %v", err)
}
func sitesFor(t *testing.T, mode string) []string {
	t.Helper()
	s, _ := fixture(t)
	write(t, s, "HUMAN.md", "Prior directive\n")
	g := openQuestion(t, s)
	write(t, s, "HUMAN.md", "Exact answer\n")
	_, source, err := s.ReadObserved("HUMAN.md", Limit)
	if err != nil {
		t.Fatal(err)
	}
	var entry store.InboxEntry
	if mode != "submit" {
		r, err := Submit(context.Background(), s, g.RunID, g.ID, "answer", "legacy-file", Input{Text: "Exact answer\n", IdempotencyKey: "crash-answer"}, &source, false, g.PriorDirective)
		if err != nil {
			t.Fatal(err)
		}
		entry = r.Entry
	}
	var sites []string
	s.Fault = func(site string) error { sites = append(sites, site); return nil }
	if mode == "submit" {
		_, err = Submit(context.Background(), s, g.RunID, g.ID, "answer", "legacy-file", Input{Text: "Exact answer\n", IdempotencyKey: "crash-answer"}, nil, false, g.PriorDirective)
	} else {
		a, e := CommitApplication(context.Background(), s, entry)
		err = e
		if mode == "removal" && err == nil {
			sites = nil
			_, err = RemoveConsumedSource(s, a)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	var selected []string
	for _, site := range sites {
		if mode == "submit" && (strings.HasPrefix(site, "input-receipt.") || strings.HasPrefix(site, "inbox.")) || mode == "application" && (strings.HasPrefix(site, "human.") || strings.HasPrefix(site, "manifest.") || strings.HasPrefix(site, "transaction.")) || mode == "removal" && strings.HasPrefix(site, "input-source.") {
			selected = append(selected, site)
		}
	}
	if len(selected) == 0 {
		t.Fatal("No seam recorded")
	}
	return selected
}
func TestAHUMAN03ActualCrashes(t *testing.T) {
	for _, mode := range []string{"submit", "application", "removal"} {
		for _, site := range sitesFor(t, mode) {
			t.Run(mode+"/"+site, func(t *testing.T) {
				s, root := fixture(t)
				write(t, s, "HUMAN.md", "Prior directive\n")
				g := openQuestion(t, s)
				write(t, s, "HUMAN.md", "Exact answer\n")
				_, source, _ := s.ReadObserved("HUMAN.md", Limit)
				if mode != "submit" {
					if _, err := Submit(context.Background(), s, g.RunID, g.ID, "answer", "legacy-file", Input{Text: "Exact answer\n", IdempotencyKey: "crash-answer"}, &source, false, g.PriorDirective); err != nil {
						t.Fatal(err)
					}
				}
				path := s.Path
				s.Close()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(exe, "-test.run=^TestHumanCrashChild$", "--", "--human-crash", path, mode, site)
				cmd.Env = []string{"HOME=" + path, "TMPDIR=" + path, "PATH=" + filepath.Join(path, "empty-tools")}
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatal("Child survived")
				}
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatalf("Wrong crash %v %s", err, output)
				}
				recovered, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer recovered.Close()
				if err = recovered.AcquireOwner(context.Background(), time.Second); err != nil {
					t.Fatal(err)
				}
				if _, err = recovered.Recover(); err != nil {
					t.Fatal(err)
				}
				inbox, err := recovered.ReadInbox(context.Background(), g.RunID, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "submit" {
					if len(inbox.Entries) > 1 {
						t.Fatal("Duplicate input")
					}
					r, err := Submit(context.Background(), recovered, g.RunID, g.ID, "answer", "legacy-file", Input{Text: "Exact answer\n", IdempotencyKey: "crash-answer"}, nil, false, g.PriorDirective)
					if err != nil {
						t.Fatal(err)
					}
					inbox, err = recovered.ReadInbox(context.Background(), g.RunID, time.Second)
					if err != nil || inbox.Entries[0].ReceiptHash != r.Entry.ReceiptHash {
						t.Fatal(err)
					}
				}
				if len(inbox.Entries) != 1 {
					t.Fatal("One committed answer required")
				}
				a, err := CommitApplication(context.Background(), recovered, inbox.Entries[0])
				if err != nil {
					t.Fatal(err)
				}
				again, err := CommitApplication(context.Background(), recovered, inbox.Entries[0])
				if err != nil || a.TurnID != again.TurnID {
					t.Fatal("New logical application", err)
				}
				if mode == "removal" {
					if _, err := RemoveConsumedSource(recovered, a); err != nil {
						t.Fatal(err)
					}
				}
				answer, err := recovered.ReadText(a.AnswerPath)
				if err != nil || string(answer) != "Exact answer\n" {
					t.Fatal("Answer lost", err)
				}
				evidence(t, recovered, root, map[string]any{"signal": "SIGKILL", "site": site, "mode": mode, "application": a})
			})
		}
	}
}
func TestAHUMAN03ChangedSource(t *testing.T) {
	for _, variant := range []string{"before-removal", "during-rename", "append-after-removal"} {
		t.Run(variant, func(t *testing.T) {
			s, root := fixture(t)
			g := openQuestion(t, s)
			write(t, s, "HUMAN.md", "Exact answer\n")
			_, source, _ := s.ReadObserved("HUMAN.md", Limit)
			r, err := Submit(context.Background(), s, g.RunID, g.ID, "answer", "legacy-file", Input{Text: "Exact answer\n", IdempotencyKey: "file"}, &source, false, "")
			if err != nil {
				t.Fatal(err)
			}
			a, err := CommitApplication(context.Background(), s, r.Entry)
			if err != nil {
				t.Fatal(err)
			}
			if variant == "before-removal" {
				write(t, s, "HUMAN.md", "Later pending input\n")
			}
			if variant == "during-rename" {
				s.Fault = func(site string) error {
					if site == "input-source.removal-rename.before" {
						write(t, s, "HUMAN.md", "Later pending input\n")
					}
					return nil
				}
			}
			var writer *os.File
			if variant == "append-after-removal" {
				writer, err = os.OpenFile(filepath.Join(s.Path, "HUMAN.md"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
			}
			removed, err := RemoveConsumedSource(s, a)
			if variant == "during-rename" {
				requireCode(t, err, "ANSWER_CONFLICT")
			} else if err != nil {
				t.Fatal(err)
			}
			if variant != "append-after-removal" {
				if removed {
					t.Fatal("Changed source removed")
				}
				text, err := s.ReadText("HUMAN.md")
				if err != nil || string(text) != "Later pending input\n" {
					t.Fatal("Later source lost")
				}
			} else {
				if !removed {
					t.Fatal("Unchanged source remains")
				}
				writer.WriteString("Late append retained\n")
				writer.Sync()
				matches, _ := filepath.Glob(filepath.Join(s.Path, "state/human/removed-source-*.md"))
				if len(matches) != 1 {
					t.Fatal(matches)
				}
				b, err := os.ReadFile(matches[0])
				if err != nil || string(b) != "Exact answer\nLate append retained\n" {
					t.Fatal("Open writer lost", err)
				}
			}
			evidence(t, s, root, map[string]any{"removed": removed, "variant": variant, "application": a})
		})
	}
}

type fakePane struct {
	observe               PaneObservation
	complete              PaneCompletion
	observeErr, errorWait error
	calls                 []string
	action                func(string)
}

func (p *fakePane) Observe(context.Context) (PaneObservation, error) {
	p.calls = append(p.calls, "observe")
	if p.action != nil {
		p.action("observe")
	}
	return p.observe, p.observeErr
}
func (p *fakePane) WaitSince(_ context.Context, since string) (PaneCompletion, error) {
	p.calls = append(p.calls, "wait:"+since)
	if p.action != nil {
		p.action("wait")
	}
	return p.complete, p.errorWait
}
func TestAHUMAN06Settlement(t *testing.T) {
	for _, variant := range []string{"working-idle", "already-idle", "missing-completion", "wrong-cursor", "old-event", "clarification", "clarification-no-file", "spec-edit", "question-edit", "unknown-pane", "changed-answer"} {
		t.Run(variant, func(t *testing.T) {
			s, root := fixture(t)
			write(t, s, "HUMAN.md", "Prior\n")
			if variant == "clarification-no-file" {
				os.Remove(filepath.Join(s.Path, "HUMAN.md"))
			}
			g := openQuestion(t, s)
			before, err := s.Inspect(context.Background(), store.Protection{Actor: "planner", AnswerTurn: true})
			if err != nil {
				t.Fatal(err)
			}
			spec, _ := s.ReadText("SPEC.md")
			if variant != "clarification" && variant != "clarification-no-file" {
				write(t, s, "HUMAN.md", "Prior\nExact answer\n")
			}
			pane := &fakePane{observe: PaneObservation{"owned-pane", "working", "10", "hook", true, 10}, complete: PaneCompletion{"owned-pane", "idle", "11", "10", "turn_ended", true, 11}}
			switch variant {
			case "already-idle":
				pane.observe.State = "idle"
			case "missing-completion":
				pane.complete.Checked = false
			case "old-event":
				pane.complete.Sequence = 9
			case "wrong-cursor":
				pane.complete.Since = "9"
			case "unknown-pane":
				pane.observe.PaneID = "foreign-pane"
			case "spec-edit":
				pane.action = func(string) { write(t, s, "SPEC.md", "Changed spec\n") }
			case "question-edit":
				pane.action = func(string) { write(t, s, "QUESTIONS.md", "Changed questions\n") }
			case "changed-answer":
				pane.action = func(string) { write(t, s, "HUMAN.md", "Prior\nChanged answer\n") }
			}
			var paths []string
			o := SettlementOptions{Generation: *g, PaneID: "owned-pane", ExpectedSpecHash: contract.HashBytes(spec), Before: before, Clock: &fakeClock{}, Register: func(path string) error { paths = append(paths, path); return nil }, Authorized: func() []store.AuthorizedChange {
				var changes []store.AuthorizedChange
				for _, path := range paths {
					obs, err := s.Observe(path)
					if err != nil {
						t.Fatal(err)
					}
					obs.Path = filepath.Join(s.Path, path)
					changes = append(changes, store.AuthorizedChange{Path: path, Before: before.Files[path], After: obs, Kind: "owned candidate"})
				}
				return changes
			}}
			result, err := SettlePaneAnswer(context.Background(), s, pane, o)
			switch variant {
			case "working-idle", "already-idle":
				if err != nil || !result.Settled || result.Text != "Prior\nExact answer\n" {
					t.Fatal(result, err)
				}
			case "clarification", "clarification-no-file":
				if err != nil || result.Settled || len(pane.calls) != 0 {
					t.Fatal("Clarification released gate", err)
				}
			case "spec-edit", "question-edit":
				requireCode(t, err, "PLANNER_MUTATION")
			case "changed-answer":
				requireCode(t, err, "ANSWER_CONFLICT")
			default:
				requireCode(t, err, "TURN_UNCERTAIN")
			}
			if variant == "working-idle" && fmt.Sprint(pane.calls) != "[observe wait:10]" {
				t.Fatal("Unqualified wait", pane.calls)
			}
			if variant == "already-idle" && len(pane.calls) != 1 {
				t.Fatal("Idle observed again")
			}
			inbox, err := s.ReadInbox(context.Background(), g.RunID, time.Second)
			if err != nil || len(inbox.Entries) != 0 {
				t.Fatal("Settlement started a loop or selected an uncommitted candidate")
			}
			evidence(t, s, root, map[string]any{"result": result, "calls": pane.calls, "error_code": errorCode(err)})
		})
	}
}

func TestHumanTerminalRetentionAndDeliveries(t *testing.T) {
	s, root := fixture(t)
	g := openQuestion(t, s)
	first, err := Submit(context.Background(), s, g.RunID, g.ID, "answer", "command", Input{Text: "Winning answer\n", IdempotencyKey: "winner"}, nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	losing, path, err := TerminalAnswer(context.Background(), s, *g, strings.NewReader("Typed losing words\n\n"))
	if err != nil || losing.Selected || path == "" {
		t.Fatal("Typed losing candidate lost", err)
	}
	a, err := CommitApplication(context.Background(), s, first.Entry)
	if err != nil {
		t.Fatal(err)
	}
	stale := *g
	stale.ID = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, stalePath, err := TerminalAnswer(context.Background(), s, stale, strings.NewReader("Late exact words\n\n"))
	requireCode(t, err, "ANSWER_CONFLICT")
	if _, err = s.ReadText(stalePath); err != nil {
		t.Fatal("Stale typed candidate lost")
	}
	incomplete, pendingPath, err := TerminalAnswer(context.Background(), s, *g, strings.NewReader("Incomplete words"))
	if err != nil || incomplete.Selected || pendingPath == "" {
		t.Fatal("EOF candidate selected or lost")
	}
	for _, role := range []string{"planner", "critic"} {
		m, _, err := s.LoadSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		turn := a.TurnID
		if role == "critic" {
			turn, _ = store.NewID()
		}
		prompt := []byte("Exact archived input reference")
		promptPath := "state/prompts/" + turn + ".md"
		if _, err = s.StagePrivateText(promptPath, prompt); err != nil {
			t.Fatal(err)
		}
		intent := map[string]any{"id": turn, "purpose": "apply_directive", "role": role, "round": 2, "attempt": 1, "intent_hash": "owned intent", "prompt_path": promptPath, "prompt_hash": contract.HashBytes(prompt), "operation": "turn", "delivery_uncertain": false, "receipt_path": "", "cursor": "", "started_at": "", "budget": "", "remaining": ""}
		m["current_turn"] = intent
		m["status"] = "running"
		m["phase"] = "apply_directive"
		tx, err := s.NewTransaction("intent", m, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		record := sampleSchema(t, "receipt")
		record["record_version"] = 1
		record["run_id"] = g.RunID
		record["turn_id"] = turn
		for _, key := range []string{"purpose", "role", "round", "prompt_hash"} {
			record[key] = intent[key]
		}
		record["spec_before_hash"] = m["spec_hash"]
		record["input_receipt_ids"] = []string{a.ReceiptHash}
		record.Object("completion")["kind"] = "completed"
		record.Object("completion")["settled"] = true
		ref, err := s.SaveTurnReceipt(turn, record)
		if err != nil {
			t.Fatal(err)
		}
		if err = RecordDelivery(s, a, role, ref); errorCode(err) != "TURN_UNCERTAIN" {
			t.Fatal("Unsettled result accepted", err)
		}
		m, _, err = s.LoadSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		m["current_turn"] = nil
		m["status"] = "ready"
		tx, err = s.NewTransaction("result", m, nil, &ref)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		if err = RecordDelivery(s, a, role, ref); err != nil {
			t.Fatal(err)
		}
		if err = RecordDelivery(s, a, role, ref); err != nil {
			t.Fatal("Idempotent delivery differs", err)
		}
		wrong := ref
		wrong.Hash = "changed"
		requireCode(t, RecordDelivery(s, a, role, wrong), "STATE_INVALID")
	}
	checked, err := LoadApplication(s, a.ReceiptHash)
	if err != nil || checked.TurnID != a.TurnID {
		t.Fatal("Delivered application changed", err)
	}
	evidence(t, s, root, map[string]any{"application": a, "loser": losing, "candidate": path, "pending_candidate": pendingPath})
}

func TestHumanUnsafeFileAndWithdrawal(t *testing.T) {
	for _, variant := range []string{"invalid-utf8", "nul", "oversized", "fifo", "deleted-question", "empty-question"} {
		t.Run(variant, func(t *testing.T) {
			s, root := fixture(t)
			g := openQuestion(t, s)
			switch variant {
			case "invalid-utf8":
				os.WriteFile(filepath.Join(s.Path, "HUMAN.md"), []byte{0xff}, 0600)
			case "nul":
				write(t, s, "HUMAN.md", "Answer\x00")
			case "oversized":
				write(t, s, "HUMAN.md", strings.Repeat("x", Limit+1))
			case "fifo":
				if err := syscall.Mkfifo(filepath.Join(s.Path, "HUMAN.md"), 0600); err != nil {
					t.Fatal(err)
				}
			case "deleted-question":
				os.Remove(filepath.Join(s.Path, "QUESTIONS.md"))
			case "empty-question":
				write(t, s, "QUESTIONS.md", " \n")
			}
			if strings.HasSuffix(variant, "question") {
				r, withdrawn, err := FileWithdrawal(context.Background(), s, *g, &fakeClock{})
				if err != nil || !withdrawn || !r.Receipt.Withdrawn || r.Receipt.Text != "" {
					t.Fatal(r, withdrawn, err)
				}
				evidence(t, s, root, r)
			} else {
				_, submitted, err := FileAnswer(context.Background(), s, *g, &fakeClock{})
				requireCode(t, err, "ANSWER_CONFLICT")
				if submitted {
					t.Fatal("Unsafe input selected")
				}
				evidence(t, s, root, "refused unsafe file")
			}
		})
	}
}
