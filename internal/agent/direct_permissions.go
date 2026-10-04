//go:build darwin || linux

package agent

import (
	"bytes"
	"fmt"
	"path/filepath"

	"github.com/TheEditor/volley/internal/input"
)

// PermissionInspection is a local source observation, not a claim about
// vendor enforcement or remotely supplied policy. The caller supplies every
// selected additional policy source; an unknown source cannot be marked checked.
type PermissionInspection struct {
	Allow   []string
	Sources []initialObservation
}

func InspectPermissionSources(paths []string) (PermissionInspection, error) {
	p := PermissionInspection{Allow: []string{}, Sources: []initialObservation{}}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return p, fmt.Errorf("Permission source must be absolute")
		}
		obs, err := observeInput(path)
		if err != nil {
			return p, err
		}
		p.Sources = append(p.Sources, obs)
		if !obs.Exists {
			continue
		}
		b, _, err := readRegular(path, input.Limit)
		if err != nil {
			return p, err
		}
		v, err := input.Record(bytes.NewReader(b), input.Limit)
		if err != nil {
			return p, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return p, fmt.Errorf("Permission source must be an object")
		}
		raw, exists := m["permissions"]
		if !exists {
			continue
		}
		permissions, ok := raw.(map[string]any)
		if !ok {
			return p, fmt.Errorf("Permissions must be an object")
		}
		if mode, exists := permissions["defaultMode"]; exists {
			s, ok := mode.(string)
			if !ok || s == "bypassPermissions" {
				return p, fmt.Errorf("Inherited permission mode cannot meet the boundary")
			}
		}
		if raw, exists := permissions["allow"]; exists {
			a, ok := raw.([]any)
			if !ok {
				return p, fmt.Errorf("Permission allow must be an array")
			}
			for _, raw := range a {
				rule, ok := raw.(string)
				if !ok {
					return p, fmt.Errorf("Permission rule must be a string")
				}
				p.Allow = append(p.Allow, rule)
			}
		}
	}
	if err := checkInputs(p.Sources); err != nil {
		return p, err
	}
	return p, nil
}
