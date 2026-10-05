// Package ownedagent supplies the deterministic local provider fixture.
package ownedagent

import (
	"encoding/json"
	"fmt"
	"github.com/TheEditor/volley/internal/contract"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Plan struct {
	MalformedCodex    bool
	BlankPlannerReply bool     `json:"blank_planner_reply"`
	MissingSpec       bool     `json:"missing_spec"`
	Advisory          string   `json:"advisory"`
	ClosingUnchanged  bool     `json:"closing_unchanged"`
	Critiques         []string `json:"critiques"`
	QuestionPurpose   string   `json:"question_purpose"`
	MutationPath      string   `json:"mutation_path"`
	MutationRole      string   `json:"mutation_role"`
	MutationPurpose   string   `json:"mutation_purpose"`
	Fail              bool     `json:"fail"`
	Hold              bool     `json:"hold"`
}

func Run() int {
	start := -1
	for i, arg := range os.Args {
		if arg == "--engine-child" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return -1
	}
	provider := os.Args[start]
	args := os.Args[start+1:]
	if len(args) == 1 && args[0] == "--version" {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stdout, "owned engine stub 1.0")
		return 0
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil || len(stdin) > 0 {
		return 91
	}
	cwd, _ := os.Getwd()
	b, err := os.ReadFile(filepath.Join(cwd, "state/manifest.json"))
	if err != nil {
		return 92
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return 93
	}
	turn := m["current_turn"].(map[string]any)
	id := turn["id"].(string)
	role := turn["role"].(string)
	purpose := turn["purpose"].(string)
	promptBytes, err := os.ReadFile(filepath.Join(cwd, turn["prompt_path"].(string)))
	if err != nil {
		return 94
	}
	capture := map[string]any{"provider": provider, "argv": args, "stdin_bytes": len(stdin), "turn": turn, "prompt": string(promptBytes)}
	captureBytes, _ := json.Marshal(capture)
	if os.WriteFile(filepath.Join(os.Getenv("F_ENGINE_CAPTURE"), id+".json"), captureBytes, 0600) != nil {
		return 95
	}
	counter := filepath.Join(os.Getenv("F_ENGINE_CAPTURE"), "launches.txt")
	prior, _ := os.ReadFile(counter)
	f, err := os.OpenFile(counter, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 96
	}
	fmt.Fprintf(f, "%s %s %s\n", provider, role, purpose)
	f.Close()
	var plan Plan
	b, err = os.ReadFile(os.Getenv("F_ENGINE_PLAN"))
	if err != nil || json.Unmarshal(b, &plan) != nil {
		return 97
	}
	time.Sleep(100 * time.Millisecond)
	if plan.Hold {
		time.Sleep(15 * time.Second)
	}
	if plan.MutationRole == role && (plan.MutationPurpose == "" || plan.MutationPurpose == purpose) {
		path := plan.MutationPath
		if path == "@stdout" {
			path = "state/turns/" + id + "/stdout.pending"
		}
		forged := "forged by owned stub\n"
		if plan.MutationPath == "@stdout" {
			forged = strings.Repeat(forged, 100)
		}
		if os.WriteFile(filepath.Join(cwd, path), []byte(forged), 0600) != nil {
			return 98
		}
	}
	if plan.Fail && (plan.MutationRole == "" || plan.MutationRole == role) && (plan.MutationPurpose == "" || plan.MutationPurpose == purpose) {
		return 23
	}
	reply := "Owned planner reply.\n"
	if role == "planner" {
		if !(purpose == "closing" && plan.ClosingUnchanged) && os.WriteFile(filepath.Join(cwd, "SPEC.md"), []byte("# SPEC\nChecked "+purpose+" result.\n"), 0600) != nil {
			return 99
		}
		if plan.QuestionPurpose == purpose {
			_ = os.WriteFile(filepath.Join(cwd, "QUESTIONS.md"), []byte("1. Choose A or B. Recommendation: A.\n"), 0600)
		} else {
			_ = os.Remove(filepath.Join(cwd, "QUESTIONS.md"))
		}
	} else {
		index := 0
		for _, line := range strings.Split(string(prior), "\n") {
			if strings.Contains(line, " critic ") && !strings.HasSuffix(line, " advisory") {
				index++
			}
		}
		reply = "Review\nVERDICT: APPROVE\n"
		if index < len(plan.Critiques) {
			reply = plan.Critiques[index]
		}
		if purpose == "advisory" && plan.Advisory != "" {
			reply = plan.Advisory
		}
	}
	if role == "planner" {
		if plan.BlankPlannerReply {
			reply = ""
		}
		if plan.MissingSpec {
			os.Remove(filepath.Join(cwd, "SPEC.md"))
		}
	}
	if provider == "codex" {
		hexID := contract.HashBytes([]byte(id))
		session := hexID[:8] + "-" + hexID[8:12] + "-4" + hexID[13:16] + "-8" + hexID[17:20] + "-" + hexID[20:32]
		if len(args) > 2 && args[1] == "resume" {
			session = args[2]
		}
		fmt.Fprintf(os.Stdout, "{\"type\":\"thread.started\",\"thread_id\":%s}\n", strconv.Quote(session))
		for i, arg := range args {
			if arg == "--output-last-message" && i+1 < len(args) {
				_ = os.WriteFile(args[i+1], []byte(reply), 0600)
			}
		}
		if plan.MalformedCodex {
			fmt.Fprintln(os.Stdout, "{malformed owned event")
		} else {
			fmt.Fprintln(os.Stdout, "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
		}
	} else {
		fmt.Fprint(os.Stdout, reply)
	}
	return 0
}
