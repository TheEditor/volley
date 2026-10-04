// Package conformance owns generated probes and their checked verdict pins.
package conformance

type Target struct {
	Verb  string `json:"verb,omitempty"`
	Flag  string `json:"flag,omitempty"`
	Stage string `json:"stage,omitempty"`
	Shape string `json:"shape,omitempty"`
	Node  string `json:"node,omitempty"`
}
type Observation struct {
	Exit int
	OK   bool
	Code string
}
type Verdict struct {
	ID       string
	Target   Target
	Verdict  string
	Reason   string
	Observed Observation
}
