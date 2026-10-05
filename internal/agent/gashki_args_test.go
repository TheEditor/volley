//go:build darwin || linux

package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/prompt"
	"github.com/TheEditor/volley/internal/review"
)

func TestAGK14ProviderArgumentArrays(t *testing.T) {
	settings := config.Settings{ClaudeModel: "opus[1m]", ClaudeEffort: "high", CodexModel: "gpt-model", CodexEffort: "high", ContextDir: "/owned/context [literal]*? (space)", ClaudePlannerTools: []string{"Read", "Glob", "Skill", "Edit", "Write"}, ClaudeCriticTools: []string{"Read", "Grep", "Skill"}}
	root := "/owned/work [literal]*? (space)"
	for _, provider := range []string{"claude", "codex"} {
		for _, role := range []string{"planner", "critic", "second"} {
			t.Run(provider+"/"+role, func(t *testing.T) {
				q := ArgumentOptions{Request: review.TurnRequest{Role: role, Provider: provider, Workspace: root}, Settings: settings, Skills: prompt.SkillPlan{ReadRoots: []string{"/owned/skill link", "/owned/skill target"}, SystemInstruction: "Read skills at /owned/skill link."}, CommittedHistory: []string{"rounds/r01.spec.md", "rounds/r01.critique.md"}, InheritedChecked: true}
				args, s, e := BuildGashkiArguments(q)
				if e != nil {
					t.Fatal(e)
				}
				b, e := json.Marshal(args)
				if e != nil {
					t.Fatal(e)
				}
				var round []string
				if json.Unmarshal(b, &round) != nil || !reflect.DeepEqual(args, round) {
					t.Fatal("array boundaries changed")
				}
				if provider == "codex" {
					if !reflect.DeepEqual(args, []string{"--model", "gpt-model", "-c", `model_reasoning_effort="high"`}) {
						t.Fatal(args)
					}
					return
				}
				if argumentValue(args, "--permission-mode") != "dontAsk" || argumentValue(args, "--model") != "opus[1m]" || argumentValue(args, "--effort") != "high" || argumentValue(args, "--add-dir") != settings.ContextDir || argumentValue(args, "--append-system-prompt") != q.Skills.SystemInstruction {
					t.Fatal(args)
				}
				count := 0
				for _, a := range args {
					if a == "--settings" {
						count++
					}
				}
				if count != 1 {
					t.Fatal("settings object count", count)
				}
				var decoded ClaudeSettings
				if json.Unmarshal([]byte(argumentValue(args, "--settings")), &decoded) != nil || !reflect.DeepEqual(s, decoded) {
					t.Fatal("settings boundaries changed")
				}
				var raw map[string]any
				_ = json.Unmarshal([]byte(argumentValue(args, "--settings")), &raw)
				if _, ok := raw["hooks"]; ok {
					t.Fatal("caller replaced Gashki hooks")
				}
				if s.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "0" {
					t.Fatal(s.Env)
				}
				allow := strings.Join(s.Permissions.Allow, "\n")
				deny := strings.Join(s.Permissions.Deny, "\n")
				if !strings.Contains(allow, `\[literal\]\*\?`) {
					t.Fatal("permission path expanded")
				}
				for _, name := range []string{"state/**)", "volley.config.toml)", "gashki.config.toml)", "BRIEF.md)", "CONSTRAINTS.md)", "rounds/r01.spec.md)", "rounds/r01.critique.md)", "skill link/**)", "skill target/**)"} {
					if !strings.Contains(deny, name) {
						t.Fatal("deny missing", name)
					}
				}
				write, _ := scopedRule("Edit", root, true)
				if role != "planner" {
					write, _ = scopedRule("Edit", root+"/rounds", true)
					tools := strings.Split(argumentValue(args, "--tools"), ",")
					if !containsString(tools, "Write") || containsString(tools, "Edit") {
						t.Fatal(tools)
					}
					for _, name := range []string{"SPEC.md)", "QUESTIONS.md)", "HUMAN.md)"} {
						if !strings.Contains(deny, name) {
							t.Fatal(name)
						}
					}
				}
				if !containsString(s.Permissions.Allow, write) {
					t.Fatal("write rule missing", write)
				}
				for _, rule := range s.Permissions.Allow {
					if strings.HasPrefix(rule, "Edit(") && rule != write || rule == "Write" || strings.HasPrefix(rule, "Write(") {
						t.Fatal("broader write", rule)
					}
				}
				q.InheritedAllow = []string{write}
				if _, _, e := BuildGashkiArguments(q); e != nil {
					t.Fatal(e)
				}
				for _, bad := range []string{"Edit", "Write", "Edit(//**)", "Bash", "NotebookEdit"} {
					q.InheritedAllow = []string{bad}
					if _, _, e := BuildGashkiArguments(q); e == nil {
						t.Fatal("inherited permission accepted", bad)
					}
				}
				q.InheritedAllow = nil
				q.InheritedChecked = false
				if _, _, e := BuildGashkiArguments(q); e == nil {
					t.Fatal("unknown permissions accepted")
				}
			})
		}
	}
	for _, tools := range [][]string{nil, {"Read", "Read"}, {"Read", "Bash"}, {"Read", "Edit"}, {"Read", "unknown"}} {
		q := ArgumentOptions{Request: review.TurnRequest{Role: "critic", Provider: "claude", Workspace: root}, Settings: settings, InheritedChecked: true}
		q.Settings.ClaudeCriticTools = tools
		if _, _, e := BuildGashkiArguments(q); e == nil {
			t.Fatal("invalid critic tools accepted", tools)
		}
	}
}
func containsString(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
