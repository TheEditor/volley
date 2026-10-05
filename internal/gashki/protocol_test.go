package gashki

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
)

func document(t *testing.T, v any) map[string]any {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	x, e := parse(b)
	if e != nil {
		t.Fatal(e)
	}
	return Map(x)
}
func envelope(t *testing.T, data any, code string) []byte {
	t.Helper()
	p, e := LoadProtocol()
	if e != nil {
		t.Fatal(e)
	}
	value := document(t, map[string]any{"data": data})["data"]
	b, e := PayloadCanonical(value)
	if e != nil {
		t.Fatal(e)
	}
	errors := []any{}
	if code != "" {
		decl := Map(Map(p.Capabilities["error_codes"])[code])
		errors = append(errors, map[string]any{"code": code, "message": "fixture", "path": nil, "did_you_mean": nil, "remediation": nil, "exit_code": decl["exit_code"], "retryable": decl["retryable"], "evidence": nil})
	}
	m := map[string]any{"ok": code == "", "tool_version": "fixture", "data": value, "meta": map[string]any{"request_id": "01234567-1234-7123-8123-0123456789ab", "ts_iso": "2026-10-04T00:00:00.000Z", "elapsed_ms": 0, "contract_version": "2", "data_hash": "sha256:" + contract.HashBytes(b)}, "errors": errors, "warnings": []any{}, "commands": []any{}}
	b, e = json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func rewrite(t *testing.T, b []byte, change func(map[string]any)) []byte {
	t.Helper()
	v := document(t, json.RawMessage(b))
	change(v)
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestPublicCanonicalRFCVectors(t *testing.T) {
	for _, v := range []struct {
		bits uint64
		want string
	}{{0, "0"}, {0x8000000000000000, "0"}, {1, "5e-324"}, {0x7fefffffffffffff, "1.7976931348623157e+308"}, {0x4340000000000000, "9007199254740992"}, {0x44b52d02c7e14af5, "9.999999999999997e+22"}, {0x44b52d02c7e14af6, "1e+23"}, {0x44b52d02c7e14af7, "1.0000000000000001e+23"}, {0x444b1ae4d6e2ef4e, "999999999999999700000"}} {
		t.Run(v.want, func(t *testing.T) {
			b, e := PayloadCanonical(math.Float64frombits(v.bits))
			if e != nil || string(b) != v.want {
				t.Fatal(string(b), e)
			}
		})
	}
	b, e := PayloadCanonical(map[string]any{"\ue000": json.Number("-0"), "😀": json.Number("1e-7"), "\r": "\u2028<>&\x00"})
	if e != nil || string(b) != "{\"\\r\":\"\u2028<>&\\u0000\",\"😀\":1e-7,\"\ue000\":0}" {
		t.Fatal(string(b), e)
	}
	for _, v := range []any{math.Inf(1), math.NaN(), json.Number("1e1000"), make(chan int), string([]byte{0xff})} {
		if _, e := PayloadCanonical(v); e == nil {
			t.Fatalf("accepted %T", v)
		}
	}
	for _, b := range []string{`"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\ud800"`} {
		if _, e := parse([]byte(b)); e == nil {
			t.Fatal("accepted lone surrogate", b)
		}
	}
	for _, b := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `"valid"`} {
		if _, e := parse([]byte(b)); e != nil {
			t.Fatal(b, e)
		}
	}
}

func TestCheckedProtocolMalformedEvidence(t *testing.T) {
	p, e := LoadProtocol()
	if e != nil {
		t.Fatal(e)
	}
	base := envelope(t, map[string]any{"id": "01234567-1234-7123-8123-0123456789ab", "name": "group/planner", "agent": "claude", "target": "%2", "existing": false}, "")
	if _, e := p.Check("spawn", base, 0); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(map[string]any){
		"version":     func(m map[string]any) { Map(m["meta"])["contract_version"] = "3" },
		"hash":        func(m map[string]any) { Map(m["meta"])["data_hash"] = "sha256:" + strings.Repeat("0", 64) },
		"missingUUID": func(m map[string]any) { delete(Map(m["data"]), "id") },
		"unknownWarning": func(m map[string]any) {
			m["warnings"] = []any{map[string]any{"code": "NEW_UNKNOWN", "message": "fixture", "details": nil}}
		},
		"successErrors": func(m map[string]any) { m["ok"] = false },
		"extraEnvelope": func(m map[string]any) { m["extra"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := p.Check("spawn", rewrite(t, base, change), 0); e == nil {
				t.Fatal("accepted malformed evidence")
			}
		})
	}
	for _, b := range [][]byte{append(append([]byte{}, base...), base...), bytes.Replace(base, []byte(`"ok":true`), []byte(`"ok":true,"ok":true`), 1), []byte{0xff}, bytes.Repeat([]byte(" "), ResponseLimit+1)} {
		if _, e := p.Check("spawn", b, 0); e == nil {
			t.Fatal("accepted malformed raw JSON")
		}
	}
	if _, e := p.Check("spawn", base, 3); e == nil {
		t.Fatal("success exit mismatch")
	}
	failed := envelope(t, nil, "NOT_SAFE_TO_SEND")
	if _, e := p.Check("send", failed, 4); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Check("spawn", failed, 4); e == nil {
		t.Fatal("wrong-verb code")
	}
	if e := p.RequiredSubset(p.Capabilities, p.SchemaData); e != nil {
		t.Fatal(e)
	}
	wrong := document(t, p.Capabilities)
	Map(Map(wrong["verbs"])["spawn"])["flags"] = []any{}
	if e := p.RequiredSubset(wrong, p.SchemaData); e == nil {
		t.Fatal("missing required flags")
	}
}

func TestAGK11FixedBudgets(t *testing.T) {
	now := time.Unix(1, 0)
	for _, v := range []struct {
		name                 string
		limit, elapsed, want time.Duration
		fail                 bool
	}{{"500ms", 500 * time.Millisecond, 0, 0, true}, {"exact1s", time.Second, 0, time.Second, false}, {"residual", 2 * time.Second, 1500 * time.Millisecond, 0, true}, {"above24h", 25 * time.Hour, 0, 24 * time.Hour, false}, {"unlimited", 0, 30 * time.Hour, 24 * time.Hour, false}} {
		t.Run(v.name, func(t *testing.T) {
			b := Budget{Limit: v.limit, ElapsedBefore: v.elapsed, Started: now, Now: func() time.Time { return now }}
			chunk, e := b.WaitChunk()
			if (e != nil) != v.fail || chunk != v.want {
				t.Fatal(chunk, e)
			}
			now = now.Add(250 * time.Millisecond)
			if b.Elapsed() != v.elapsed+250*time.Millisecond {
				t.Fatal("budget reset")
			}
		})
	}
}

func TestKnownWarningsRetainTheirEvidence(t *testing.T) {
	p, e := LoadProtocol()
	if e != nil {
		t.Fatal(e)
	}
	base := envelope(t, map[string]any{"items": []any{}, "recommended_action": nil}, "")
	for _, code := range []string{"HOOK_SCREEN_DISAGREE", "HOOK_STREAM_GAP", "PANE_LABEL_UNAVAILABLE", "EVENT_LOG_LARGE", "ENV_SETTING_IGNORED", "DEPRECATED"} {
		t.Run(code, func(t *testing.T) {
			raw := rewrite(t, base, func(m map[string]any) {
				m["warnings"] = []any{map[string]any{"code": code, "message": "fixture warning", "details": map[string]any{"retained": true}}}
			})
			response, e := p.Check("status", raw, 0)
			if e != nil || len(response.Warnings) != 1 || response.Warnings[0].Code != code || Map(response.Warnings[0].Details)["retained"] != true {
				t.Fatal(response, e)
			}
		})
	}
}
