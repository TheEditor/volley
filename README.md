# Volley

Volley uses Claude Code and Codex to review a specification. One agent drafts
and revises `SPEC.md`. The other agent critiques it. The controller records
questions, answers, turns and approval for exact file bytes.

Volley now uses one Go engine. The root commands `volley.sh`, `cc-volley`,
and `codex-volley` are thin launchers for that engine. Local integration checks
and the required direct and terminal live reviews passed. No release is
published by this work.

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
- [Distribution and supported targets](packaging/README.md)
- [Change log](CHANGELOG.md)

Use `capabilities --json`, `schema`, and `robot-docs guide` for the complete
native contract. Prompts are embedded in the executable. Runtime use does not
need the source prompt directory, Go, a download, or a build.

Local tests use owned provider stubs. They prove controller mechanics and
argument arrays. The bounded live check records actual vendor permission and workflow evidence;
it does not establish behavior for every vendor version or setting. Unknown
model and effort choices remain recorded as unknown.
Codex protection detects file changes after a turn. It cannot prevent a write.

Dependency checks record command paths and reported versions. Volley does not
read complete dependency program files or fingerprint their contents. Resume
checks the reported versions. See [dependency checks](docs/go-migration.md#dependency-program-checks)
for the saved-record format change and remaining checks.
