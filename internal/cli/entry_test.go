package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/TheEditor/volley/internal/contract"
	"strings"
	"testing"
	"time"
)

func fixed() Options {
	return Options{Now: func() time.Time { return time.Unix(100, 0) }, RequestID: func() (string, error) { return "fixture", nil }}
}
func TestMachineBootstrap(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{[]string{"--json"}, true}, {[]string{"--json=false"}, true}, {[]string{"--", "--json"}, false}, {[]string{"--", "--json=false"}, false}, {[]string{"--help"}, false},
	} {
		if got := MachineMode(test.args); got != test.want {
			t.Fatalf("%q got %v", test.args, got)
		}
	}
}
func TestAARCH01HandlerFaults(t *testing.T) {
	r, err := contract.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range append([]string{"entry"}, r.DiagnosisOrder...) {
		t.Run(stage, func(t *testing.T) {
			var out, stderr bytes.Buffer
			opts := fixed()
			if stage == "entry" {
				opts.Entry = func() { panic("private panic details") }
			} else {
				opts.Stage = func(s string) {
					if s == stage {
						panic("private panic details")
					}
				}
			}
			exit := Execute(context.Background(), []string{"--json"}, &out, &stderr, opts)
			var result contract.Result
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("not an envelope: %s", out.String())
			}
			if exit != 6 || result.OK || len(result.Errors) != 1 || result.Errors[0].Code != "INTERNAL" {
				t.Fatalf("exit=%d result=%+v", exit, result)
			}
			if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "goroutine") {
				t.Fatal("panic leaked")
			}
			if !strings.Contains(stderr.String(), "INTERNAL") {
				t.Fatal("missing stderr message")
			}
		})
	}
}
func TestCapabilitiesOnlyWorkingHandlers(t *testing.T) {
	var out, stderr bytes.Buffer
	if Execute(context.Background(), []string{"capabilities", "--json"}, &out, &stderr, fixed()) != 0 {
		t.Fatal(stderr.String())
	}
	var result struct {
		Data struct {
			Commands map[string]any `json:"commands"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Commands) != 7 || result.Data.Commands["run"] == nil || result.Data.Commands["runs stop"] != nil {
		t.Fatal("unimplemented handler advertised")
	}
}
