package process

import "strings"

// ChildEnvironment preserves identity roots. Gashki owns its spawn stripping.
// Direct nested-Claude calls use only the three names retained from Bash.
func ChildEnvironment(base []string, backend string) ([]string, []string) {
	nested := false
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok && key == "CLAUDECODE" && value != "" {
			nested = true
		}
	}
	removed := make([]string, 0)
	out := make([]string, 0, len(base))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if backend == "cli" && nested && (key == "ANTHROPIC_BASE_URL" || key == "CLAUDECODE" || key == "CLAUDE_CODE_ENTRYPOINT") {
			removed = append(removed, key)
			continue
		}
		out = append(out, item)
	}
	return out, removed
}
