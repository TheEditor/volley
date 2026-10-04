package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/pelletier/go-toml/v2"
)

type Override struct {
	Value    any
	Flag     string
	Position int
}
type ResolveOptions struct {
	Cwd, Home, XDGRoot, NamedFile, Workspace   string
	Mutating, AllowMissingNamed, RequireExists bool
	Flags                                      map[string]Override
	// Lookup is used only for active executable bindings. Config handlers leave
	// it nil. It is never a process launch.
	Lookup func(string) (string, error)
	// Defaults permits a future registry-default change to be tested against a
	// saved complete record. It is internal input, not an environment setting.
	Defaults map[string]any
}
type Inspection struct {
	Config       Settings          `json:"config"`
	Provenance   map[string]Source `json:"_provenance"`
	SelectedFile Selection         `json:"selected_file"`
}
type Selection struct {
	Path     string `json:"path"`
	Explicit bool   `json:"explicit"`
	Exists   bool   `json:"exists"`
}

func (r Resolved) Inspection() Inspection {
	return Inspection{r.Settings, r.Sources, Selection{r.SelectedFile, r.Explicit, r.Exists}}
}
func selectFile(o ResolveOptions) (string, bool, error) {
	if !filepath.IsAbs(o.Cwd) {
		return "", false, fmt.Errorf("Invocation cwd must be absolute")
	}
	if o.NamedFile != "" {
		return fromBase(o.Cwd, o.NamedFile), true, nil
	}
	root := o.XDGRoot
	if root == "" {
		if o.Home == "" || !filepath.IsAbs(o.Home) {
			return "", false, fmt.Errorf("HOME must be absolute")
		}
		root = filepath.Join(o.Home, ".config")
	}
	if !filepath.IsAbs(root) {
		if o.Mutating {
			return "", false, fmt.Errorf("XDG_CONFIG_HOME must be absolute for mutation")
		}
		root = fromBase(o.Cwd, root)
	}
	return filepath.Join(root, "volley", "config.toml"), false, nil
}
func Resolve(o ResolveOptions) (Resolved, []contract.Warning, error) {
	out := Resolved{Sources: make(map[string]Source)}
	warnings := make([]contract.Warning, 0)
	path, explicit, err := selectFile(o)
	if err != nil {
		return out, warnings, err
	}
	out.SelectedFile = path
	out.Explicit = explicit
	r, err := contract.Load()
	if err != nil {
		return out, warnings, err
	}
	values := make(map[string]any)
	for _, s := range r.Settings {
		v := s.Default
		if replacement, ok := o.Defaults[s.Key]; ok {
			v = replacement
		}
		v, err = Validate(s, v)
		if err != nil {
			return out, warnings, err
		}
		values[s.Key] = v
		declared, err := json.Marshal(v)
		if err != nil {
			return out, warnings, err
		}
		out.Sources[s.Key] = Source{Source: "default", Rule: "declared registry default = " + string(declared) + "; file then flag may override"}
	}
	d, err := ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) || o.RequireExists || explicit && !o.AllowMissingNamed {
			return out, warnings, err
		}
	} else {
		out.Exists = true
		for key, v := range d.Values {
			values[key] = v
			out.Sources[key] = Source{Source: "file", Path: path, Line: d.Spans[key].Line, Rule: "file overrides default"}
		}
	}
	// Validate the complete file and its relative-path meanings before flags.
	fileValues := cloneValues(values)
	if err := validateResolved(fileValues, out.Sources, o); err != nil {
		return out, warnings, err
	}
	known := make(map[string]contract.Setting)
	for _, s := range r.Settings {
		known[s.Key] = s
	}
	for key, override := range o.Flags {
		declaration, ok := known[key]
		if !ok {
			return out, warnings, fmt.Errorf("Unknown setting %q", key)
		}
		v, err := Validate(declaration, override.Value)
		if err != nil {
			return out, warnings, fmt.Errorf("%s: %w", key, err)
		}
		if override.Flag != declaration.Flag {
			return out, warnings, fmt.Errorf("Setting override must use canonical flag %s", declaration.Flag)
		}
		values[key] = v
		out.Sources[key] = Source{Source: "flag", Flag: declaration.Flag, ArgumentPosition: override.Position, Rule: "flag overrides file and default"}
	}
	if err := validateResolved(values, out.Sources, o); err != nil {
		return out, warnings, err
	}
	for _, key := range []string{"claude_bin", "codex_bin", "gashki_bin"} {
		active := (values["backend"] == "cli" && key != "gashki_bin") || (values["backend"] == "gashki" && key == "gashki_bin")
		if !active {
			if out.Sources[key].Source != "default" {
				warnings = append(warnings, contract.Warning{Code: "INACTIVE_SETTING", Message: "Executable override is inactive", Evidence: map[string]any{"setting": key, "reason": "inactive on selected backend"}})
			}
			continue
		}
		if o.Lookup != nil {
			resolved, err := o.Lookup(values[key].(string))
			if err != nil {
				return out, warnings, err
			}
			values[key] = resolved
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return out, warnings, err
	}
	if err := json.Unmarshal(encoded, &out.Settings); err != nil {
		return out, warnings, err
	}
	return out, warnings, nil
}
func cloneValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}
func fromBase(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

// Physical resolves all existing symlink parents, including an absent leaf.
func Physical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	physical, err := Physical(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(physical, filepath.Base(absolute)), nil
}
func validateResolved(values map[string]any, sources map[string]Source, o ResolveOptions) error {
	if values["backend"] == "gashki" && values["persistent"] == true {
		return fmt.Errorf("persistent=true requires the direct CLI backend")
	}
	r, err := contract.Load()
	if err != nil {
		return err
	}
	for _, s := range r.Settings {
		if s.Type != "path" && s.Type != "executable" {
			continue
		}
		v := values[s.Key].(string)
		if v == "" {
			continue
		}
		base := o.Cwd
		if sources[s.Key].Source == "file" {
			base = filepath.Dir(sources[s.Key].Path)
		}
		if s.Type == "path" || strings.ContainsRune(v, '/') {
			values[s.Key] = fromBase(base, v)
		}
	}
	context := values["context_dir"].(string)
	if context != "" {
		canonical, err := filepath.EvalSymlinks(context)
		if err != nil {
			return fmt.Errorf("context_dir: %w", err)
		}
		st, err := os.Stat(canonical)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("context_dir must be a readable directory")
		}
		f, err := os.Open(canonical)
		if err != nil {
			return err
		}
		_, err = f.Readdirnames(1)
		_ = f.Close()
		if err != nil && err != io.EOF {
			return fmt.Errorf("context_dir is not readable: %w", err)
		}
		if o.Workspace != "" {
			workspace, err := Physical(fromBase(o.Cwd, o.Workspace))
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(workspace, canonical)
			if err != nil {
				return err
			}
			if relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("context_dir must be outside the workspace")
			}
		}
		values["context_dir"] = canonical
	}
	if path := values["gashki_config"].(string); path != "" {
		st, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("gashki_config: %w", err)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("gashki_config must be a regular file")
		}
	}
	return nil
}
func ParseArgument(key, text string) (any, error) {
	r, err := contract.Load()
	if err != nil {
		return nil, err
	}
	for _, s := range r.Settings {
		if s.Key != key {
			continue
		}
		var value any = text
		switch s.Type {
		case "integer":
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil || strconv.FormatInt(n, 10) != text {
				return nil, fmt.Errorf("Expected a canonical integer")
			}
			value = n
		case "boolean":
			if text != "true" && text != "false" {
				return nil, fmt.Errorf("Expected true or false")
			}
			value = text == "true"
		case "array":
			value, err = input.JSON(strings.NewReader(text))
			if err != nil {
				return nil, err
			}
		}
		return Validate(s, value)
	}
	return nil, fmt.Errorf("Unknown setting %q", key)
}
func (r Resolved) TOML() ([]byte, error) {
	data, err := json.Marshal(r.Settings)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&values); err != nil {
		return nil, err
	}
	declarations, err := contract.Load()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("# Volley effective settings\n# Values are explicit; source comments describe this resolution only.\n")
	for _, s := range declarations.Settings {
		v, err := Validate(s, values[s.Key])
		if err != nil {
			return nil, err
		}
		source := r.Sources[s.Key]
		note := source.Source
		if source.Source == "file" {
			note += fmt.Sprintf(" %q line %d", source.Path, source.Line)
		} else if source.Source == "flag" {
			note += fmt.Sprintf(" %s argument %d", source.Flag, source.ArgumentPosition)
		}
		fmt.Fprintf(&out, "# source: %s\n", note)
		b, err := toml.Marshal(map[string]any{s.Key: v})
		if err != nil {
			return nil, err
		}
		out.Write(b)
	}
	return out.Bytes(), nil
}
func RetiredWarnings(present func(string) bool) ([]contract.Warning, error) {
	raw, err := contract.RawRegistry()
	if err != nil {
		return nil, err
	}
	r, err := contract.Load()
	if err != nil {
		return nil, err
	}
	flags := make(map[string]string)
	for _, s := range r.Settings {
		flags[s.Key] = s.Flag
	}
	warnings := make([]contract.Warning, 0)
	for _, rawName := range raw["retired_setting_variables"].([]any) {
		name := rawName.(string)
		if !present(name) {
			continue
		}
		key := strings.ToLower(strings.TrimPrefix(name, "VOLLEY_"))
		switch key {
		case "call_timeout":
			key = "wait_timeout"
		case "profile":
			key = "rubric"
		case "poll":
			key = "poll_interval"
		}
		warnings = append(warnings, contract.Warning{Code: "ENV_SETTING_IGNORED", Message: "Retired setting variable is ignored", Evidence: map[string]any{"setting": name, "replacement_flag": flags[key]}})
	}
	return warnings, nil
}

// LookupExecutable keeps Go's current-directory lookup refusal intact.
func LookupExecutable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("Executable is not a regular file")
	}
	return absolute, nil
}
