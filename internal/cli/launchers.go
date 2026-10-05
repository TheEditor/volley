package cli

import (
	"os"

	"github.com/TheEditor/volley/internal/contract"
)

func legacyEntrypoint(name string) bool {
	return name == "volley.sh" || name == "cc-volley" || name == "codex-volley"
}

// The shell passes a fixed role before the native arguments. Only review
// commands receive that role. All parsing still uses the native registry.
func launcherArgs(args []string, r *contract.Registry, opts Options) ([]string, error) {
	if !legacyEntrypoint(opts.Entrypoint) {
		return args, nil
	}
	role := ""
	if opts.Entrypoint == "cc-volley" {
		role = "claude"
	} else if opts.Entrypoint == "codex-volley" {
		role = "codex"
	}
	if role != "" && len(args) >= 2 && args[0] == "--planner" && args[1] == role {
		args = args[2:]
	}
	x, err := Parse(args, r)
	if err != nil {
		return nil, err
	}
	prefix := []string{}
	if role != "" && (x.Command == "run" || x.Command == "plan" || x.Command == "runs resume") {
		prefix = append(prefix, "--planner", role)
	}
	if x.Shorthand {
		if x.Values["--wait"] != true {
			prefix = append(prefix, "--wait")
		}
		in := opts.Input
		if in == nil {
			in = os.Stdin
		}
		file, fileInput := in.(*os.File)
		terminal := fileInput && isTerminal(file)
		if opts.Terminal != nil {
			terminal = *opts.Terminal
		}
		if terminal && !MachineMode(args) && x.Values["--interactive"] != true {
			prefix = append(prefix, "--interactive")
		}
	}
	return append(prefix, args...), nil
}
