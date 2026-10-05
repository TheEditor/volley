//go:build darwin || linux

package engine

import (
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

func contextObservation(path string) (store.FileObservation, error) {
	s, err := store.Open(path)
	if err != nil {
		return store.FileObservation{}, err
	}
	defer s.Close()
	return s.RootObservation()
}
func checkContextBinding(path string, saved *store.FileObservation) error {
	if path == "" {
		if saved != nil {
			return failure("STATE_INVALID", "Context binding has no selected path", nil)
		}
		return nil
	}
	if saved == nil {
		return failure("STATE_INVALID", "Selected context has no saved directory identity", nil)
	}
	now, err := contextObservation(path)
	if err != nil || now != *saved {
		before, _ := contract.Canonical(saved)
		after, _ := contract.Canonical(now)
		return failure("CONTEXT_CHANGED", "Canonical context directory identity changed", map[string]any{"path": path, "expected": contract.HashBytes(before), "observed": contract.HashBytes(after)})
	}
	return nil
}
