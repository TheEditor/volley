// Package config owns typed settings and their source records.
package config

type Settings struct {
	Planner              string   `json:"planner"`
	Backend              string   `json:"backend"`
	MaxRounds            int      `json:"max_rounds"`
	WaitTimeout          string   `json:"wait_timeout"`
	PollInterval         string   `json:"poll_interval"`
	Persistent           bool     `json:"persistent"`
	ClosingPass          bool     `json:"closing_pass"`
	SecondOpinion        bool     `json:"second_opinion"`
	Rubric               string   `json:"rubric"`
	ContextDir           string   `json:"context_dir"`
	ClaudeBin            string   `json:"claude_bin"`
	CodexBin             string   `json:"codex_bin"`
	GashkiBin            string   `json:"gashki_bin"`
	GashkiConfig         string   `json:"gashki_config"`
	ClaudeModel          string   `json:"claude_model"`
	CodexModel           string   `json:"codex_model"`
	ClaudeEffort         string   `json:"claude_effort"`
	CodexEffort          string   `json:"codex_effort"`
	ClaudePermissionMode string   `json:"claude_permission_mode"`
	ClaudePlannerTools   []string `json:"claude_planner_tools"`
	ClaudeCriticTools    []string `json:"claude_critic_tools"`
	TrustFolder          bool     `json:"trust_folder"`
	Placement            string   `json:"placement"`
	AllowAPIKey          bool     `json:"allow_api_key"`
}
type Source struct {
	Source           string `json:"source"`
	Path             string `json:"path"`
	Line             int    `json:"line"`
	Flag             string `json:"flag"`
	ArgumentPosition int    `json:"argument_position"`
	Rule             string `json:"rule"`
}
type Resolved struct {
	Settings     Settings
	Sources      map[string]Source
	SelectedFile string
	Explicit     bool
	Exists       bool
	SelectedHash string
}
