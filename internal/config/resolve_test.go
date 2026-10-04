package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TheEditor/volley/internal/contract"
)

func fixtureOptions(t *testing.T) ResolveOptions {
	t.Helper()
	root := t.TempDir()
	return ResolveOptions{Cwd: root, Home: filepath.Join(root, "home"), XDGRoot: filepath.Join(root, "config")}
}
func writeConfig(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestACFG04SelectionAndPrecedence(t *testing.T) {
	o := fixtureOptions(t)
	resolved, _, err := Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Settings.Planner != "claude" || resolved.Settings.MaxRounds != 8 || resolved.Exists || resolved.Explicit {
		t.Fatalf("Defaults: %+v", resolved)
	}
	path := resolved.SelectedFile
	writeConfig(t, path, "# file\nplanner='codex'\nmax_rounds=9\n")
	o.Flags = map[string]Override{"max_rounds": {Value: int64(10), Flag: "--max-rounds", Position: 4}}
	resolved, _, err = Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Settings.Planner != "codex" || resolved.Settings.MaxRounds != 10 || resolved.Sources["planner"].Line != 2 || resolved.Sources["planner"].Path != path || resolved.Sources["max_rounds"].Source != "flag" || resolved.Sources["max_rounds"].ArgumentPosition != 4 {
		t.Fatalf("Precedence or provenance: %+v", resolved)
	}
	writeConfig(t, path, "max_rounds=0\n")
	if _, _, err := Resolve(o); err == nil {
		t.Fatal("Override hid a bad file value")
	}
	writeConfig(t, path, "backend='gashki'\npersistent=true\n")
	o.Flags = map[string]Override{"backend": {Value: "cli", Flag: "--backend"}}
	if _, _, err := Resolve(o); err == nil {
		t.Fatal("Override hid an invalid file combination")
	}
	o.Flags = nil
	o.NamedFile = "missing.toml"
	if _, _, err := Resolve(o); err == nil {
		t.Fatal("Missing named file accepted")
	}
	o.AllowMissingNamed = true
	if _, _, err := Resolve(o); err != nil {
		t.Fatal(err)
	}
	o.RequireExists = true
	if _, _, err := Resolve(o); err == nil {
		t.Fatal("Missing validate file accepted")
	}
	o = fixtureOptions(t)
	o.XDGRoot = ""
	resolved, _, err = Resolve(o)
	if err != nil || resolved.SelectedFile != filepath.Join(o.Home, ".config", "volley", "config.toml") {
		t.Fatalf("Empty XDG: %+v %v", resolved, err)
	}
	o.XDGRoot = "relative"
	o.Mutating = true
	if _, _, err := Resolve(o); err == nil {
		t.Fatal("Relative mutating XDG accepted")
	}
	o.Mutating = false
	if _, _, err := Resolve(o); err != nil {
		t.Fatal(err)
	}
	// Environment values never enter resolver options. The warning factory only
	// receives presence. Every retired name has its declared replacement flag.
	raw, _ := contract.RawRegistry()
	present := make(map[string]bool)
	for _, name := range raw["retired_setting_variables"].([]any) {
		present[name.(string)] = true
		t.Setenv(name.(string), "ignored-value")
	}
	warnings, err := RetiredWarnings(func(name string) bool { return present[name] })
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != len(present) {
		t.Fatalf("Warnings: %d", len(warnings))
	}
	for _, w := range warnings {
		if w.Code != "ENV_SETTING_IGNORED" || w.Evidence["replacement_flag"] == "" {
			t.Fatalf("Replacement: %+v", w)
		}
	}
	resolved, _, err = Resolve(o)
	if err != nil || resolved.Settings.Planner != "claude" || resolved.Settings.AllowAPIKey {
		t.Fatalf("Retired environment changed settings: %+v %v", resolved, err)
	}
	t.Log("Defaults, file, flags, missing-file rules, complete-file validation, XDG rules, and all retired-name replacement warnings passed")
}
func TestACFG05PathsAndInactiveLookup(t *testing.T) {
	o := fixtureOptions(t)
	o.NamedFile = "settings/config.toml"
	file := filepath.Join(o.Cwd, o.NamedFile)
	workspace := filepath.Join(o.Cwd, "workspace")
	reference := filepath.Join(o.Cwd, "reference")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reference, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(reference, filepath.Join(o.Cwd, "reference-link")); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, file, "claude_bin='./owned claude'\ncodex_bin='./owned codex'\ngashki_bin='./inactive gashki'\ncontext_dir='../reference-link'\n")
	o.Workspace = "workspace"
	lookups := []string{}
	o.Lookup = func(value string) (string, error) { lookups = append(lookups, value); return value, nil }
	resolved, warnings, err := Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Settings.ClaudeBin != filepath.Join(o.Cwd, "settings", "owned claude") || resolved.Settings.ContextDir != mustPhysical(t, reference) || len(lookups) != 2 || strings.Contains(strings.Join(lookups, "|"), "gashki") {
		t.Fatalf("Lookup or paths: %+v %v", resolved, lookups)
	}
	if len(warnings) != 1 || warnings[0].Code != "INACTIVE_SETTING" {
		t.Fatalf("Inactive warning: %+v", warnings)
	}
	o.Flags = map[string]Override{"claude_bin": {Value: "./flag tool", Flag: "--claude-bin", Position: 8}}
	resolved, _, err = Resolve(o)
	if err != nil || resolved.Settings.ClaudeBin != filepath.Join(o.Cwd, "flag tool") || resolved.Sources["claude_bin"].ArgumentPosition != 8 {
		t.Fatalf("Flag base: %+v %v", resolved, err)
	}
	for _, contextPath := range []string{"../workspace", "../workspace/child"} {
		if err := os.MkdirAll(filepath.Join(workspace, "child"), 0700); err != nil {
			t.Fatal(err)
		}
		writeConfig(t, file, "context_dir='"+contextPath+"'\n")
		if _, _, err := Resolve(o); err == nil {
			t.Fatalf("Contained context accepted: %s", contextPath)
		}
	}
	writeConfig(t, file, "backend='gashki'\nclaude_bin='./inactive claude'\ncodex_bin='./inactive codex'\ngashki_bin='./owned gashki'\n")
	o.Flags = nil
	lookups = nil
	resolved, warnings, err = Resolve(o)
	if err != nil || len(lookups) != 1 || lookups[0] != filepath.Join(o.Cwd, "settings", "owned gashki") || len(warnings) != 2 {
		t.Fatalf("Gashki lookup: %+v %v %v %v", resolved, lookups, warnings, err)
	}
	t.Chdir(o.Cwd)
	t.Setenv("PATH", ".")
	writeConfig(t, filepath.Join(o.Cwd, "ownedtool"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(o.Cwd, "ownedtool"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LookupExecutable("ownedtool"); !errors.Is(err, exec.ErrDot) {
		t.Fatalf("Current-directory lookup refusal lost: %v", err)
	}
	if path, err := LookupExecutable(filepath.Join(o.Cwd, "ownedtool")); err != nil || !filepath.IsAbs(path) {
		t.Fatalf("Explicit executable path: %q %v", path, err)
	}
	t.Log("File and flag bases, symlink context, containment refusal, and active-only lookup passed")
}
func TestACFG06SavedRecordRoundTrip(t *testing.T) {
	o := fixtureOptions(t)
	o.Flags = map[string]Override{"planner": {Value: "codex", Flag: "--planner", Position: 2}, "max_rounds": {Value: 9, Flag: "--max-rounds", Position: 4}, "claude_model": {Value: "", Flag: "--claude-model", Position: 6}}
	before, _, err := Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := before.TOML()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.Cwd, "saved.toml")
	writeConfig(t, path, string(saved))
	o.Flags = nil
	o.NamedFile = path
	o.Defaults = map[string]any{"planner": "claude", "max_rounds": 99, "closing_pass": false, "claude_model": "changed-default"}
	after, _, err := Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Settings, after.Settings) {
		t.Fatalf("Round-trip changed values:\n%+v\n%+v", before.Settings, after.Settings)
	}
	for key, source := range after.Sources {
		if source.Source != "file" || source.Path != path || source.Line < 1 {
			t.Fatalf("Saved source %s: %+v", key, source)
		}
	}
	if after.Settings.ClaudeModel != "" || after.Settings.CodexModel != "" {
		t.Fatal("Unknown model default was invented")
	}
	t.Logf("Saved all %d typed values; new file provenance; unknown model choices remain empty", len(after.Sources))
}
func TestConfigHandlersNoDependencyLaunch(t *testing.T) {
	o := fixtureOptions(t)
	path, _, _ := selectFile(o)
	writeConfig(t, path, "")
	o.Lookup = func(string) (string, error) { t.Fatal("Config handler looked up a dependency"); return "", nil }
	for _, cmd := range []string{"config show", "config get", "config schema", "config validate"} {
		result, err := Handle(context.Background(), CommandRequest{Command: cmd, Key: "planner", Options: o})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := json.Marshal(result.Data); err != nil {
			t.Fatal(err)
		}
		declaration, _ := contract.Load()
		if err := contract.Validate(declaration.Commands[cmd].Schema, result.Data); err != nil {
			t.Fatalf("%s schema: %v", cmd, err)
		}
		if cmd == "config show" {
			if _, err := Parse(path, result.TOML); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestConfigArgumentGrammar(t *testing.T) {
	for _, f := range []struct {
		key, value string
		valid      bool
	}{{"max_rounds", "8", true}, {"max_rounds", "08", false}, {"max_rounds", "", false}, {"persistent", "true", true}, {"persistent", "false", true}, {"persistent", "1", false}, {"planner", "", false}, {"claude_model", "", true}, {"claude_critic_tools", `["Read","Skill"]`, true}, {"claude_critic_tools", `["Read"] []`, false}, {"claude_critic_tools", `[null]`, false}, {"claude_critic_tools", `[]`, false}, {"claude_critic_tools", `["Edit"]`, false}, {"unknown", "x", false}} {
		_, err := ParseArgument(f.key, f.value)
		if (err == nil) != f.valid {
			t.Fatalf("%s %q: %v", f.key, f.value, err)
		}
	}
}

func mustPhysical(t *testing.T, path string) string {
	t.Helper()
	result, err := Physical(path)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
