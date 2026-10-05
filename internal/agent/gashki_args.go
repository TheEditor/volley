//go:build darwin || linux

package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TheEditor/volley/internal/process"
)

// BuildGashkiArguments builds only the caller's startup arguments. Gashki owns
// hook installation and merges this one Claude settings object with its hooks.
func BuildGashkiArguments(q ArgumentOptions) ([]string, ClaudeSettings, error) {
	var settings ClaudeSettings
	r := q.Request
	if r.Role != "planner" && r.Role != "critic" && r.Role != "second" || r.Provider != "claude" && r.Provider != "codex" || !filepath.IsAbs(r.Workspace) {
		return nil, settings, fmt.Errorf("Invalid Gashki argument context")
	}
	for _, v := range []string{q.Settings.ClaudeModel, q.Settings.ClaudeEffort, q.Settings.CodexModel, q.Settings.CodexEffort} {
		if v != "" {
			if e := process.ValidateIdentifier(v); e != nil {
				return nil, settings, e
			}
		}
	}
	args := []string{}
	if r.Provider == "codex" {
		if q.Settings.CodexModel != "" {
			args = append(args, "--model", q.Settings.CodexModel)
		}
		if q.Settings.CodexEffort != "" {
			args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(q.Settings.CodexEffort))
		}
		// The pinned Gashki pane is workspace-write for each role. Protected-set
		// detection applies after every turn; this does not claim prevention.
		return args, settings, nil
	}
	tools := append([]string{}, q.Settings.ClaudeCriticTools...)
	if r.Role == "planner" {
		tools = append([]string{}, q.Settings.ClaudePlannerTools...)
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool] {
			return nil, settings, fmt.Errorf("Repeated file tool")
		}
		seen[tool] = true
		switch tool {
		case "Read", "Glob", "Grep", "Skill", "Write":
		case "Edit":
			if r.Role != "planner" {
				return nil, settings, fmt.Errorf("Pane critic has no Edit tool")
			}
		default:
			return nil, settings, fmt.Errorf("Command or unknown file tool")
		}
	}
	if len(tools) == 0 {
		return nil, settings, fmt.Errorf("File tool list is empty")
	}
	if r.Role != "planner" && !seen["Write"] {
		tools = append(tools, "Write")
	}
	var err error
	settings, err = claudeSettings(q, tools, true)
	if err != nil {
		return nil, settings, err
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return nil, settings, err
	}
	args = append(args, "--permission-mode", "dontAsk", "--tools", strings.Join(tools, ","), "--settings", string(b))
	if q.Settings.ClaudeModel != "" {
		args = append(args, "--model", q.Settings.ClaudeModel)
	}
	if q.Settings.ClaudeEffort != "" {
		args = append(args, "--effort", q.Settings.ClaudeEffort)
	}
	if q.Skills.SystemInstruction != "" {
		args = append(args, "--append-system-prompt", q.Skills.SystemInstruction)
	}
	if q.Settings.ContextDir != "" {
		args = append(args, "--add-dir", q.Settings.ContextDir)
	}
	return args, settings, nil
}
