# Go command use

Build the development executable with `go build -o ./build/volley ./cmd/volley`. Use `./build/volley --help` for core commands. Use `./build/volley capabilities --json` for the complete declarations, `schema NAME --json` for schemas, and `robot-docs guide` for the command guide.

Use `plan WORKSPACE` to read settings and proposed arguments. It creates no run and starts no process. Use `run WORKSPACE` for a foreground review. The Go engine uses saved settings for resume. The source Bash launchers remain the default while the final checks are incomplete.

A bare workspace is shorthand for run. An exact command name selects that command. Use `run ./status` or `-- ./status` to select a path with a command name. Flags may occur before or after the command, before `--`. Boolean settings take `true` or `false`. Selectors such as `--json` take no value.

Machine output has seven keys: `ok`, `tool_version`, `data`, `meta`, `warnings`, `commands`, and `errors`. Stderr repeats the first error. Use the schema and guide commands for complete examples and error definitions.

Declared inspection commands accept `--deliver=file:PATH` and `--deliver=null`. They deliver the serialized data payload and return byte count and SHA-256 metadata. An identical file succeeds. A different file requires `--force`. A workspace manifest or source config cannot be an output target. `config show --toml` writes raw TOML to stdout. Add file delivery when a machine metadata envelope is needed.

`feedback TEXT --idempotency-key=KEY` saves only the supplied text locally. Repeating the same key and text succeeds. Changed text with that key refuses. It makes no network call.

See [command proof](../internal/cli/CLI-PROOF.md) for the checked scope and remaining gates.
