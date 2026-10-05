//go:build darwin || linux

package gashki

import "strings"

// ControllerSink recognizes only the checked primitive's exact file grammar.
// Registration still requires the current turn and a confirmed controller write.
func ControllerSink(run, turn, path string) bool {
	if !validID(run) || !validID(turn) {
		return false
	}
	if path == "state/prompts/"+run+"-"+turn+".md" {
		return true
	}
	if strings.HasPrefix(path, "state/control/gk-call-") {
		s := strings.TrimPrefix(path, "state/control/gk-call-")
		if len(s) < 26 || !validID(s[:26]) {
			return false
		}
		switch s[26:] {
		case "-intent.json", "-start.json", "-stdout.raw", "-stderr.raw", "-exit.json", "-result.json":
			return true
		}
	}
	for _, role := range []string{"planner", "critic", "second"} {
		base := "state/control/gk-spawn-" + run + "-" + role
		if path == base+"-intent.json" || path == base+"-pane.json" {
			return true
		}
	}
	return false
}
