# Legacy workspaces and uncertain turns

The Go command cannot start a review in a legacy Bash workspace. One legacy marker under `state/` is sufficient: `roles`, `persistent`, `backend`, `run`, `provenance.md`, or a direct child named `session.` with a nonempty suffix. Empty and partial records count. A marker that is a directory or a symbolic link also blocks creation. The native command returns `LEGACY_BOUNDARY_UNVERIFIED` with exit 8. An installed native `cc-volley` or `codex-volley` launcher maps that exit to 1. The JSON error keeps the native exit code.

Use `volley workspace legacy-report OLD_WORKSPACE --json` to read the old files. The report contains source hashes, checked facts, unknown facts, and a fresh workspace recipe. It does not query a process or pane. An old approval is historical text. It has no native approval receipt. The command creates no native state and accepts no `--yes`, migration, or import mode.

Settle old execution and keep the old files. Copy the required brief, constraints, and decided human requirements to a new workspace. Then use:

```sh
volley run NEW_WORKSPACE --seed OLD_WORKSPACE/SPEC.md --config NEW_SETTINGS.toml
```

Do not copy old state, session identifiers, or open question generations. The new review starts at critic round 1. Native import and reuse of legacy sessions are deferred.

A native manifest takes precedence over legacy markers. A broken manifest returns `STATE_INVALID`. An unsupported record version returns `STATE_VERSION_UNSUPPORTED`. Keep the broken workspace. Use read-only inspection, restore a verified workspace copy, or start a fresh workspace from preserved inputs with explicit settings. The command does not treat broken native state as a new run.

A workspace with only `rounds/` has no saved run identity. The command does not infer an approval, phase, or session from those files. A planned native output that conflicts with an existing file returns `OUTPUT_CONFLICT` before an agent call.

## Explicit resolution

To resolve the current uncertain native turn, supply its exact identifier and a strict JSON object:

```sh
volley runs resolve WORKSPACE --turn TURN_ID --from-stdin --yes <<'JSON'
{"resolution":"abandon","artifact_paths":[],"evidence_paths":[],"note":"Keep the partial files and stop this run."}
JSON
```

All four fields are required. Unknown fields, duplicate fields, nulls, invalid values, and trailing JSON are rejected. The choices are:

- `accept_completed`: check artifacts, turn ownership, and session settlement before advance. The resolution record uses `human_attested` if independent backend completion evidence is absent. It uses `backend_confirmed` only when that evidence is checked.
- `confirm_not_executed`: require independent proof of no delivery and no process start. A human statement alone cannot permit retry.
- `abandon`: keep all partial files and save the run as stopped. A new review requires a fresh workspace.

The command does not execute a model turn. A direct turn can advance from an existing checked completion receipt. It can permit later resume after a checkpoint-bound proof of failure to start. Missing or changed proof remains uncertain. The real Gashki evidence adapter is part of the integration work. The evidence and session interfaces have file-backed fake checks for that integration boundary.

The manifest binds the resolution record by hash, turn, choice, and completion source. Inspection rejects a changed bound record. A copied seed spec has a separate file identity from its immutable saved source. Later revisions preserve the source and transaction history.
