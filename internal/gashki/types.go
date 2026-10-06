// Package gashki owns checked public Gashki operations, not its private ledger.
package gashki

type Pane struct {
	UUID        string
	Selector    string
	Provider    string
	Target      string
	ReadyCursor string
}
type Server struct {
	SocketPath     string
	CallerWindow   string
	EvidenceReason string
}
type CallReceipt struct {
	Verb         string
	Exit         int
	RawPath      string
	StderrPath   string
	Cursor       string
	Binary       string
	ContractHash string
}
