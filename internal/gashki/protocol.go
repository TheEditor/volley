package gashki

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const SourcePin = "8eaecc9b31c965bab6c63a6a5562ebf38c43e363"
const ResponseLimit = 32 << 20

//go:embed assets/*.json
var publicAssets embed.FS

type Protocol struct {
	Schemas      map[string]*jsonschema.Schema
	Capabilities map[string]any
	SchemaData   map[string]any
	Hash         string
}
type UpstreamError struct {
	Code        string  `json:"code"`
	Message     string  `json:"message"`
	Path        *string `json:"path"`
	DidYouMean  *string `json:"did_you_mean"`
	Remediation *string `json:"remediation"`
	Exit        int     `json:"exit_code"`
	Retryable   *bool   `json:"retryable"`
	Evidence    any     `json:"evidence"`
}
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details"`
}
type Response struct {
	OK       bool            `json:"ok"`
	Version  string          `json:"tool_version"`
	Data     any             `json:"data"`
	Meta     map[string]any  `json:"meta"`
	Warnings []Warning       `json:"warnings"`
	Commands []string        `json:"commands"`
	Errors   []UpstreamError `json:"errors"`
}

func Map(v any) map[string]any { m, _ := v.(map[string]any); return m }
func String(v any) string      { s, _ := v.(string); return s }
func Integer(v any) int64      { n, _ := v.(json.Number); i, _ := n.Int64(); return i }
func parse(b []byte) (any, error) {
	if err := checkedSurrogates(b); err != nil {
		return nil, err
	}
	return input.Record(bytes.NewReader(b), ResponseLimit)
}

type noNetwork struct{}

func (noNetwork) Load(url string) (any, error) {
	return nil, fmt.Errorf("Unregistered public schema resource %q", url)
}

func LoadProtocol() (*Protocol, error) {
	p := &Protocol{Schemas: map[string]*jsonschema.Schema{}}
	b, e := publicAssets.ReadFile("assets/capabilities.json")
	if e != nil {
		return nil, e
	}
	v, e := parse(b)
	if e != nil {
		return nil, e
	}
	p.Capabilities = Map(v)
	s, e := publicAssets.ReadFile("assets/schemas.json")
	if e != nil {
		return nil, e
	}
	v, e = parse(s)
	if e != nil {
		return nil, e
	}
	p.SchemaData = Map(v)
	p.Hash = contract.HashBytes(append(append([]byte{}, b...), s...))
	c := jsonschema.NewCompiler()
	c.UseLoader(noNetwork{})
	add := func(v any) error { return c.AddResource(String(Map(v)["$id"]), v) }
	for _, v := range Map(p.SchemaData["definitions"]) {
		if e := add(v); e != nil {
			return nil, e
		}
	}
	if e := add(p.SchemaData["envelope_schema"]); e != nil {
		return nil, e
	}
	for _, v := range Map(p.SchemaData["schemas"]) {
		if e := add(v); e != nil {
			return nil, e
		}
	}
	for name, v := range Map(p.SchemaData["schemas"]) {
		compiled, e := c.Compile(String(Map(v)["$id"]))
		if e != nil {
			return nil, e
		}
		p.Schemas[name] = compiled
	}
	compiled, e := c.Compile(String(Map(p.SchemaData["envelope_schema"])["$id"]))
	if e != nil {
		return nil, e
	}
	p.Schemas["envelope"] = compiled
	return p, nil
}

func (p *Protocol) Check(verb string, b []byte, exit int) (Response, error) {
	var response Response
	v, e := parse(b)
	if e != nil {
		return response, e
	}
	if e := p.Schemas["envelope"].Validate(v); e != nil {
		return response, e
	}
	m := Map(v)
	meta := Map(m["meta"])
	if meta["contract_version"] != "2" {
		return response, fmt.Errorf("Unsupported Gashki contract")
	}
	h, e := PayloadCanonical(m["data"])
	if e != nil {
		return response, e
	}
	if meta["data_hash"] != "sha256:"+contract.HashBytes(h) {
		return response, fmt.Errorf("Gashki payload hash does not match")
	}
	if e := json.Unmarshal(b, &response); e != nil {
		return response, e
	}
	// Restore json.Number and nullable values in data and error evidence.
	response.Data = m["data"]
	response.Meta = meta
	for i, v := range m["errors"].([]any) {
		response.Errors[i].Evidence = Map(v)["evidence"]
	}
	for i, v := range m["warnings"].([]any) {
		response.Warnings[i].Details = Map(v)["details"]
	}
	entry := p.Verb(verb)
	if entry == nil && verb != "version" {
		return response, fmt.Errorf("Unsupported Gashki operation")
	}
	registry := Map(p.Capabilities["error_codes"])
	for i, err := range response.Errors {
		decl := Map(registry[err.Code])
		if decl == nil || Integer(decl["exit_code"]) != int64(err.Exit) || !reflect.DeepEqual(decl["retryable"], Map(m["errors"].([]any)[i])["retryable"]) || !listed(decl["appears_in"], "errors") {
			return response, fmt.Errorf("Unknown or inconsistent upstream error %q", err.Code)
		}
		if !listed(entry["exit_codes"], json.Number(fmt.Sprint(err.Exit))) {
			return response, fmt.Errorf("Wrong-verb upstream error %q", err.Code)
		}
		if !sourceVerbAllows(err.Code, verb) {
			return response, fmt.Errorf("Wrong-verb upstream error %q", err.Code)
		}
	}
	for _, w := range response.Warnings {
		if !listed(Map(registry[w.Code])["appears_in"], "warnings") {
			return response, fmt.Errorf("Unknown upstream warning %q", w.Code)
		}
	}
	if response.OK {
		if exit != 0 || len(response.Errors) != 0 {
			return response, fmt.Errorf("Success envelope and process exit disagree")
		}
		if verb == "version" {
			if String(Map(response.Data)["contract_version"]) != "2" {
				return response, fmt.Errorf("Version facts disagree")
			}
			return response, nil
		}
		schema := p.Schemas[verb]
		if schema == nil {
			return response, fmt.Errorf("Required schema is absent")
		}
		if e := schema.Validate(response.Data); e != nil {
			return response, e
		}
	} else if exit <= 0 || len(response.Errors) == 0 || response.Errors[0].Exit != exit {
		return response, fmt.Errorf("Failure envelope and process exit disagree")
	}
	return response, nil
}

// The public registry lists exits, not verb membership. This table is pinned
// to the public verb implementations at SourcePin. Parser/envelope failures
// can arise before dispatch; operation-specific outcomes cannot.
func sourceVerbAllows(code, verb string) bool {
	switch code {
	case "UNKNOWN_FLAG", "UNKNOWN_COMMAND", "INVALID_INPUT", "MISSING_REQUIRED", "INVALID_CONFIG", "INTERNAL", "DEPRECATED":
		return true
	case "HERE_UNAVAILABLE", "TMUX_SPLIT_FAILED", "AGENT_CLI_MISSING", "CODEX_HOOKS_UNTRUSTED", "LAUNCH_PROMPT_UNHANDLED", "ENV_NOT_SET", "CONFLICT", "ENV_SETTING_IGNORED":
		return verb == "spawn"
	case "AGENT_NOT_LOGGED_IN":
		return verb == "wait"
	case "AGENT_UNVERIFIABLE", "COMPOSER_NOT_EMPTY", "NOT_SAFE_TO_SEND", "SEND_STUCK_IN_COMPOSER", "SEND_UNCONFIRMED", "SEND_INTERRUPTED", "SEND_INPUT_MIXED", "SUBMIT_UNVERIFIED":
		return verb == "send"
	case "APPROVAL_REQUIRED", "TURN_FAILED", "WAIT_TIMEOUT":
		return verb == "wait"
	case "IDEMPOTENCY_CONFLICT", "LOCKED":
		return verb == "send" || verb == "kill"
	case "PANE_DEAD":
		return verb == "send" || verb == "wait"
	case "NOT_FOUND":
		return verb == "send" || verb == "wait" || verb == "observe" || verb == "kill"
	case "TMUX_NO_SERVER", "STATE_DIR_UNWRITABLE", "PANE_LABEL_UNAVAILABLE", "EVENT_LOG_LARGE":
		return verb == "spawn" || verb == "send" || verb == "wait" || verb == "observe" || verb == "status" || verb == "kill"
	case "HOOK_SCREEN_DISAGREE", "HOOK_STREAM_GAP":
		return verb == "send" || verb == "wait" || verb == "observe" || verb == "status"
	default:
		return false
	}
}

func listed(v, want any) bool {
	a, _ := v.([]any)
	for _, x := range a {
		if reflect.DeepEqual(x, want) {
			return true
		}
	}
	return false
}
func MapArray(v any, code string) map[string]any {
	a, _ := v.([]any)
	for _, x := range a {
		if Map(x)["code"] == code {
			return Map(x)
		}
	}
	return nil
}
func (p *Protocol) Verb(name string) map[string]any {
	parts := strings.Split(name, " ")
	v := Map(Map(p.Capabilities["verbs"])[parts[0]])
	if len(parts) == 2 {
		v = Map(Map(v["subcommands"])[parts[1]])
	}
	return v
}

// RequiredSubset permits unrelated additions while refusing changes to the
// pinned operations that the adapter executes.
func (p *Protocol) RequiredSubset(capabilities, schemaData any) error {
	caps := Map(capabilities)
	if caps["tool_name"] != "gashki" || caps["contract_version"] != "2" {
		return fmt.Errorf("Required Gashki contract is absent")
	}
	actual := &Protocol{Capabilities: caps}
	for code, want := range Map(p.Capabilities["error_codes"]) {
		if !reflect.DeepEqual(want, Map(caps["error_codes"])[code]) {
			return fmt.Errorf("Pinned Gashki outcome %q changed", code)
		}
	}
	for _, name := range []string{"config show", "config get", "spawn", "send", "wait", "observe", "status", "kill"} {
		want, got := p.Verb(name), actual.Verb(name)
		if got == nil {
			return fmt.Errorf("Required Gashki verb %q is absent", name)
		}
		for _, key := range []string{"json", "args", "flags", "exit_codes", "output_modes"} {
			if !reflect.DeepEqual(want[key], got[key]) {
				return fmt.Errorf("Required Gashki verb %q changed %s", name, key)
			}
		}
		if !reflect.DeepEqual(Map(p.SchemaData["schemas"])[name], Map(Map(schemaData)["schemas"])[name]) {
			return fmt.Errorf("Required Gashki schema %q changed", name)
		}
	}
	if !reflect.DeepEqual(p.Capabilities["global_flags"], caps["global_flags"]) || !reflect.DeepEqual(p.Capabilities["data_hash"], caps["data_hash"]) || !reflect.DeepEqual(p.SchemaData["envelope_schema"], Map(schemaData)["envelope_schema"]) || !reflect.DeepEqual(p.SchemaData["definitions"], Map(schemaData)["definitions"]) {
		return fmt.Errorf("Required Gashki envelope or flag contract changed")
	}
	return nil
}
