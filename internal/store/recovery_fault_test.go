//go:build darwin || linux

package store

import (
	"errors"
	"testing"
)

func recoveryOperation(t *testing.T, s *Store, seed string) func() error {
	t.Helper()
	var operation func() error
	if seed == "receipt" {
		tx := nextIntent(t, s)
		if err := s.CommitTransaction(tx); err != nil {
			t.Fatal(err)
		}
		id, r := receiptFixture(t, s)
		operation = func() error { _, err := s.SaveTurnReceipt(id, r); return err }
	} else if seed == "intent" {
		tx := nextIntent(t, s)
		operation = func() error { return s.CommitTransaction(tx) }
	} else {
		operation = resultOperation(t, s, false)
	}
	site := "transaction.publish.after"
	switch seed {
	case "receipt":
		site = "receipt.publish.after"
	case "replacement":
		site = "artifact-1.replacement-file-fsync.after"
	case "promoted":
		site = "artifact-1.rename.after"
	}
	fired := false
	s.Fault = func(name string) error {
		if name == site && !fired {
			fired = true
			return errors.New("preparing recovery stimulus")
		}
		return nil
	}
	if err := operation(); err == nil || !fired {
		t.Fatal("seed fault not reached")
	}
	s.Fault = nil
	return func() error { _, err := s.Recover(); return err }
}
func TestASTATE01And02RecoveryBoundaries(t *testing.T) {
	for _, seed := range []string{"intent", "receipt", "replacement", "promoted"} {
		prep := func(t *testing.T, s *Store) func() error { return recoveryOperation(t, s, seed) }
		for _, site := range faultSites(t, prep) {
			t.Run(seed+"/"+site, func(t *testing.T) {
				s := fixture(t)
				operation := prep(t, s)
				fired := false
				s.Fault = func(name string) error {
					if name == site && !fired {
						fired = true
						return errors.New("recovery interrupted")
					}
					return nil
				}
				if err := operation(); err == nil || !fired {
					t.Fatal("recovery fault not reached")
				}
				s.Fault = nil
				if _, err := s.Recover(); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Recover(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
