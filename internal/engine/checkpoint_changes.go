//go:build darwin || linux

package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/TheEditor/volley/internal/store"
)

// A checked chain validates the mutable head. Keep immutable checkpoint
// records from older proofs, then add the chain's current head once.
func immutableCheckpointChanges(changes []store.AuthorizedChange) []store.AuthorizedChange {
	result := make([]store.AuthorizedChange, 0, len(changes))
	for _, change := range changes {
		if change.Kind == "checked handover checkpoint" && (change.Path == "state/manifest.json" || change.Path == "state/events.jsonl") {
			continue
		}
		result = append(result, change)
	}
	return result
}

// A handover can change controller records after the checked turn. Admit only
// exact records from a checked chain that preserves the turn and engine state.
func checkpointChanges(s *store.Store, before store.Inventory) ([]store.AuthorizedChange, error) {
	history, err := s.History()
	if err != nil {
		return nil, err
	}
	baseHash := before.Files["state/manifest.json"].Hash
	var base store.Snapshot
	var changes []store.AuthorizedChange
	add := func(path, hash string) error {
		obs, err := s.Observe(path)
		if err != nil {
			return err
		}
		if hash != "" && obs.Hash != hash {
			return failure("STATE_INVALID", "Controller record hash differs", nil)
		}
		obs.Path = filepath.Join(s.Path, path)
		changes = append(changes, store.AuthorizedChange{Path: path, Before: before.Files[path], After: obs, Kind: "checked handover checkpoint"})
		return nil
	}
	normalize := func(m store.Snapshot) ([]byte, error) {
		b, _ := json.Marshal(m)
		v, err := input.Record(bytes.NewReader(b), store.RecordLimit)
		if err != nil {
			return nil, err
		}
		copy := store.Snapshot(v.(map[string]any))
		for _, key := range []string{"revision", "last_transaction_id", "status", "errors", "no_start"} {
			delete(copy, key)
		}
		if turn := copy.Object("current_turn"); len(turn) > 0 {
			delete(turn, "delivery_uncertain")
			gk, _ := copy.Object("config_records")["gashki"].(map[string]any)
			if gk["type"] == "file" {
				delete(copy, "recovery")
				for _, key := range []string{"recovery_guard", "cursor", "operation", "remaining", "pane_uuid"} {
					delete(turn, key)
				}
			}
		}
		if engine := copy.Object("engine"); len(engine) > 0 {
			delete(engine, "path")
		}
		return contract.Canonical(copy)
	}
	for _, tx := range history {
		if contract.HashBytes(tx.Next) == baseHash {
			json.Unmarshal(tx.Next, &base)
			continue
		}
		if base == nil {
			continue
		}
		var next store.Snapshot
		if err = json.Unmarshal(tx.Next, &next); err != nil {
			return nil, err
		}
		oldData, e := normalize(base)
		if e != nil {
			return nil, e
		}
		nextData, e := normalize(next)
		if e != nil {
			return nil, e
		}
		gk, _ := next.Object("config_records")["gashki"].(map[string]any)
		operation := next.Object("current_turn")["operation"]
		delivery := gk["type"] == "file" && next.String("status") == "running" && (operation == "delivery_confirmed" || operation == "send_prepared")
		if tx.Kind != "control" || len(tx.Artifacts) > 0 || tx.Receipt != nil || next.String("status") != "handover" && !delivery || !bytes.Equal(oldData, nextData) {
			return nil, failure("TURN_UNCERTAIN", "Checkpoint changed after the checked turn", nil)
		}
		if gk["type"] == "file" && next.Object("current_turn")["recovery_guard"] != nil {
			ref, _ := next.Object("current_turn")["recovery_guard"].(map[string]any)
			path, _ := ref["path"].(string)
			if !recoveryGuardPath(fmt.Sprint(next.Object("current_turn")["id"]), path) {
				return nil, failure("STATE_INVALID", "Delivery guard binding differs", nil)
			}
			if err = add(path, fmt.Sprint(ref["sha256"])); err != nil {
				return nil, err
			}
		}
		encoded, e := contract.Canonical(tx)
		if e != nil {
			return nil, e
		}
		if err = add(fmt.Sprintf("state/transactions/%08d-%s.json", tx.PreviousRevision+1, tx.ID), contract.HashBytes(encoded)); err != nil {
			return nil, err
		}
		engine := next.Object("engine")
		if err = add(fmt.Sprint(engine["path"]), fmt.Sprint(engine["sha256"])); err != nil {
			return nil, err
		}
	}
	if base == nil {
		return nil, failure("STATE_INVALID", "Checked turn checkpoint is absent from history", nil)
	}
	currentHash := contract.HashBytes(history[len(history)-1].Next)
	if currentHash != baseHash {
		if err = add("state/manifest.json", currentHash); err != nil {
			return nil, err
		}
		if _, err = s.Events(); err != nil {
			return nil, err
		}
		if err = add("state/events.jsonl", ""); err != nil {
			return nil, err
		}
	}
	return changes, nil
}
