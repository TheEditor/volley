//go:build darwin || linux

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/TheEditor/volley/internal/agent"
	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/store"
)

type Creation struct {
	RecordVersion  int                              `json:"record_version"`
	RunID          string                           `json:"run_id"`
	Resolved       config.Resolved                  `json:"resolved"`
	Inputs         map[string]store.FileObservation `json:"inputs"`
	Seeds          map[string]store.FileObservation `json:"seeds"`
	Brief          string                           `json:"brief"`
	Seed           string                           `json:"seed"`
	Key            string                           `json:"key"`
	Caller         agent.IdentityContext            `json:"caller"`
	APIKeyExplicit bool                             `json:"api_key_explicit"`
}

func (o *Owner) creationRecord() (Creation, error) {
	c := Creation{RecordVersion: 1, Resolved: *o.Request.Resolved, Seeds: map[string]store.FileObservation{}, Brief: o.Request.Brief, Seed: o.Request.Seed, Key: o.Request.Key, Caller: agent.CaptureIdentity(o.Options.Env), APIKeyExplicit: o.Request.Explicit["allow_api_key"] != nil}
	var err error
	c.Inputs, err = bindInputs(o.Store)
	if err != nil {
		return c, err
	}
	for target, source := range map[string]string{"BRIEF.md": c.Brief, "SPEC.md": c.Seed} {
		if source != "" {
			_, obs, e := readSeed(source)
			if e != nil {
				return c, e
			}
			c.Seeds[target] = obs
		}
	}
	return c, nil
}
func (o *Owner) recoverCreation(ctx context.Context, m store.Snapshot) error {
	if len(m.Object("current_turn")) > 0 {
		return failure("STATE_INVALID", "Setup cannot contain an active model turn", nil)
	}
	c, err := o.loadCreation(m)
	if err != nil {
		return err
	}
	if err = agent.CompareIdentity(c.Caller, agent.CaptureIdentity(o.Options.Env), nil, nil); err != nil {
		return err
	}
	saved, _ := contract.Canonical(c.Resolved.Settings)
	values, err := inputObject(saved)
	if err != nil {
		return err
	}
	for key, value := range o.Request.Explicit {
		before, _ := contract.Canonical(values[key])
		after, _ := contract.Canonical(value)
		if !bytes.Equal(before, after) {
			return failure("CONFIG_CONFLICT", "Explicit setting differs from saved setup: "+key, nil)
		}
	}
	if o.Request.Key != "" && o.Request.Key != c.Key {
		return failure("IDEMPOTENCY_CONFLICT", "Creation key differs", nil)
	}
	for target, source := range map[string]string{"BRIEF.md": c.Brief, "SPEC.md": c.Seed} {
		if source == "" {
			continue
		}
		_, now, e := readSeed(source)
		if e != nil {
			return e
		}
		if now != c.Seeds[target] {
			return failure("ARTIFACT_CHANGED", "Seed source changed during setup", map[string]any{"path": source})
		}
	}
	for target, source := range map[string]string{"BRIEF.md": o.Request.Brief, "SPEC.md": o.Request.Seed} {
		if source != "" {
			_, obs, e := readSeed(source)
			if e != nil {
				return e
			}
			if obs != c.Seeds[target] {
				return failure("CONFIG_CONFLICT", "Explicit seed differs from saved setup", nil)
			}
		}
	}
	history, err := o.Store.History()
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(c.Inputs))
	for path := range c.Inputs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		before := c.Inputs[path]
		now, e := observation(o.Store, path)
		if e != nil {
			return e
		}
		if now == before {
			continue
		}
		seed, hasSeed := c.Seeds[path]
		allowed := false
		if before.Kind == "absent" && hasSeed && now.Kind == "file" && now.Hash == seed.Hash {
			for _, tx := range history {
				for _, artifact := range tx.Artifacts {
					if artifact.TargetPath != path || artifact.Hash != seed.Hash {
						continue
					}
					candidate := artifact.StagedPath
					if artifact.CopyCandidate != nil {
						expected := *artifact.CopyCandidate
						expected.Path = path
						allowed = now == expected
					} else {
						stage, e := o.Store.Observe(candidate)
						if e != nil {
							return e
						}
						stage.Path = path
						allowed = now == stage
					}
					if allowed {
						break
					}
				}
				if allowed {
					break
				}
			}
		}
		if !allowed {
			return failure("ARTIFACT_CHANGED", "Initial input changed during setup", map[string]any{"path": path})
		}
	}
	if c.Resolved.Exists {
		doc, e := config.ReadFile(c.Resolved.SelectedFile)
		if e != nil {
			return e
		}
		if contract.HashBytes(doc.Bytes) != c.Resolved.SelectedHash {
			return failure("ARTIFACT_CHANGED", "Source config changed during setup", nil)
		}
	} else if _, e := os.Stat(c.Resolved.SelectedFile); !os.IsNotExist(e) {
		return failure("ARTIFACT_CHANGED", "Source config appeared during setup", nil)
	}
	o.Request.Resolved = &c.Resolved
	o.Request.Brief = c.Brief
	o.Request.Seed = c.Seed
	o.Request.Key = c.Key
	if c.APIKeyExplicit {
		o.Request.Explicit = map[string]any{"allow_api_key": c.Resolved.Settings.AllowAPIKey}
	} else {
		o.Request.Explicit = map[string]any{}
	}
	return o.create(ctx, m)
}
func frozenPreparedSettings(s *store.Store, p *agent.Prepared, c config.Resolved) error {
	doc, err := config.ReadFile(filepath.Join(s.Path, "volley.config.toml"))
	if err != nil {
		return err
	}
	b, err := json.Marshal(doc.Values)
	if err != nil {
		return err
	}
	var settings config.Settings
	if err = json.Unmarshal(b, &settings); err != nil {
		return err
	}
	c.Settings = settings
	p.Settings = c
	return nil
}
func stageCreation(s *store.Store, id string, c Creation) (map[string]any, error) {
	c.RunID = id
	b, err := contract.Canonical(c)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("state/control/creation-%s.json", id)
	hash, err := s.StagePrivateText(path, b)
	return map[string]any{"path": path, "sha256": hash}, err
}

func (o *Owner) loadCreation(m store.Snapshot) (Creation, error) {
	var c Creation
	ref := m.Object("creation")
	path, _ := ref["path"].(string)
	if path != "state/control/creation-"+m.String("run_id")+".json" {
		return c, failure("STATE_INVALID", "Saved creation request is absent", nil)
	}
	b, err := o.Store.ReadText(path)
	if err != nil {
		return c, err
	}
	if contract.HashBytes(b) != ref["sha256"] {
		return c, failure("STATE_INVALID", "Saved creation request changed", nil)
	}
	if err = decode(b, &c); err != nil {
		return c, err
	}
	if c.RecordVersion != 1 || c.RunID != m.String("run_id") {
		return c, failure("STATE_INVALID", "Creation run binding differs", nil)
	}
	return c, nil
}
