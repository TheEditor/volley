//go:build darwin || linux

package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/process"
	"github.com/TheEditor/volley/internal/prompt"
	"github.com/TheEditor/volley/internal/review"
)

type ArgumentOptions struct {
	Request                          review.TurnRequest
	Settings                         config.Settings
	Executable, ReplyTemp, SessionID string
	Skills                           prompt.SkillPlan
	CommittedHistory                 []string
	// A caller must inspect the selected inherited permission sources. The
	// builder refuses an unverified source or a write rule broader than its own.
	InheritedChecked bool
	InheritedAllow   []string
}

type ClaudeSettings struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
	Env map[string]string `json:"env,omitempty"`
}

func scopedRule(tool, path string, descendants bool) (string, error) {
	r, err := prompt.ClaudeReadRule(path)
	if err != nil {
		return "", err
	}
	r = tool + strings.TrimPrefix(r, "Read")
	if !descendants {
		r = strings.TrimSuffix(r, "/**)") + ")"
	}
	return r, nil
}

func directTools(role string, tools []string) error {
	if len(tools) == 0 {
		return fmt.Errorf("File tool list is empty")
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool] {
			return fmt.Errorf("Repeated file tool")
		}
		seen[tool] = true
		switch tool {
		case "Read", "Glob", "Grep", "Skill":
		case "Edit", "Write":
			if role != "planner" {
				return fmt.Errorf("Direct critic tools must be read-only")
			}
		default:
			return fmt.Errorf("Command and unknown tools are not permitted")
		}
	}
	return nil
}

func directClaudeSettings(q ArgumentOptions, tools []string) (ClaudeSettings, error) {
	return claudeSettings(q, tools, false)
}

func claudeSettings(q ArgumentOptions, tools []string, pane bool) (ClaudeSettings, error) {
	var settings ClaudeSettings
	settings.Permissions.Allow = []string{}
	settings.Permissions.Deny = []string{}
	if !q.InheritedChecked {
		return settings, fmt.Errorf("Inherited permission sources are unverified")
	}
	workspaceRule, err := scopedRule("Edit", q.Request.Workspace, true)
	if err != nil {
		return settings, err
	}
	writeRule := workspaceRule
	if pane && q.Request.Role != "planner" {
		writeRule, err = scopedRule("Edit", filepath.Join(q.Request.Workspace, "rounds"), true)
		if err != nil {
			return settings, err
		}
	}
	for _, rule := range q.InheritedAllow {
		name := strings.SplitN(rule, "(", 2)[0]
		switch name {
		case "Read", "Glob", "Grep", "Skill":
		case "Edit", "Write", "MultiEdit", "NotebookEdit":
			if q.Request.Role != "planner" && !pane || rule != writeRule {
				return settings, fmt.Errorf("Inherited write permission exceeds the declared boundary")
			}
		default:
			return settings, fmt.Errorf("Inherited permission rule is unsupported")
		}
	}
	roots := append([]string{q.Request.Workspace}, q.Skills.ReadRoots...)
	if q.Settings.ContextDir != "" {
		roots = append(roots, q.Settings.ContextDir)
	}
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		seen[root] = true
		r, err := scopedRule("Read", root, true)
		if err != nil {
			return settings, err
		}
		settings.Permissions.Allow = append(settings.Permissions.Allow, r)
		if root != q.Request.Workspace {
			d, err := scopedRule("Edit", root, true)
			if err != nil {
				return settings, err
			}
			settings.Permissions.Deny = append(settings.Permissions.Deny, d)
		}
	}
	for _, tool := range tools {
		if tool == "Glob" || tool == "Grep" || tool == "Skill" {
			settings.Permissions.Allow = append(settings.Permissions.Allow, tool)
		}
	}
	if q.Request.Role == "planner" {
		settings.Permissions.Allow = append(settings.Permissions.Allow, workspaceRule)
		// These names are protected even before the controller creates them.
		// Gitignore cannot express a run of digits alone. The numbered patterns
		// also deny digit-prefixed lookalikes with controller artifact suffixes.
		roundsRule, err := scopedRule("Edit", filepath.Join(q.Request.Workspace, "rounds"), false)
		if err != nil {
			return settings, err
		}
		roundsPrefix := strings.TrimSuffix(roundsRule, ")") + "/"
		for _, suffix := range []string{".critique.md", ".critique-retry.md", ".response.md", ".answer-response.md", ".spec.md", ".human.md", ".questions.md"} {
			settings.Permissions.Deny = append(settings.Permissions.Deny, roundsPrefix+"r[0-9]*"+suffix+")")
		}
		for _, name := range []string{"second-opinion.md", "closing-*"} {
			settings.Permissions.Deny = append(settings.Permissions.Deny, roundsPrefix+name+")")
		}
		for _, name := range []string{"state", "volley.config.toml", "gashki.config.toml", "BRIEF.md", "CONSTRAINTS.md"} {
			r, err := scopedRule("Edit", filepath.Join(q.Request.Workspace, name), name == "state")
			if err != nil {
				return settings, err
			}
			settings.Permissions.Deny = append(settings.Permissions.Deny, r)
		}
		for _, path := range q.CommittedHistory {
			if !filepath.IsAbs(path) {
				path = filepath.Join(q.Request.Workspace, path)
			}
			rel, err := filepath.Rel(filepath.Join(q.Request.Workspace, "rounds"), path)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
				return settings, fmt.Errorf("Committed history path escapes rounds")
			}
			r, err := scopedRule("Edit", path, false)
			if err != nil {
				return settings, err
			}
			settings.Permissions.Deny = append(settings.Permissions.Deny, r)
		}
	} else if !pane {
		settings.Permissions.Deny = append(settings.Permissions.Deny, workspaceRule)
	} else {
		settings.Permissions.Allow = append(settings.Permissions.Allow, writeRule)
		for _, name := range []string{"state", "volley.config.toml", "gashki.config.toml", "BRIEF.md", "CONSTRAINTS.md", "SPEC.md", "QUESTIONS.md", "HUMAN.md"} {
			r, err := scopedRule("Edit", filepath.Join(q.Request.Workspace, name), name == "state")
			if err != nil {
				return settings, err
			}
			settings.Permissions.Deny = append(settings.Permissions.Deny, r)
		}
		for _, path := range q.CommittedHistory {
			if !filepath.IsAbs(path) {
				path = filepath.Join(q.Request.Workspace, path)
			}
			rel, err := filepath.Rel(filepath.Join(q.Request.Workspace, "rounds"), path)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
				return settings, fmt.Errorf("Committed history path escapes rounds")
			}
			r, err := scopedRule("Edit", path, false)
			if err != nil {
				return settings, err
			}
			settings.Permissions.Deny = append(settings.Permissions.Deny, r)
		}
	}
	if strings.HasSuffix(q.Settings.ClaudeModel, "[1m]") {
		settings.Env = map[string]string{"CLAUDE_CODE_DISABLE_1M_CONTEXT": "0"}
	}
	sort.Strings(settings.Permissions.Allow)
	sort.Strings(settings.Permissions.Deny)
	return settings, nil
}

// BuildDirectArguments is pure. Tool availability and permission rules are
// separate. Its result never grants unscoped Write/Edit or a command tool.
func BuildDirectArguments(q ArgumentOptions) ([]string, ClaudeSettings, error) {
	var settings ClaudeSettings
	request := q.Request
	if request.Role != "planner" && request.Role != "critic" || request.Provider != "claude" && request.Provider != "codex" || !filepath.IsAbs(request.Workspace) || !filepath.IsAbs(q.Executable) || !filepath.IsAbs(q.ReplyTemp) {
		return nil, settings, fmt.Errorf("Invalid direct argument context")
	}
	for _, value := range []string{q.Settings.ClaudeModel, q.Settings.CodexModel, q.Settings.ClaudeEffort, q.Settings.CodexEffort} {
		if value != "" {
			if err := process.ValidateIdentifier(value); err != nil {
				return nil, settings, err
			}
		}
	}
	if q.SessionID != "" && !validUUID(q.SessionID) {
		return nil, settings, fmt.Errorf("Invalid exact session UUID")
	}
	if request.Provider == "claude" {
		tools := q.Settings.ClaudeCriticTools
		if request.Role == "planner" {
			tools = q.Settings.ClaudePlannerTools
		}
		if err := directTools(request.Role, tools); err != nil {
			return nil, settings, err
		}
		var err error
		settings, err = directClaudeSettings(q, tools)
		if err != nil {
			return nil, settings, err
		}
		b, err := json.Marshal(settings)
		if err != nil {
			return nil, settings, err
		}
		args := []string{q.Executable, "--print", "--output-format", "text", "--permission-mode", "dontAsk", "--tools", strings.Join(tools, ","), "--settings", string(b)}
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
		if q.SessionID != "" {
			flag := "--session-id"
			if request.SessionID != "" {
				flag = "--resume"
			}
			args = append(args, flag, q.SessionID)
		}
		return append(args, "--", string(request.Prompt)), settings, nil
	}
	sandbox := "read-only"
	if request.Role == "planner" {
		sandbox = "workspace-write"
	}
	args := []string{q.Executable, "exec"}
	if request.SessionID != "" {
		args = append(args, "resume", q.SessionID, "-c", "sandbox_mode="+strconv.Quote(sandbox))
	} else {
		args = append(args, "--sandbox", sandbox, "--cd", request.Workspace)
	}
	args = append(args, "--json", "--skip-git-repo-check", "--output-last-message", q.ReplyTemp)
	if q.Settings.CodexModel != "" {
		args = append(args, "--model", q.Settings.CodexModel)
	}
	if q.Settings.CodexEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(q.Settings.CodexEffort))
	}
	return append(args, "--", string(request.Prompt)), settings, nil
}
