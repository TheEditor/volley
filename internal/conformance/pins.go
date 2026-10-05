package conformance

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/TheEditor/volley/internal/contract"
	"sort"
)

//go:embed pins/*.json
var pins embed.FS

type Pin struct {
	Profile string   `json:"profile"`
	Results []Pinned `json:"results"`
}
type Pinned struct {
	ID      string `json:"id"`
	Target  Target `json:"target"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

func CheckPin(profile string, observed []Verdict) error {
	b, err := pins.ReadFile("pins/" + profile + ".json")
	if err != nil {
		return err
	}
	var pin Pin
	if err = json.Unmarshal(b, &pin); err != nil {
		return err
	}
	if pin.Profile != profile {
		return fmt.Errorf("Pin profile differs")
	}
	return ComparePin(pin.Results, observed)
}
func ComparePin(expected []Pinned, observed []Verdict) error {
	before := map[string]Pinned{}
	after := map[string]Pinned{}
	for _, v := range expected {
		if _, ok := before[v.ID]; ok {
			return fmt.Errorf("Duplicate pinned instance %s", v.ID)
		}
		before[v.ID] = v
	}
	for _, v := range observed {
		if _, ok := after[v.ID]; ok {
			return fmt.Errorf("Duplicate observed instance %s", v.ID)
		}
		after[v.ID] = Pinned{v.ID, v.Target, v.Verdict, v.Reason}
	}
	ids := []string{}
	for id := range before {
		ids = append(ids, id)
	}
	for id := range after {
		if _, ok := before[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		old, ok := before[id]
		now, exists := after[id]
		if !ok {
			return fmt.Errorf("Added instance %s requires review", id)
		}
		if !exists {
			return fmt.Errorf("Removed instance %s requires review", id)
		}
		a, _ := contract.Canonical(old)
		b, _ := contract.Canonical(now)
		if !bytes.Equal(a, b) {
			return fmt.Errorf("Changed instance %s (%s to %s, reason or target) requires review", id, old.Verdict, now.Verdict)
		}
	}
	return nil
}
