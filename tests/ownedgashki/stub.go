package ownedgashki

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type stubFrame struct {
	Raw    []byte `json:"raw"`
	Exit   int    `json:"exit"`
	Stderr string `json:"stderr,omitempty"`
}
type stubInvocation struct {
	Verb  string   `json:"verb"`
	Args  []string `json:"args"`
	Input string   `json:"input"`
	PID   int      `json:"pid"`
}

func Run() int {
	at := -1
	for i, a := range os.Args {
		if a == "volley-owned-gk" {
			at = i
			break
		}
	}
	if at < 0 {
		return -1
	}
	root := os.Getenv("VOLLEY_GK_CANNED_ROOT")
	if root == "" {
		return 90
	}
	args := os.Args[at+1:]
	start := 0
	for start < len(args) && strings.HasPrefix(args[start], "--") {
		start++
	}
	verb := "version"
	if start < len(args) {
		verb = args[start]
		if verb == "config" && start+1 < len(args) {
			verb += " " + args[start+1]
			if args[start+1] == "get" && start+2 < len(args) {
				verb += " " + args[start+2]
			}
		}
	}
	input, e := io.ReadAll(os.Stdin)
	if e != nil {
		return 91
	}
	path := filepath.Join(root, "calls.jsonl")
	prior, _ := os.ReadFile(path)
	count := 1
	for _, line := range strings.Split(string(prior), "\n") {
		var r stubInvocation
		if json.Unmarshal([]byte(line), &r) == nil && r.Verb == verb {
			count++
		}
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return 92
	}
	_ = json.NewEncoder(f).Encode(stubInvocation{verb, args, string(input), os.Getpid()})
	_ = f.Close()
	name := strings.ReplaceAll(verb, " ", "-")
	b, e := os.ReadFile(filepath.Join(root, fmt.Sprintf("%s-%04d.json", name, count)))
	if e != nil {
		b, e = os.ReadFile(filepath.Join(root, name+".json"))
	}
	if e != nil {
		return 93
	}
	var frame stubFrame
	if json.Unmarshal(b, &frame) != nil {
		return 94
	}
	_, _ = os.Stdout.Write(frame.Raw)
	_, _ = os.Stderr.WriteString(frame.Stderr)
	return frame.Exit
}
