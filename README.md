# volley

Automated plan/critique loop between two coding agents on the same machine:
one of **Claude Code** and **Codex** is the planner, the other the critic.
The planner drafts a spec from your brief; the critic reviews it; the planner
revises or rebuts; repeat until the critic approves. No human input after the
brief.

## Usage

1. Write `BRIEF.md` — the human instruction you would give manually. It can
   name source files to read, standards to consider, and the artifact to
   produce. `BRIEF.md.example` is a template meant to be edited, not a schema.
   If you have true non-negotiables, put them in optional `CONSTRAINTS.md`.
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
| `SPEC.md` | The living plan/artifact requested by `BRIEF.md` |
| `rounds/rNN.critique.md` | The critic's objections each round |
| `rounds/rNN.response.md` | The planner's fix/rebuttal response for that critique |
| `rounds/rNN.spec.md` | Spec snapshot after each revision |
| `rounds/second-opinion.md` | The swapped critic's advisory review (only with `VOLLEY_SECOND_OPINION=1`) |
| `rounds/rNN.closing-response.md` | Planner disposition of non-blocking approval remarks, if a closing pass ran |
| `rounds/rNN.human.md` | Archived one-shot `HUMAN.md` directive, if you steered round NN |
| `state/provenance.md` | Run provenance: role assignment, CLI versions, explicit model pins if any, context/profile settings |
| `state/*.log` | Full planner/critic transcripts and the loop log |
| `state/IMPASSE.md` | Written only if the round cap is hit without approval |

## Knobs (environment variables)

| Var | Default | Meaning |
| --- | --- | --- |
| `VOLLEY_PLANNER` | `claude` | Which agent plans (`claude` or `codex`); the other critiques |
| `MAX_ROUNDS` | `8` | Hard cap on critique/revise rounds |
| `CALL_TIMEOUT` | `900` | Seconds per agent invocation (needs `timeout`/`gtimeout`; skipped if absent) |
| `CLAUDE_BIN` / `CODEX_BIN` | `claude` / `codex` | Binary overrides |
| `VOLLEY_CLAUDE_MODEL` / `VOLLEY_CODEX_MODEL` | unset | Optional explicit model pins passed as `--model`; when unset, `state/provenance.md` records that the CLI default was used and not known to Volley |
| `VOLLEY_CLAUDE_EFFORT` / `VOLLEY_CODEX_EFFORT` | unset | Optional reasoning-effort pins. Claude takes `low`\|`medium`\|`high`\|`xhigh`\|`max` via `--effort` (validated up front); codex gets the value as `-c model_reasoning_effort=…` and validates it itself (valid set depends on the model). Recorded in `state/provenance.md`; unset means CLI default |
| `VOLLEY_CLOSING_PASS` | `1` | After APPROVE, one extra planner pass addresses or declines the critic's non-blocking remarks; `0` disables |
| `VOLLEY_SECOND_OPINION` | `0` | After APPROVE, the *other* agent reviews the final spec once (`rounds/second-opinion.md`); advisory only — its remarks feed the closing pass, it cannot flip the verdict |
| `VOLLEY_CONTEXT_DIR` | unset | Absolute path to an existing codebase both agents read (claude via `--add-dir`; codex's sandboxes read outside cwd natively). Read-only by contract: writes stay in the workspace. Name entry points in `BRIEF.md` to avoid context dilution |
| `VOLLEY_PROFILE` | unset | Append `prompts/profiles/<name>.md` to every critic prompt. Shipped: `security`, `data`, `decision-memo`, `plan-spec` |
| `VOLLEY_PERSISTENT` | `0` | `1` keeps one CLI session per role across rounds (claude `--session-id`/`--resume`, codex `exec resume`), so later rounds carry working memory instead of cold-starting from the files. Session ids live in `state/session.<role>`; the mode is pinned per workspace like the role assignment. The second opinion stays one-shot: fresh eyes are its point |

## Billing guard

Before running, the script refuses to start if it finds a pay-per-token API
credential either CLI might use instead of your subscription login:
`ANTHROPIC_API_KEY` or `OPENAI_API_KEY` in the environment, an `apiKeyHelper`
in `~/.claude/settings.json`, or a non-null API key in `~/.codex/auth.json`.
Set `VOLLEY_ALLOW_API_KEY=1` to override if metered billing is intended.

## Design notes

- **Files are the only state.** Every invocation re-reads the brief, spec, and
  latest critique from disk, so the loop is resumable: rerunning `volley.sh`
  picks up after the last completed critique. `VOLLEY_PERSISTENT=1` adds CLI
  session memory on top of this, not instead of it: the files remain the
  ground truth, and resumed runs re-attach to their recorded sessions.
- **Termination is machine-read.** The critic must end with `VERDICT: APPROVE`
  or `VERDICT: REVISE`; the orchestrator greps for it and re-asks once if
  missing. Prior critiques and planner response files give later rounds enough
  context to avoid re-litigating settled points.
- **The critic is sandboxed read-only** whichever agent plays it (`codex
  exec --sandbox read-only`, or claude restricted to `Read,Glob,Grep`); its
  critique is captured from its final reply, not written by it. The planner
  gets write access to the workspace only (claude: file tools with
  `acceptEdits`; codex: `--sandbox workspace-write`).
- Prompts live in `prompts/` and are deliberately minimal: they mirror the
  short instructions a human types when running this loop by hand, plus the
  few mechanical requirements the orchestrator needs (artifact names and the
  verdict line). Resist adding role framing or process rules to them — that
  language leaks into the deliverable. Critic rubric profiles live in
  `prompts/profiles/` for opt-in additions; `decision-memo` is useful for
  non-software briefs, and `plan-spec` when the artifact is a build
  plan/specification.
