// Package conformance owns generated probes and their checked verdict pins.
package conformance

type Target struct {
	Verb  string `json:"verb"`
	Flag  string `json:"flag"`
	Stage string `json:"stage"`
	Shape string `json:"shape"`
	Node  string `json:"node"`
}
type Observation struct {
	Exit int    `json:"exit"`
	OK   bool   `json:"ok"`
	Code string `json:"code"`
}
type Verdict struct {
	ID        string      `json:"id"`
	RequestID string      `json:"request_id"`
	Target    Target      `json:"target"`
	Verdict   string      `json:"verdict"`
	Reason    string      `json:"reason"`
	Observed  Observation `json:"observed"`
}
type Case struct {
	ID          string
	Target      Target
	Args        []string
	Want        Observation
	Human       bool
	Hint        bool
	Fault       string
	Unavailable string
}
