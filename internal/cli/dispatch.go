package cli

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/delivery"
	"github.com/TheEditor/volley/internal/ops"
)

type rawOutput struct {
	Data     any
	Bytes    []byte
	Warnings []contract.Warning
}

func dispatch(ctx context.Context, args []string, r *contract.Registry, opts Options) (any, error) {
	stage := func(name string) {
		injectFault(name, environment(opts))
		if opts.Stage != nil {
			opts.Stage(name)
		}
	}
	x, err := parseWithStage(args, r, stage)
	if err != nil {
		return nil, err
	}
	stage("execution")
	value, err := handleInvocation(ctx, x, r, opts)
	if err != nil {
		return value, err
	}
	retired, err := config.RetiredWarnings(func(name string) bool {
		for _, entry := range environment(opts) {
			if strings.HasPrefix(entry, name+"=") {
				return true
			}
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	result, ok := value.(commandOutput)
	if !ok {
		result = commandOutput{Data: value}
	}
	result.Warnings = append(result.Warnings, retired...)
	target, _ := x.Values["--deliver"].(string)
	if target == "" {
		target = "stdout"
	}
	if target == "stdout" {
		if result.Raw != nil || len(result.Warnings) > 0 {
			return rawOutput{result.Data, result.Raw, result.Warnings}, nil
		}
		return result.Data, nil
	}
	b := result.Raw
	if b == nil {
		b, err = contract.Canonical(result.Data)
		if err != nil {
			return nil, err
		}
		b = append(b, '\n')
	}
	receipt := delivery.Null(b)
	if strings.HasPrefix(target, "file:") {
		env := environment(opts)
		source, _ := x.Values["--config"].(string)
		if source == "" {
			root := envValue(env, "XDG_CONFIG_HOME")
			if root == "" {
				root = filepath.Join(envValue(env, "HOME"), ".config")
			}
			source = filepath.Join(root, "volley/config.toml")
		}
		protected := append([]string{}, result.Protected...)
		protected = append(protected, source)
		if data, ok := result.Data.(map[string]any); ok {
			if workspace, ok := data["workspace"].(string); ok {
				protected = append(protected, filepath.Join(workspace, "state/manifest.json"), filepath.Join(workspace, "volley.config.toml"), filepath.Join(workspace, "gashki.config.toml"))
			}
		}
		path := strings.TrimPrefix(target, "file:")
		receipt, err = delivery.File(ctx, path, b, x.Values["--force"] == true, protected)
		if err != nil {
			err = translateError(r, err)
			if e, ok := err.(*contract.Error); ok && e.Code == "OUTPUT_CONFLICT" {
				command := append([]string{"volley"}, strings.Split(x.Command, " ")...)
				command = append(command, x.Positionals...)
				command = append(command, "--deliver", "file:"+path+".new")
				text := ops.Command(command...)
				e.Remediation = &text
			}
			return nil, err
		}
	}
	return rawOutput{Data: receipt, Warnings: result.Warnings}, nil
}
