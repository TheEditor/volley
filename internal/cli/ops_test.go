//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/TheEditor/volley/internal/engine"
)

func TestAOPSCommandGrammar(t *testing.T) {
	root := t.TempDir()
	index := filepath.Join(root, "state")
	opts := Options{RunOptions: &engine.Options{Env: []string{"HOME=" + root, "XDG_STATE_HOME=" + index, "XDG_CONFIG_HOME=" + filepath.Join(root, "config")}}}
	for _, args := range [][]string{{"runs", "list", "--limit=0"}, {"runs", "list", "--limit=101"}, {"runs", "list", "--limit=1.5"}, {"runs", "list", "--limit"}, {"runs", "list", "--limit=1", "--limit=2"}, {"runs", "list", "--fields=unknown"}, {"runs", "list", "--fields="}, {"runs", "list", "extra"}, {"runs", "events", "absent", "--wait-timeout=-1s"}, {"runs", "events", "absent", "--wait-timeout=bad"}, {"runs", "prune", "--older-than=0s", "--yes"}, {"runs", "prune", "--older-than=1h", "--yes=true"}, {"status", "absent", "--probe=true"}, {"runs", "get"}} {
		var out, stderr bytes.Buffer
		exit := Execute(context.Background(), append(args, "--json"), &out, &stderr, opts)
		var value map[string]any
		if err := json.Unmarshal(out.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if exit != 1 || value["ok"] != false {
			t.Fatal(args, exit, out.String(), stderr.String())
		}
	}
	for _, args := range [][]string{{"runs", "list"}, {"runs", "list", "--limit=100", "--fields=workspace,status"}, {"doctor"}, {"runs", "prune", "--older-than=1h", "--dry-run"}} {
		var out, stderr bytes.Buffer
		exit := Execute(context.Background(), append(args, "--json"), &out, &stderr, opts)
		if exit != 0 {
			t.Fatal(args, exit, out.String(), stderr.String())
		}
		var v map[string]any
		json.Unmarshal(out.Bytes(), &v)
		if v["ok"] != true {
			t.Fatal(v)
		}
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatal("read-only commands created an index", err)
	}
}
