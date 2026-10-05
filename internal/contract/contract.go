// Package contract owns declarations and result types. It does no execution.
package contract

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed registry.json schemas/*.json
var assets embed.FS

type Flag struct {
	Name       string   `json:"name"`
	Arity      int      `json:"arity"`
	Type       string   `json:"type"`
	Default    any      `json:"default"`
	AllowEmpty bool     `json:"allow_empty"`
	Repeatable bool     `json:"repeatable"`
	Aliases    []string `json:"aliases"`
	Forms      []string `json:"forms"`
	Scope      string   `json:"scope"`
	Enum       []string `json:"enum,omitempty"`
	Setting    string   `json:"setting,omitempty"`
	ParserPath string   `json:"parser_path"`
}
type Positional struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`
	Type       string `json:"type"`
	AllowEmpty bool   `json:"allow_empty"`
}
type Command struct {
	Path          string           `json:"path"`
	ParserPath    string           `json:"parser_path"`
	HandlerPath   string           `json:"handler_path"`
	Advertisement string           `json:"advertisement"`
	Positionals   []Positional     `json:"positionals"`
	Flags         []Flag           `json:"flags"`
	SettingFlags  bool             `json:"setting_flags"`
	OutputModes   []string         `json:"output_modes"`
	Schema        string           `json:"schema"`
	ReadOnly      bool             `json:"read_only"`
	Stdin         *string          `json:"stdin"`
	Constraints   []map[string]any `json:"constraints"`
}
type Setting struct {
	Key        string         `json:"key"`
	Type       string         `json:"type"`
	Default    any            `json:"default"`
	Flag       string         `json:"flag"`
	Order      int            `json:"order"`
	AllowEmpty bool           `json:"allow_empty"`
	Validation map[string]any `json:"validation"`
}
type Code struct {
	Code          string   `json:"code"`
	Exit          int      `json:"exit"`
	Retryable     *bool    `json:"retryable"`
	Stage         string   `json:"stage"`
	AllowedStages []string `json:"allowed_stages"`
	Summary       string   `json:"summary"`
}
type Registry struct {
	ContractVersion string             `json:"contract_version"`
	RecordVersion   int                `json:"record_version"`
	ToolVersion     string             `json:"tool_version"`
	DiagnosisOrder  []string           `json:"diagnosis_order"`
	GlobalFlags     []Flag             `json:"global_flags"`
	Commands        map[string]Command `json:"commands"`
	Settings        []Setting          `json:"settings"`
	Codes           []Code             `json:"error_codes"`
}

func Load() (*Registry, error) {
	b, err := assets.ReadFile("registry.json")
	if err != nil {
		return nil, err
	}
	var r Registry
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err = d.Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}
func RawRegistry() (map[string]any, error) {
	b, err := assets.ReadFile("registry.json")
	if err != nil {
		return nil, err
	}
	var v map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	err = d.Decode(&v)
	return v, err
}
func Schema(name string) ([]byte, error) { return assets.ReadFile("schemas/" + name + ".json") }
func (r *Registry) Find(code string) (Code, bool) {
	for _, c := range r.Codes {
		if c.Code == code {
			return c, true
		}
	}
	return Code{}, false
}

type Error struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Exit        int            `json:"exit_code"`
	Retryable   *bool          `json:"retryable"`
	Stage       string         `json:"stage"`
	Path        *string        `json:"path"`
	DidYouMean  *string        `json:"did_you_mean"`
	Remediation *string        `json:"remediation"`
	Evidence    map[string]any `json:"evidence"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func (r *Registry) Error(code, message string) *Error {
	c, ok := r.Find(code)
	if !ok {
		c, _ = r.Find("INTERNAL")
		message = fmt.Sprintf("Unregistered error code %q", code)
	}
	return &Error{Code: c.Code, Message: message, Exit: c.Exit, Retryable: c.Retryable, Stage: c.Stage, Evidence: map[string]any{}}
}

type Warning struct {
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Evidence map[string]any `json:"evidence"`
}
type Meta struct {
	RequestID       string `json:"request_id"`
	Time            string `json:"ts_iso"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	ContractVersion string `json:"contract_version"`
	SchemaVersion   string `json:"schema_version"`
	DataHash        string `json:"data_hash"`
	Entrypoint      string `json:"entrypoint,omitempty"`
	ExitSemantics   string `json:"exit_semantics,omitempty"`
}
type Result struct {
	OK          bool      `json:"ok"`
	ToolVersion string    `json:"tool_version"`
	Data        any       `json:"data"`
	Meta        Meta      `json:"meta"`
	Warnings    []Warning `json:"warnings"`
	Commands    []string  `json:"commands"`
	Errors      []*Error  `json:"errors"`
}

// Canonical marshals owned data with sorted map keys and no float values.
// encoding/json preserves array order and sorts string map keys. Its default
// HTML escape is disabled because the hash is over compact UTF-8 JSON.
func HashBytes(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func Canonical(v any) ([]byte, error) {
	// Decode the owned value to maps first, so struct field declaration order
	// cannot change a hash. json.Number preserves integer precision.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var integers func(any) error
	integers = func(x any) error {
		switch n := x.(type) {
		case json.Number:
			if strings.ContainsAny(string(n), ".eE") {
				return fmt.Errorf("Owned data must not contain floats")
			}
		case map[string]any:
			for _, child := range n {
				if err := integers(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range n {
				if err := integers(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := integers(value); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}
