# Volley

Volley uses Claude Code and Codex to review a specification. One agent drafts
and revises `SPEC.md`. The other agent critiques it. The controller records
questions, answers, turns and approval for exact file bytes.

The Go implementation and its local integration checks are complete through
T22. Go distribution files are prepared in `packaging/`. The root Bash
launchers remain the current default until the final gates pass. The live
vendor check is pending. No release is published by this work.

## Use the Go executable

Build with Go 1.26.1 or later:

```sh
go build -o ./build/volley ./cmd/volley
./build/volley --help
./build/volley plan /absolute/workspace --config settings.toml
./build/volley run /absolute/workspace --config settings.toml
./build/volley status /absolute/workspace --json
```

Put a brief in `BRIEF.md`, or put an existing specification in `SPEC.md`.
Use optional `CONSTRAINTS.md` for binding requirements. Settings resolve in
this order: flag, TOML file, default. Old Volley setting environment variables
are ignored and produce warnings. The default config path is
`$XDG_CONFIG_HOME/volley/config.toml`, or `~/.config/volley/config.toml`.
A missing explicitly named config file is an error.

A native run returns when an answer is required. Use `--wait` to keep the
controller waiting. Use `human questions`, `human answer`, `human steer` and
`runs resume` for recorded input and continuation. Answers bind to a question
generation. They do not depend on file modification times.

Approval means that no material objection remains under the saved review
contract for the exact specification bytes. It does not approve implementation,
validate premises, or prove runtime behavior.

## Read the guides

- [Commands and machine output](docs/go-commands.md)
- [Approval, closing costs and rejected changes](docs/go-review.md)
- [Saved state, stop and recovery](docs/go-inspection.md)
- [Legacy workspaces and uncertain turns](docs/go-legacy.md)
- [Migration: C01–C15, D01–D06 and safety limits](docs/go-migration.md)
- [Prepared distribution and supported targets](packaging/README.md)
- [Change log](CHANGELOG.md)

Use `capabilities --json`, `schema`, and `robot-docs guide` for the complete
native contract. Prompts are embedded in the executable. Runtime use does not
need the source prompt directory, Go, a download, or a build.

Local tests use owned provider stubs. They prove controller mechanics and
argument arrays. Vendor permissions, authentication, billing, session behavior
and model choices remain unverified until the separately approved live check.
Codex protection detects file changes after a turn. It cannot prevent a write.
