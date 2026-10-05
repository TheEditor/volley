# Prepared Go distribution

This directory contains the reviewable replacement launchers. The default
switch is pending. Do not install these files over the current commands until
the T25 gate permits the switch. No tag, upload or publication is performed.

The installed layout contains one Go engine:

```text
BIN_DIRECTORY/
  volley
  volley.sh
  cc-volley
  codex-volley
```

Build `cmd/volley` as `volley`. Copy the three launchers from this directory
beside it. They require `/bin/bash`. They do not build, download, or search PATH
for an engine. An installed launcher uses only the adjacent `volley` executable.
A source launcher uses `build/volley` when its own directory, or the parent of
its `packaging/` directory, contains `go.mod` and `cmd/volley/main.go`.

The role launchers pass `--planner claude` or `--planner codex` explicitly.
Inspection commands keep native grammar. An additional explicit planner flag
is a duplicate and is refused. No retired setting variable selects the role.

A historical positional invocation adds waiting behavior. With terminal input
and human output it also enables interactive answers. Use `run WORKSPACE` for
the native foreground behavior. Matching native workspaces attach to saved
state. Legacy workspaces return a migration refusal. There is no Bash fallback.

With no arguments, a source launcher selects the checkout directory. An
installed launcher requires a workspace and returns a missing-argument error.
It does not create a workspace in the binary directory. The native executable
with no arguments shows help. Use `--help` to show launcher help explicitly.

Legacy exits: native 0 becomes 0; impasse 7 becomes 2; other failures become 1.
Signal exits 130 and 143 remain unchanged. JSON retains native error codes and
native exit evidence, with `meta.entrypoint` and `meta.exit_semantics=legacy`.

`--version --json` reports the tool version, contract version, build commit,
commit date, modified-source status, Go version and target. A development build
has tool version `0.0.0-dev`. Keep the build record with each installed artifact.

## Targets and rollback

Runtime process, file, lock and signal checks cover macOS arm64 and Linux arm64.
macOS amd64 and Linux amd64 are build targets with no current runtime proof.
Do not advertise those two targets as runtime verified. Windows is unsupported.

Use `GOOS=darwin GOARCH=arm64 go build -o build/volley ./cmd/volley`, or the
corresponding Linux arm64 build. Cross compilation is not a runtime pass.

The immutable Bash baseline is in `tests/scenarios/legacy/`, pinned to commit
`a7252e474023f986d9912469b1e58742e644afc8`. Tests may use it only with owned
stubs and isolated roots. It is not installed. Use the old Git revision for an
explicit rollback. Do not add automatic engine selection or fallback.
