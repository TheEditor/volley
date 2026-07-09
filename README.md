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

### Planning against an existing repo

Set `VOLLEY_CONTEXT_DIR=/abs/path/to/repo` and the brief can request plans
about real systems ("plan the migration of X in this repo"): both planner
and critic ground their work in the actual code. The path must be absolute,
readable, and outside the workspace. Have `BRIEF.md` name the entry points
and paths of interest so the agents don't drown in an unfamiliar tree.

### Steering a running loop

Drop a `HUMAN.md` into the workspace at any time. At the next round it is
injected into both role prompts as a directive that outranks the critic —
any point it settles is settled, and neither agent may re-litigate it — then
archived to `rounds/rNN.human.md` so it applies exactly once. This is the
only way to steer a run without killing it (`^C` discards an in-flight
round).

Exit codes: `0` converged (critic approved), `2` impasse (round cap reached),
`1` setup or invocation failure.

## Files produced

| Path | Contents |
| --- | --- |
| `SPEC.md` | The living plan, including a Decision Log of every fix and rebuttal |
| `rounds/rNN.critique.md` | The critic's objections each round |
| `rounds/rNN.spec.md` | Spec snapshot after each revision |
| `rounds/second-opinion.md` | The swapped critic's advisory review (only with `VOLLEY_SECOND_OPINION=1`) |
| `rounds/rNN.human.md` | Archived one-shot `HUMAN.md` directive, if you steered round NN |
| `state/*.log` | Full planner/critic transcripts and the loop log |
| `state/IMPASSE.md` | Written only if the round cap is hit without approval |

## Knobs (environment variables)

| Var | Default | Meaning |
| --- | --- | --- |
| `VOLLEY_PLANNER` | `claude` | Which agent plans (`claude` or `codex`); the other critiques |
| `MAX_ROUNDS` | `8` | Hard cap on critique/revise rounds |
| `CALL_TIMEOUT` | `900` | Seconds per agent invocation (needs `timeout`/`gtimeout`; skipped if absent) |
| `CLAUDE_BIN` / `CODEX_BIN` | `claude` / `codex` | Binary overrides |
| `VOLLEY_CLOSING_PASS` | `1` | After APPROVE, one extra planner pass addresses or declines the critic's non-blocking remarks (Decision Log entries); `0` disables |
| `VOLLEY_SECOND_OPINION` | `0` | After APPROVE, the *other* agent reviews the final spec once (`rounds/second-opinion.md`); advisory only — its remarks feed the closing pass, it cannot flip the verdict |
| `VOLLEY_CONTEXT_DIR` | unset | Absolute path to an existing codebase both agents read (claude via `--add-dir`; codex's sandboxes read outside cwd natively). Read-only by contract: writes stay in the workspace. Name entry points in `BRIEF.md` to avoid context dilution |
| `VOLLEY_PROFILE` | unset | Append `prompts/profiles/<name>.md` to every critic prompt: extra material-defect classes, active probes (silence can be the objection), and severity calibration for a domain. Shipped: `security`, `data`, `decision-memo` |

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
- Prompts live in `prompts/` and are meant to be edited to taste. Critic
  rubric profiles live in `prompts/profiles/`; each ends by restating the
  materiality bar so a profile sharpens the critic without turning it into
  a checklist zealot. `decision-memo` is the one that makes volley useful
  on non-software briefs.
