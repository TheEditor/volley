//go:build darwin || linux

package human

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/store"
	"golang.org/x/sys/unix"
)

const Limit = 1 << 20
const PollInterval = 250 * time.Millisecond

func failure(code, message string) error {
	r, err := contract.Load()
	if err != nil {
		return err
	}
	return r.Error(code, message)
}
func validText(text string) bool {
	return len(text) <= Limit && utf8.ValidString(text) && !strings.ContainsRune(text, 0) && strings.TrimSpace(text) != ""
}
func ParseInput(reader io.Reader) (Input, error) {
	v, err := input.JSON(reader)
	if err != nil {
		return Input{}, failure("INVALID_INPUT", "Input must be one bounded strict JSON object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Input{}, failure("INVALID_INPUT", "Input must be a JSON object")
	}
	for key := range m {
		if key != "text" && key != "idempotency_key" {
			return Input{}, failure("INVALID_INPUT", "Unknown human input field")
		}
	}
	text, ok := m["text"].(string)
	if !ok || !validText(text) {
		return Input{}, failure("INVALID_INPUT", "Answer text must be nonempty UTF-8 within 1 MiB")
	}
	key := ""
	if v, present := m["idempotency_key"]; present {
		key, ok = v.(string)
		if !ok || key == "" || len(key) > 1024 || strings.ContainsRune(key, 0) {
			return Input{}, failure("INVALID_INPUT", "Input key must be a nonempty bounded string")
		}
	}
	return Input{Text: text, IdempotencyKey: key}, nil
}
func FileInput(path string) (Input, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return Input{}, failure("INVALID_INPUT", "Answer file cannot be read")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return Input{}, failure("INVALID_INPUT", "Answer file must be regular")
	}
	b, err := input.Read(f)
	if err != nil || !validText(string(b)) {
		return Input{}, failure("INVALID_INPUT", "Answer file must be nonempty UTF-8 within 1 MiB")
	}
	after, err := f.Stat()
	current, e := os.Lstat(path)
	if err != nil || e != nil || !os.SameFile(before, current) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return Input{}, failure("ANSWER_CONFLICT", "Answer file changed during read")
	}
	return Input{Text: string(b)}, nil
}

// TerminalInput terminates at an empty line. EOF preserves the candidate for
// the caller but never marks it complete. Exact commands avoid line framing.
func TerminalInput(reader io.Reader) (candidate string, complete bool, err error) {
	r := bufio.NewReader(io.LimitReader(reader, Limit+1))
	var text strings.Builder
	for {
		line, err := r.ReadString('\n')
		if line == "\n" || line == "\r\n" {
			if validText(text.String()) {
				return text.String(), true, nil
			}
			return "", false, nil
		}
		if text.Len()+len(line) > Limit {
			return text.String(), false, failure("INVALID_INPUT", "Terminal answer exceeds 1 MiB")
		}
		if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
			return text.String(), false, failure("INVALID_INPUT", "Terminal answer is not valid text")
		}
		text.WriteString(line)
		if err == io.EOF {
			return text.String(), false, nil
		}
		if err != nil {
			return text.String(), false, failure("INVALID_INPUT", "Terminal answer cannot be read")
		}
	}
}

type Clock interface {
	Wait(context.Context, time.Duration) error
}
type clock struct{}

func (clock) Wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func StableRead(ctx context.Context, s *store.Store, path string, c Clock) ([]byte, store.FileObservation, error) {
	if c == nil {
		c = clock{}
	}
	b, first, err := s.ReadObserved(path, Limit)
	if err != nil {
		return nil, first, failure("ANSWER_CONFLICT", "Input file cannot be read as bounded regular UTF-8")
	}
	if err = c.Wait(ctx, PollInterval); err != nil {
		return nil, first, err
	}
	again, second, err := s.ReadObserved(path, Limit)
	if err != nil {
		return nil, second, failure("ANSWER_CONFLICT", "Input file cannot be read as bounded regular UTF-8")
	}
	if first != second || !bytes.Equal(b, again) || bytes.ContainsRune(b, 0) {
		return nil, second, failure("ANSWER_CONFLICT", "Input file is still changing")
	}
	return b, second, nil
}

type Submission struct {
	Entry     store.InboxEntry
	Selected  bool
	Duplicate bool
	Receipt   store.InputReceipt
}

func Submit(ctx context.Context, s *store.Store, runID, questionID, kind, channel string, in Input, source *store.FileObservation, withdrawn bool, prior string) (Submission, error) {
	if kind != "answer" && kind != "steer" || !withdrawn && !validText(in.Text) || withdrawn && (kind != "answer" || in.Text != "") || len(prior) > Limit || !utf8.ValidString(prior) {
		return Submission{}, failure("INVALID_INPUT", "Invalid human submission")
	}
	if in.IdempotencyKey == "" {
		key, err := store.NewID()
		if err != nil {
			return Submission{}, err
		}
		in.IdempotencyKey = key
	}
	var generation *string
	if kind == "answer" {
		generation = &questionID
	}
	receipt := store.InputReceipt{RunID: runID, Kind: kind, Key: in.IdempotencyKey, GenerationID: generation, Text: in.Text, Channel: channel, Source: source, Withdrawn: withdrawn, PriorDirective: prior, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	entry, err := s.SubmitCurrentInput(ctx, receipt, time.Second)
	if err != nil {
		return Submission{}, err
	}
	inbox, err := s.ReadInbox(ctx, runID, time.Second)
	if err != nil {
		return Submission{}, err
	}
	saved := inbox.Receipts[entry.ReceiptPath]
	result := Submission{Entry: entry, Receipt: saved, Selected: kind == "steer", Duplicate: saved.CreatedAt != receipt.CreatedAt}
	if kind == "answer" {
		for _, candidate := range inbox.Entries {
			if candidate.Kind == kind && candidate.GenerationID != nil && *candidate.GenerationID == questionID {
				result.Selected = candidate.ReceiptPath == entry.ReceiptPath
				break
			}
		}
	}
	return result, nil
}
func CheckCurrentGeneration(s *store.Store, id string) (store.Snapshot, error) {
	m, _, err := s.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	if m.Object("question")["id"] != id {
		return nil, failure("ANSWER_CONFLICT", "Question generation is no longer current")
	}
	return m, nil
}
func FileAnswer(ctx context.Context, s *store.Store, g Generation, c Clock) (Submission, bool, error) {
	if _, err := CheckCurrentGeneration(s, g.ID); err != nil {
		return Submission{}, false, err
	}
	b, observed, err := StableRead(ctx, s, "HUMAN.md", c)
	if err != nil {
		return Submission{}, false, err
	}
	if reflect.DeepEqual(observed, g.HumanBaseline) || !validText(string(b)) {
		return Submission{}, false, nil
	}
	key := contract.HashBytes([]byte(fmt.Sprintf("%s:%d:%d:%s", g.ID, observed.Device, observed.Inode, observed.Hash)))
	r, err := Submit(ctx, s, g.RunID, g.ID, "answer", "legacy-file", Input{Text: string(b), IdempotencyKey: key}, &observed, false, g.PriorDirective)
	return r, err == nil, err
}
func Withdraw(ctx context.Context, s *store.Store, g Generation, yes bool) (Submission, error) {
	if !yes {
		return Submission{}, failure("ACK_REQUIRED", "Question withdrawal requires --yes")
	}
	return Submit(ctx, s, g.RunID, g.ID, "answer", "withdrawal", Input{IdempotencyKey: "withdraw-" + g.ID}, nil, true, g.PriorDirective)
}
func FileWithdrawal(ctx context.Context, s *store.Store, g Generation, c Clock) (Submission, bool, error) {
	b, obs, err := StableRead(ctx, s, "QUESTIONS.md", c)
	if err != nil {
		return Submission{}, false, err
	}
	if len(bytes.TrimSpace(b)) != 0 {
		return Submission{}, false, nil
	}
	r, err := Submit(ctx, s, g.RunID, g.ID, "answer", "file-withdrawal", Input{IdempotencyKey: "withdraw-" + g.ID}, &obs, true, g.PriorDirective)
	return r, err == nil, err
}

// TerminalAnswer retains the candidate before selection. EOF and a stale gate
// cannot discard text or submit an empty answer.
func TerminalAnswer(ctx context.Context, s *store.Store, g Generation, reader io.Reader) (Submission, string, error) {
	text, complete, err := TerminalInput(reader)
	path := ""
	if text != "" && len(text) <= Limit && utf8.ValidString(text) {
		path = "state/human/terminal-candidate-" + g.ID + "-" + contract.HashBytes([]byte(text)) + ".json"
		b, e := contract.Canonical(map[string]any{"record_version": 1, "run_id": g.RunID, "generation_id": g.ID, "text": text, "complete": complete})
		if e != nil {
			return Submission{}, path, e
		}
		if _, e = s.StagePrivateText(path, b); e != nil {
			return Submission{}, path, e
		}
	}
	if err != nil || !complete {
		return Submission{}, path, err
	}
	r, err := Submit(ctx, s, g.RunID, g.ID, "answer", "terminal", Input{Text: text, IdempotencyKey: "terminal-" + g.ID + "-" + contract.HashBytes([]byte(text))}, nil, false, "")
	return r, path, err
}
