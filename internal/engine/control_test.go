//go:build darwin || linux

package engine

import (
	"context"
	"errors"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/ops"
	"github.com/TheEditor/volley/internal/store"
	"testing"
	"time"
)

func TestAOPS04EngineStop(t *testing.T) {
	f := newFixture(t, "claude", true, true, AgentPlan{Hold: true})
	result := make(chan error, 1)
	go func() { _, err := Run(context.Background(), f.Request, f.Options); result <- err }()
	end := time.Now().Add(3 * time.Second)
	for len(f.calls()) == 0 {
		if time.Now().After(end) {
			t.Fatal("no owned agent started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	d, err := (ops.Options{}).Handle(context.Background(), ops.Request{Command: "runs stop", Selector: f.Workspace, Yes: true})
	if err != nil || d["status"] != "stopped" {
		t.Fatal(d, err)
	}
	requireCode(t, <-result, "CONTROLLER_STOPPED")
	if len(f.calls()) != 1 || f.manifest(t).Object("stop_control")["applied"] != true {
		t.Fatal("stop repeated agent or lacked acknowledgment")
	}
	before := len(f.calls())
	f.Request.Resume = true
	f.Request.Resolved = nil
	_, err = f.run(t)
	requireCode(t, err, "CONFIG_CONFLICT")
	if len(f.calls()) != before {
		t.Fatal("stopped run launched another agent")
	}
	b, _ := contract.Canonical(f.manifest(t))
	t.Logf("A-OPS-04 artifact_sha256=%s owned_agent_launches=%d settings=%+v", contract.HashBytes(b), len(f.calls()), f.Settings)
}

func TestAOPS01IndexFailureDoesNotBlock(t *testing.T) {
	f := newFixture(t, "claude", false, true, AgentPlan{})
	f.Options.Register = func(context.Context, store.Snapshot) error { return errors.New("owned index unavailable") }
	d, err := f.run(t)
	if err != nil || d["status"] != "approved" {
		t.Fatal(d, err)
	}
	if len(d["warnings"].([]string)) != 1 || len(f.calls()) != 1 {
		t.Fatal(d, f.calls())
	}
	d, err = (ops.Options{}).Handle(context.Background(), ops.Request{Command: "doctor", Selector: f.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	if d["settings"].(map[string]any)["closing_pass"] != false {
		t.Fatal("doctor replaced frozen settings with ambient defaults", d)
	}
	b, _ := contract.Canonical(f.manifest(t))
	t.Logf("A-OPS-01 artifact_sha256=%s owned_agent_launches=%d settings=%+v", contract.HashBytes(b), len(f.calls()), f.Settings)
}
