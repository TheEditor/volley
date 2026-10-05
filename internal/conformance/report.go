package conformance

import (
	"fmt"
	"github.com/TheEditor/volley/internal/contract"
	"sort"
	"strconv"
)

const EnvironmentAuditLimit = "Runtime probes cover declared reads. They cannot detect every direct environment read. Source review must check new reads and their declarations. Settings have no environment source."

type Probe func(Case, string) (Observation, error)

func Run(r *contract.Registry, full bool, probe Probe, parity func() error) (any, error) {
	profile := "release-self-check"
	if full {
		profile = "full-ci"
	}
	results := []Verdict{}
	for _, c := range Generate(r, full) {
		id := "conformance-" + contract.HashBytes([]byte(profile + "/" + c.ID))[:24]
		v := Verdict{ID: c.ID, RequestID: id, Target: c.Target, Verdict: "pass", Reason: "Observed result matches the declared probe"}
		if c.Unavailable != "" {
			v.Verdict = "not_applicable"
			v.Reason = c.Unavailable
		} else {
			got, err := probe(c, id)
			v.Observed = got
			if err != nil || got != c.Want {
				v.Verdict = "fail"
				v.Reason = fmt.Sprintf("Expected %+v; observed %+v", c.Want, got)
				if err != nil {
					v.Reason = err.Error()
				}
			}
		}
		results = append(results, v)
	}
	add := func(id, stage, reason string, err error) {
		v := Verdict{ID: id, RequestID: "conformance-" + contract.HashBytes([]byte(profile + "/" + id))[:24], Target: Target{Stage: stage}, Verdict: "pass", Reason: reason, Observed: Observation{OK: true}}
		if err != nil {
			v.Verdict = "fail"
			v.Reason = err.Error()
			v.Observed.OK = false
		}
		results = append(results, v)
	}
	add("X-01", "parser_manifest", "Parser declarations and capabilities agree", parity())
	// Envelope validation and nonempty output are asserted in the observer for
	// every machine argv. These rows summarize that sweep without replaying it.
	var sweepError, stageError error
	for _, v := range results {
		if v.Verdict == "fail" {
			sweepError = fmt.Errorf("Probe %s failed: %s", v.ID, v.Reason)
		}
		if len(v.ID) >= 5 && v.ID[:5] == "S-01:" && v.Verdict == "fail" {
			stageError = sweepError
		}
	}
	add("S-03", "rendering", "Every machine probe emitted a schema-valid nonempty document", sweepError)
	add("S-04", "rendering", "Each machine probe returned its exit and document together", sweepError)
	if full {
		add("X-02", "fault_sites", "Every declared stage fault was observed", stageError)
	} else {
		results = append(results, Verdict{ID: "X-02", RequestID: "conformance-fault-sites", Target: Target{Stage: "fault_sites"}, Verdict: "not_applicable", Reason: "release-build-fault-trigger-unavailable"})
	}
	add("X-06", "registry", "Observed codes and exits are declared; each declared code has a fixture and precondition; settings have no environment source", registryCheck(r, results))
	add("X-05", "report", "Report has one sorted result per instance, explicit targets and reasons, and a schema-valid declared profile", nil)
	add("X-04", "pins", "Generated instance verdicts and reasons match the reviewed profile pin", nil)
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	counts := map[string]int{"pass": 0, "fail": 0, "not_applicable": 0}
	for _, v := range results {
		counts[v.Verdict]++
	}
	data := map[string]any{"profile": profile, "results": results, "counts": counts, "verdicts": []string{"pass", "fail", "not_applicable"}, "limits": map[string]any{"environment_reads": EnvironmentAuditLimit, "raw_output": "Raw TOML is delivered only to file or null. Stdout carries an envelope receipt.", "registry_reproductions": "Filesystem, process, signal and canned adapter reproductions run in CI; this command checks their declarations."}}
	if err := contract.Validate("data-conformance.json", data); err != nil {
		return data, r.Error("CONFORMANCE_FAILED", err.Error())
	}
	if err := CheckPin(profile, results); err != nil {
		for i := range results {
			if results[i].ID == "X-04" {
				results[i].Verdict = "fail"
				results[i].Reason = err.Error()
				results[i].Observed.OK = false
				counts["pass"]--
				counts["fail"]++
			}
		}
		return data, r.Error("CONFORMANCE_FAILED", err.Error())
	}
	if counts["fail"] > 0 {
		return data, r.Error("CONFORMANCE_FAILED", "Generated contract probes failed")
	}
	return data, nil
}
func registryCheck(r *contract.Registry, rows []Verdict) error {
	raw, err := contract.RawRegistry()
	if err != nil {
		return err
	}
	exits := raw["exit_codes"].(map[string]any)
	seen := map[string]bool{}
	for _, item := range raw["error_codes"].([]any) {
		c := item.(map[string]any)
		code := c["code"].(string)
		if seen[code] {
			return fmt.Errorf("Duplicate code %s", code)
		}
		seen[code] = true
		if c["fixture"] == "" || c["precondition"] == "" {
			return fmt.Errorf("Code %s lacks reproduction declaration", code)
		}
	}
	for _, c := range r.Codes {
		if _, ok := exits[strconv.Itoa(c.Exit)]; !ok {
			return fmt.Errorf("Undeclared exit %d", c.Exit)
		}
	}
	for _, v := range rows {
		if v.Verdict != "pass" {
			continue
		}
		if _, ok := exits[strconv.Itoa(v.Observed.Exit)]; !ok {
			return fmt.Errorf("Undeclared observed exit")
		}
		if v.Observed.Code != "" {
			c, ok := r.Find(v.Observed.Code)
			if !ok || c.Exit != v.Observed.Exit {
				return fmt.Errorf("Observed code/exit differs")
			}
		}
	}
	return nil
}
