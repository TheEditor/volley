package conformance

import (
	"github.com/TheEditor/volley/internal/contract"
	"sort"
)

func Declared(r *contract.Registry, handlerKind func(string) string) (any, error) {
	names := []string{}
	for name := range r.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	results := []any{}
	pass, fail := 0, 0
	for _, name := range names {
		command := r.Commands[name]
		_, err := contract.Schema(command.Schema)
		verdict, reason := "pass", "Declared command has a typed handler and a response schema"
		if handlerKind(name) == "" || err != nil {
			verdict = "fail"
			reason = "Handler or schema is absent"
			fail++
		} else {
			pass++
		}
		results = append(results, map[string]any{"id": "declaration/" + name, "target": map[string]any{"verb": name, "flag": "", "stage": "command_path", "shape": "", "node": ""}, "verdict": verdict, "reason": reason, "observed": map[string]any{"exit": 0, "ok": verdict == "pass", "code": ""}})
	}
	data := map[string]any{"profile": "declared-command-tree", "results": results, "counts": map[string]any{"pass": pass, "fail": fail, "not_applicable": 0}, "verdicts": []string{"pass", "fail", "not_applicable"}}
	if fail > 0 {
		return data, r.Error("CONFORMANCE_FAILED", "Declared command tree is incomplete")
	}
	return data, nil
}
