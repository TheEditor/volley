//go:build darwin || linux

// Package engine owns review effects. The review package owns pure decisions.
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/review"
	"github.com/TheEditor/volley/internal/store"
)

type TurnGuard struct {
	Store          *store.Store
	Gate           interface{ Check() error }
	AllowedHistory []string
	mu             sync.Mutex
	active         string
	paths          map[string]string
	confirmed      map[string]store.FileObservation
	Proof          *GuardProof
}

type GuardProof struct {
	Before  store.Inventory          `json:"before"`
	Changes []store.AuthorizedChange `json:"changes"`
}

func failure(code, message string, evidence map[string]any) error {
	r, _ := contract.Load()
	e := r.Error(code, message)
	if evidence != nil {
		e.Evidence = evidence
	}
	return e
}

// Register accepts exact primitive sinks only. It cannot grant an adapter
// authority over a checkpoint, a frozen config or another turn's evidence.
func (g *TurnGuard) Register(r agent.PrimitiveRegistration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == "" {
		return nil
	} // preparation precedes the active inventory
	if r.TurnID != g.active {
		return failure("STATE_INVALID", "Sink belongs to another turn", nil)
	}
	for i, group := range [][]string{r.ControllerPaths, r.AgentPaths} {
		for _, path := range group {
			if filepath.Clean(path) != path || strings.ContainsRune(path, 0) ||
				!(strings.HasPrefix(path, "state/turns/"+g.active+"/") || strings.HasPrefix(path, "state/control/session-")) {
				return failure("STATE_INVALID", "Invalid primitive sink", map[string]any{"path": path})
			}
			kind := r.Operation
			if i == 1 {
				kind = "agent:" + kind
			}
			g.paths[path] = kind
		}
	}
	return nil
}

// Authorize records the observation at a successful controller write, whose
// expected bytes the adapter knows. Registration alone is not authorization.
func (g *TurnGuard) Authorize(path string, observed store.FileObservation) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == "" {
		return nil
	}
	if _, ok := g.paths[path]; !ok {
		return failure("STATE_INVALID", "Controller write has no registered sink", map[string]any{"path": path})
	}
	observed.Path = filepath.Join(g.Store.Path, path)
	g.confirmed[path] = observed
	return nil
}

func (g *TurnGuard) Execute(ctx context.Context, q review.TurnRequest, op review.Operation) (out review.TurnOutcome, err error) {
	if g.Store == nil || g.Gate == nil || op == nil {
		return out, fmt.Errorf("Turn guard is incomplete")
	}
	if err = g.Gate.Check(); err != nil {
		return out, err
	}
	before, err := g.Store.Inspect(ctx, store.Protection{Actor: q.Role, AnswerTurn: q.Purpose == "answer_record", AllowedHistory: g.AllowedHistory})
	if err != nil {
		return out, err
	}
	g.mu.Lock()
	if g.active != "" {
		g.mu.Unlock()
		return out, fmt.Errorf("Concurrent guarded turn")
	}
	g.active = q.TurnID
	g.paths = make(map[string]string)
	g.confirmed = make(map[string]store.FileObservation)
	g.Proof = nil
	g.mu.Unlock()
	defer func() {
		// Use an uncancelled context for the comparison after an interrupt. A
		// signal must not waive tamper detection or turn a partial result into proof.
		checkCtx := context.WithoutCancel(ctx)
		g.mu.Lock()
		paths := g.paths
		confirmed := g.confirmed
		g.active = ""
		g.paths = nil
		g.confirmed = nil
		g.mu.Unlock()
		var changes []store.AuthorizedChange
		for path, kind := range paths {
			_, after, readErr := g.Store.ReadObserved(path, store.TextLimit)
			if readErr != nil {
				err = readErr
				continue
			}
			if after.Kind == "absent" {
				continue
			}
			after.Path = filepath.Join(g.Store.Path, path)
			if !strings.HasPrefix(kind, "agent:") && confirmed[path] != after {
				continue
			}
			changes = append(changes, store.AuthorizedChange{Path: path, Before: before.Files[path], After: after, Kind: kind})
		}
		mutation, compareErr := g.Store.Compare(checkCtx, before, changes)
		if mutation != nil {
			code := "PLANNER_MUTATION"
			if q.Role == "critic" {
				code = "CRITIC_MUTATION"
			}
			err = failure(code, "Agent changed protected files", map[string]any{"actor": q.Role, "paths": mutation.Paths, "before": mutation.Before, "after": mutation.After})
		} else if compareErr != nil {
			err = compareErr
		} else {
			g.Proof = &GuardProof{Before: before, Changes: changes}
		}
	}()
	if err = g.Gate.Check(); err != nil {
		return out, err
	}
	return op(ctx)
}
