//go:build darwin || linux

package agent

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/TheEditor/volley/internal/input"
	"golang.org/x/sys/unix"
)

type BillingSelection struct {
	Allowed  bool `json:"allowed"`
	Explicit bool `json:"explicit"`
}

func GuardBilling(ctx context.Context, roots EffectiveRoots, env []string, workspace string, selection BillingSelection, extraSettings []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if selection.Allowed {
		if !selection.Explicit {
			return safeError("BILLING_REFUSED", "Billing override requires explicit caller selection", map[string]any{"indicator": "allow_api_key", "reason": "override is not explicit"})
		}
		return nil
	}
	if err := roots.RequireKnown(); err != nil {
		return err
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		v := envVariable(env, name, "caller")
		if v.Present && v.Value != nil && *v.Value != "" {
			return billingRefusal(name, "", "API credential indicator is present")
		}
	}
	paths := []string{filepath.Join(*roots.Claude, "settings.json")}
	if workspace != "" {
		paths = append(paths, filepath.Join(workspace, ".claude/settings.json"), filepath.Join(workspace, ".claude/settings.local.json"))
	}
	paths = append(paths, extraSettings...)
	for _, path := range paths {
		m, err := guardJSON(path)
		if err != nil {
			return billingRefusal("apiKeyHelper", path, "Required guard source is unreadable or invalid")
		}
		if m == nil {
			continue
		}
		if _, present := m["apiKeyHelper"]; present {
			return billingRefusal("apiKeyHelper", path, "Credential helper is configured")
		}
		if vars, ok := m["env"].(map[string]any); ok {
			for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
				if value, present := vars[name]; present && value != nil && value != "" {
					return billingRefusal(name, path, "Settings contain an API credential indicator")
				}
			}
		}
	}
	path := filepath.Join(*roots.Codex, "auth.json")
	m, err := guardJSON(path)
	if err != nil {
		return billingRefusal("OPENAI_API_KEY", path, "Required guard source is unreadable or invalid")
	}
	if value, present := m["OPENAI_API_KEY"]; present && value != nil {
		return billingRefusal("OPENAI_API_KEY", path, "Auth source contains a non-null API key indicator")
	}
	return nil
}
func billingRefusal(indicator, path, reason string) error {
	return safeError("BILLING_REFUSED", "Billing guard refused this launch", map[string]any{"indicator": indicator, "path": path, "reason": reason})
}
func guardJSON(path string) (map[string]any, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0444 == 0 {
		return nil, os.ErrPermission
	}
	b, err := io.ReadAll(io.LimitReader(f, input.Limit+1))
	if err != nil {
		return nil, err
	}
	v, err := input.Record(bytes.NewReader(b), input.Limit)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, os.ErrInvalid
	}
	after, err := f.Stat()
	if err != nil || after.Size() != st.Size() {
		return nil, os.ErrInvalid
	}
	return m, nil
}

// GuardPaths returns only source names. Guard source bytes are never persisted.
func GuardPaths(roots EffectiveRoots) []string {
	out := []string{}
	if roots.Claude != nil {
		out = append(out, strings.TrimSuffix(*roots.Claude, "/")+"/settings.json")
	}
	if roots.Codex != nil {
		out = append(out, strings.TrimSuffix(*roots.Codex, "/")+"/auth.json")
	}
	return out
}
