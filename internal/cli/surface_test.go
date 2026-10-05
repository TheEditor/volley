//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/engine"
	"github.com/TheEditor/volley/internal/ops"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func surfaceOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	o := fixed()
	o.RunOptions = &engine.Options{Env: []string{"HOME=" + root, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + filepath.Join(root, "state"), "PATH=" + root, "TERM=dumb"}, Runner: &noAgent{}}
	return o
}
func surfaceCall(t *testing.T, o Options, args ...string) (contract.Result, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	exit := Execute(context.Background(), append(args, "--json"), &out, &stderr, o)
	var r contract.Result
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(args, out.String(), stderr.String(), err)
	}
	if err := contract.Validate("envelope.json", r); err != nil {
		t.Fatal(args, err, out.String())
	}
	if len(r.Errors) > 0 && !strings.Contains(stderr.String(), r.Errors[0].Message) {
		t.Fatal("stderr error differs")
	}
	if strings.ContainsAny(out.String(), "\x1b\a") {
		t.Fatal("terminal controls in response")
	}
	t.Log("CLI", args, "exit", exit, "hash", r.Meta.DataHash)
	return r, exit
}
func TestACLI03LearningSurface(t *testing.T) {
	o := surfaceOptions(t)
	for _, tc := range []struct {
		args   []string
		schema string
	}{
		{nil, "help"}, {[]string{"--help"}, "help"}, {[]string{"--version"}, "version"}, {[]string{"capabilities"}, "data-capabilities"}, {[]string{"schema"}, "data-schema"}, {[]string{"config", "schema"}, "data-config-schema"}, {[]string{"robot-docs", "guide"}, "data-robot-docs-guide"}, {[]string{"conformance"}, "data-conformance"}, {[]string{"plan", filepath.Join(t.TempDir(), "no-workspace")}, "data-plan"},
	} {
		r, exit := surfaceCall(t, o, tc.args...)
		if exit != 0 {
			t.Fatal(tc.args, r.Errors)
		}
		if err := contract.Validate(tc.schema+".json", r.Data); err != nil {
			t.Fatal(tc.args, err)
		}
		b, _ := json.Marshal(r.Data)
		if tc.schema == "help" {
			var d map[string]any
			json.Unmarshal(b, &d)
			s := d["usage"].(string)
			if strings.Count(s, "\n") > 30 || !strings.Contains(s, "capabilities") {
				t.Fatal(s)
			}
		}
		if tc.schema == "data-robot-docs-guide" {
			for _, token := range []string{"D01", "D02", "D03", "D04", "D05", "D06", "SIGKILL", "--wait-timeout"} {
				if !bytes.Contains(b, []byte(token)) {
					t.Fatal("guide missing", token)
				}
			}
		}
		if tc.schema == "data-capabilities" {
			var d map[string]any
			json.Unmarshal(b, &d)
			registry, _ := contract.Load()
			if len(d["commands"].(map[string]any)) != len(registry.Commands) {
				t.Fatal("handler missing")
			}
		}
	}
	if o.RunOptions.Runner.(*noAgent).calls != 0 {
		t.Fatal("learning invoked process")
	}
	var out, stderr bytes.Buffer
	if exit := Execute(context.Background(), []string{"-h"}, &out, &stderr, o); exit != 0 || strings.Count(out.String(), "\n") > 30 {
		t.Fatal(out.String(), exit)
	}
}
func TestACLI04Diagnosis(t *testing.T) {
	o := surfaceOptions(t)
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"--missing", "runs", "bad"}, "UNKNOWN_FLAG"},
		{[]string{"runs", "bad", "--json=false"}, "INVALID_INPUT"},
		{[]string{"runs", "bad"}, "UNKNOWN_COMMAND"},
		{[]string{"plan", "--persistent=maybe", "--missing"}, "UNKNOWN_FLAG"},
		{[]string{"plan", "--persistent=maybe"}, "INVALID_INPUT"},
		{[]string{"plan", "--max-rounds"}, "MISSING_REQUIRED"},
		{[]string{"plan", "ws", "--max-rounds="}, "INVALID_INPUT"},
		{[]string{"plan", "ws", "--max-rounds=2", "--max-rounds", "3"}, "INVALID_INPUT"},
		{[]string{"-q", "plan", "ws", "--quiet"}, "INVALID_INPUT"},
		{[]string{"plan", "ws", "--planner=claud"}, "INVALID_INPUT"},
		{[]string{"plan", "ws", "--brief=a", "--seed=b"}, "INVALID_INPUT"},
		{[]string{"plan", "ws", "--seed=b", "--brief=a"}, "INVALID_INPUT"},
		{[]string{"config", "show", "--toml"}, "INVALID_INPUT"},
		{[]string{"config", "show", "--deliver=webhook:bad"}, "UNKNOWN_DELIVERY_SCHEME"},
	} {
		r, exit := surfaceCall(t, o, tc.args...)
		if exit != 1 || len(r.Errors) != 1 || r.Errors[0].Code != tc.code {
			t.Fatal(tc.args, exit, r.Errors)
		}
	}
	for _, args := range [][]string{{"--verbose", "plan", "ws", "--persistent=false", "--rubric="}, {"plan", "ws", "--max-rounds", "2", "-v"}} {
		r, exit := surfaceCall(t, o, args...)
		if exit != 0 {
			t.Fatal(args, r.Errors)
		}
	}
	// Literal tokens cannot select rendering or a command.
	reg, _ := contract.Load()
	x, err := Parse([]string{"--", "./status"}, reg)
	if err != nil || x.Command != "run" || x.Positionals[0] != "./status" || MachineMode([]string{"--", "--json"}) {
		t.Fatal(x, err)
	}
	if o.RunOptions.Runner.(*noAgent).calls != 0 {
		t.Fatal("syntax launched process")
	}
}
func TestACLI05NearCommand(t *testing.T) {
	o := surfaceOptions(t)
	// Use a unique sibling whose final component is the near match, without changing cwd.
	// The bare token check is separately observed by the installed command proof.
	reg, _ := contract.Load()
	x, err := Parse([]string{"statu", "--persistent=false"}, reg)
	if err != nil || x.Near != "status" {
		t.Fatal(x, err)
	}
	x, err = Parse([]string{"run", "./status"}, reg)
	if err != nil || x.Near != "" || x.Positionals[0] != "./status" {
		t.Fatal(x, err)
	}
	r, exit := surfaceCall(t, o, "statu", "--missing")
	if exit != 1 || r.Errors[0].Code != "UNKNOWN_FLAG" {
		t.Fatal(r)
	}
	r, exit = surfaceCall(t, o, "statu")
	if exit != 1 || r.Errors[0].Code != "UNKNOWN_COMMAND" || r.Errors[0].DidYouMean == nil || *r.Errors[0].DidYouMean != "status" {
		t.Fatal(r.Errors)
	}
}
func TestACLI06Delivery(t *testing.T) {
	o := surfaceOptions(t)
	root := t.TempDir()
	path := filepath.Join(root, "out.json")
	r, exit := surfaceCall(t, o, "config", "show")
	if exit != 0 {
		t.Fatal(r.Errors)
	}
	expected, _ := contract.Canonical(r.Data)
	expected = append(expected, '\n')
	for _, variant := range []string{"new", "same", "different", "force", "null"} {
		args := []string{"config", "show", "--deliver=file:" + path}
		if variant == "different" {
			os.WriteFile(path, []byte("keep\n"), 0600)
		}
		if variant == "force" {
			args = append(args, "--force")
		}
		if variant == "null" {
			args = []string{"config", "show", "--deliver=null"}
		}
		r, exit = surfaceCall(t, o, args...)
		if variant == "different" {
			if exit != 5 || r.Errors[0].Code != "OUTPUT_CONFLICT" || r.Errors[0].Remediation == nil {
				t.Fatal(r)
			}
			b, _ := os.ReadFile(path)
			if string(b) != "keep\n" {
				t.Fatal("conflict changed output")
			}
			continue
		}
		if exit != 0 {
			t.Fatal(r.Errors)
		}
		b, _ := json.Marshal(r.Data)
		var receipt struct {
			Hash      string `json:"sha256"`
			Bytes     int    `json:"bytes"`
			Duplicate bool   `json:"duplicate"`
		}
		json.Unmarshal(b, &receipt)
		if receipt.Hash != contract.HashBytes(expected) || receipt.Bytes != len(expected) || receipt.Duplicate != (variant == "same") {
			t.Fatal(string(b))
		}
		if variant != "null" {
			b, _ = os.ReadFile(path)
			if !bytes.Equal(b, expected) {
				t.Fatal("wrong sink bytes")
			}
		}
	}
	source := filepath.Join(root, "source.toml")
	os.WriteFile(source, []byte("max_rounds = 3\n"), 0600)
	r, exit = surfaceCall(t, o, "config", "show", "--config", source, "--toml", "--deliver=file:"+filepath.Join(root, "out.toml"))
	if exit != 0 {
		t.Fatal(r.Errors)
	}
	for _, target := range []string{source, filepath.Join(root, "state/manifest.json")} {
		os.MkdirAll(filepath.Dir(target), 0700)
		r, exit = surfaceCall(t, o, "config", "show", "--config", source, "--deliver=file:"+target, "--force")
		if exit != 5 || r.Errors[0].Code != "OUTPUT_CONFLICT" {
			t.Fatal(r.Errors)
		}
	}
	r, exit = surfaceCall(t, o, "config", "show", "--deliver=webhook:"+filepath.Join(root, "no-write"))
	if exit != 1 || r.Errors[0].Code != "UNKNOWN_DELIVERY_SCHEME" {
		t.Fatal(r.Errors)
	}
	if _, err := os.Stat(filepath.Join(root, "no-write")); !os.IsNotExist(err) {
		t.Fatal("unknown scheme wrote")
	}
}
func TestACLI07FeedbackAndQuotes(t *testing.T) {
	o := surfaceOptions(t)
	for _, text := range []string{"exact text", "exact text", "changed text"} {
		r, exit := surfaceCall(t, o, "feedback", text, "--idempotency-key=one")
		if text == "changed text" {
			if exit != 5 || r.Errors[0].Code != "IDEMPOTENCY_CONFLICT" {
				t.Fatal(r)
			}
		} else if exit != 0 {
			t.Fatal(r.Errors)
		}
	}
	workspace := filepath.Join(t.TempDir(), "has spaces; $(touch forbidden) ' quoted")
	r, exit := surfaceCall(t, o, "plan", workspace)
	if exit != 0 {
		t.Fatal(r.Errors)
	}
	b, _ := json.Marshal(r.Data)
	var d map[string]any
	json.Unmarshal(b, &d)
	got := d["recommended_action"].(map[string]any)["command"]
	if got != ops.Command("volley", "run", workspace) {
		t.Fatal(got)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("plan created workspace")
	}
	if o.RunOptions.Runner.(*noAgent).calls != 0 {
		t.Fatal("feedback or display called process")
	}
}

func TestConfigMutationSurface(t *testing.T) {
	o := surfaceOptions(t)
	path := filepath.Join(t.TempDir(), "settings.toml")
	os.WriteFile(path, []byte("# preserve\nmax_rounds = 3\n"), 0600)
	r, exit := surfaceCall(t, o, "config", "set", "max_rounds", "4", "--config", path)
	if exit != 0 {
		t.Fatal(r.Errors)
	}
	o.Input = strings.NewReader(`{"max_rounds":5,"persistent":false}`)
	r, exit = surfaceCall(t, o, "config", "patch", "--config", path, "--from-stdin")
	if exit != 0 {
		t.Fatal(r.Errors)
	}
	r, exit = surfaceCall(t, o, "config", "get", "max_rounds", "--config", path)
	if exit != 0 {
		t.Fatal(r.Errors)
	}
	b, _ := json.Marshal(r.Data)
	var data map[string]any
	json.Unmarshal(b, &data)
	if data["value"] != float64(5) {
		t.Fatal(data)
	}
	b, _ = os.ReadFile(path)
	if !bytes.Contains(b, []byte("# preserve")) {
		t.Fatal("comment lost")
	}
	terminal := false
	o.Terminal = &terminal
	r, exit = surfaceCall(t, o, "config", "edit", "--config", path)
	if exit == 0 || len(r.Errors) != 1 {
		t.Fatal("editor guard absent", r)
	}
	if o.RunOptions.Runner.(*noAgent).calls != 0 {
		t.Fatal("nonterminal editor launched")
	}
}
