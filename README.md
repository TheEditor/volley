# volley

Automated plan/critique loop between two coding agents on the same machine:
one of **Claude Code** and **Codex** is the planner, the other the critic.
The planner drafts a spec from your brief; the critic reviews it; the planner
revises or rebuts; repeat until the critic approves. No human input after the
brief.

## Usage

1. Write `BRIEF.md` — what you want built, in as much or as little detail as
   you like. This is the only human input.
2. Run:

   ```sh
   ./cc-volley              # Claude Code plans, Codex critiques
   ./codex-volley           # Codex plans, Claude Code critiques
   ./volley.sh path/to/dir  # underlying engine; workspace defaults to this
                            # directory, role to VOLLEY_PLANNER (claude)
   ```

3. Read `SPEC.md` when it exits.

A workspace remembers its role assignment (`state/roles`): an interrupted
run must resume with the same planner, and volley refuses if it wouldn't.

Exit codes: `0` converged (critic approved), `2` impasse (round cap reached),
`1` setup or invocation failure.

## Files produced

| Path | Contents |
| --- | --- |
| `SPEC.md` | The living plan, including a Decision Log of every fix and rebuttal |
| `rounds/rNN.critique.md` | The critic's objections each round |
| `rounds/rNN.spec.md` | Spec snapshot after each revision |
| `state/*.log` | Full planner/critic transcripts and the loop log |
| `state/IMPASSE.md` | Written only if the round cap is hit without approval |

## Knobs (environment variables)

| Var | Default | Meaning |
| --- | --- | --- |
| `VOLLEY_PLANNER` | `claude` | Which agent plans (`claude` or `codex`); the other critiques |
| `MAX_ROUNDS` | `8` | Hard cap on critique/revise rounds |
| `CALL_TIMEOUT` | `900` | Seconds per agent invocation (needs `timeout`/`gtimeout`; skipped if absent) |
| `CLAUDE_BIN` / `CODEX_BIN` | `claude` / `codex` | Binary overrides |

## Billing guard

Before running, the script refuses to start if it finds a pay-per-token API
credential either CLI might use instead of your subscription login:
`ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in the environment, an `apiKeyHelper`
in `~/.claude/settings.json`, or a non-null API key in `~/.codex/auth.json`.
Set `VOLLEY_ALLOW_API_KEY=1` to override if metered billing is intended.

## Design notes

- **Files are the only state.** Every invocation re-reads the brief, spec, and
  latest critique from disk, so the loop is resumable: rerunning `volley.sh`
  picks up after the last completed critique.
- **Termination is machine-read.** The critic must end with `VERDICT: APPROVE`
  or `VERDICT: REVISE`; the orchestrator greps for it and re-asks once if
  missing. Its rubric permits only material objections and forbids
  re-litigating Decision Log entries, so approval is reachable.
- **The critic is sandboxed read-only** whichever agent plays it (`codex
  exec --sandbox read-only`, or claude restricted to `Read,Glob,Grep`); its
  critique is captured from its final reply, not written by it. The planner
  gets write access to the workspace only (claude: file tools with
  `acceptEdits`; codex: `--sandbox workspace-write`).
- Prompts live in `prompts/` and are meant to be edited to taste.
