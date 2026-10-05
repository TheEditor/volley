//go:build darwin || linux

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func inputFixture(t *testing.T, s *Store, key string) InputReceipt {
	t.Helper()
	m, _, err := s.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	generation, _ := NewID()
	return InputReceipt{RecordVersion: 1, RunID: m.String("run_id"), Kind: "answer", Key: key, GenerationID: &generation, Text: "Exact answer\n", CreatedAt: "2026-10-04T00:00:00Z"}
}
func TestASTATE04Inbox(t *testing.T) {
	prep := func(t *testing.T, s *Store) func() error {
		r := inputFixture(t, s, "stable-key")
		return func() error { _, err := s.SubmitInput(context.Background(), r, time.Second); return err }
	}
	for _, site := range faultSites(t, prep) {
		t.Run(site, func(t *testing.T) {
			s := fixture(t)
			r := inputFixture(t, s, "stable-key")
			fired := false
			s.Fault = func(name string) error {
				if name == site && !fired {
					fired = true
					return errors.New("inbox interrupted")
				}
				return nil
			}
			if _, err := s.SubmitInput(context.Background(), r, time.Second); err == nil || !fired {
				t.Fatal("no interruption")
			}
			s.Fault = nil
			inbox, err := s.ReadInbox(context.Background(), r.RunID, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if len(inbox.Entries) > 1 {
				t.Fatal("duplicate commit")
			}
			first, err := s.SubmitInput(context.Background(), r, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			r.CreatedAt = "retry time ignored"
			second, err := s.SubmitInput(context.Background(), r, time.Second)
			if err != nil || first.ReceiptHash != second.ReceiptHash || first.Sequence != second.Sequence {
				t.Fatalf("retry differed %+v %+v %v", first, second, err)
			}
			inbox, err = s.ReadInbox(context.Background(), r.RunID, time.Second)
			if err != nil || len(inbox.Entries) != 1 {
				t.Fatalf("%+v %v", inbox, err)
			}
			r.Text = "different"
			_, err = s.SubmitInput(context.Background(), r, time.Second)
			requireCode(t, err, "IDEMPOTENCY_CONFLICT")
		})
	}
	t.Run("concurrent-while-owner-waits", func(t *testing.T) {
		s := fixture(t)
		r := inputFixture(t, s, "base")
		var group sync.WaitGroup
		results := make(chan error, 12)
		for i := 0; i < 12; i++ {
			group.Add(1)
			go func(i int) {
				defer group.Done()
				writer, err := Open(s.Path)
				if err != nil {
					results <- err
					return
				}
				defer writer.Close()
				candidate := r
				candidate.Key = fmt.Sprintf("key-%d", i)
				_, err = writer.SubmitInput(context.Background(), candidate, 5*time.Second)
				results <- err
			}(i)
		}
		group.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		inbox, err := s.ReadInbox(context.Background(), r.RunID, time.Second)
		if err != nil || len(inbox.Entries) != 12 {
			t.Fatalf("%+v %v", inbox, err)
		}
		if err := s.checkOwner(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("partial-suffix", func(t *testing.T) {
		s := fixture(t)
		r := inputFixture(t, s, "one")
		if _, err := s.SubmitInput(context.Background(), r, time.Second); err != nil {
			t.Fatal(err)
		}
		f, _ := os.OpenFile(filepath.Join(s.Path, "state/inputs/inbox.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
		_, _ = f.WriteString(`{"incomplete":`)
		_ = f.Close()
		inbox, err := s.ReadInbox(context.Background(), r.RunID, time.Second)
		if err != nil || len(inbox.Entries) != 1 {
			t.Fatal(err)
		}
		r.Key = "two"
		if _, err := s.SubmitInput(context.Background(), r, time.Second); err != nil {
			t.Fatal(err)
		}
		inbox, _ = s.ReadInbox(context.Background(), r.RunID, time.Second)
		if len(inbox.Entries) != 2 {
			t.Fatal("suffix recovery failed")
		}
	})
	t.Run("committed-corruption", func(t *testing.T) {
		s := fixture(t)
		r := inputFixture(t, s, "one")
		if _, err := s.SubmitInput(context.Background(), r, time.Second); err != nil {
			t.Fatal(err)
		}
		put(t, s, "state/inputs/inbox.jsonl", "bad entry\n")
		_, err := s.ReadInbox(context.Background(), r.RunID, time.Second)
		requireCode(t, err, "STATE_INVALID")
		_, err = s.SubmitInput(context.Background(), r, time.Second)
		requireCode(t, err, "STATE_INVALID")
	})
	t.Run("prior-receipt-changed", func(t *testing.T) {
		s := fixture(t)
		r := inputFixture(t, s, "one")
		entry, err := s.SubmitInput(context.Background(), r, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		put(t, s, entry.ReceiptPath, `{}`)
		_, err = s.ReadInbox(context.Background(), r.RunID, time.Second)
		requireCode(t, err, "STATE_INVALID")
	})
}
