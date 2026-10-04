package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/TheEditor/volley/internal/contract"
)

type CommandRequest struct {
	Command, Key, Value string
	Options             ResolveOptions
	Stdin               io.Reader
	Edit                EditOptions
	Editor              EditorOptions
}
type CommandResult struct {
	Data     any
	TOML     []byte
	Warnings []contract.Warning
}

func Handle(ctx context.Context, req CommandRequest) (CommandResult, error) {
	result := CommandResult{Warnings: []contract.Warning{}}
	if req.Command == "config schema" {
		b, err := contract.Schema("settings")
		if err != nil {
			return result, err
		}
		var schema map[string]any
		if err := json.Unmarshal(b, &schema); err != nil {
			return result, err
		}
		result.Data = schema
		return result, nil
	}
	switch req.Command {
	case "config set":
		req.Edit.Resolve = req.Options
		data, err := Set(ctx, req.Edit, req.Key, req.Value)
		result.Data = data
		return result, err
	case "config patch":
		req.Edit.Resolve = req.Options
		data, err := PatchJSON(ctx, req.Edit, req.Stdin)
		result.Data = data
		return result, err
	case "config edit":
		req.Editor.Resolve = req.Options
		data, err := Edit(ctx, req.Editor)
		result.Data = data
		return result, err
	}
	req.Options.Lookup = nil // Config inspection never resolves or launches tools.
	if req.Command == "config validate" && req.Options.NamedFile != "" {
		req.Options.RequireExists = true
	}
	resolved, warnings, err := Resolve(req.Options)
	if err != nil {
		return result, err
	}
	result.Warnings = warnings
	switch req.Command {
	case "config show":
		result.Data = resolved.Inspection()
		result.TOML, err = resolved.TOML()
	case "config get":
		raw, err := json.Marshal(resolved.Settings)
		if err != nil {
			return result, err
		}
		var values map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&values); err != nil {
			return result, err
		}
		value, ok := values[req.Key]
		if !ok {
			return result, fmt.Errorf("Unknown setting %q", req.Key)
		}
		result.Data = map[string]any{"key": req.Key, "value": value, "source": resolved.Sources[req.Key]}
	case "config validate":
		result.Data = map[string]any{"valid": true, "path": resolved.SelectedFile, "exists": resolved.Exists}
	default:
		return result, fmt.Errorf("Unknown config command %q", req.Command)
	}
	return result, err
}
