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
   To review a spec you already have, put it in the workspace as `SPEC.md`
   instead; no brief is needed, and the loop starts at the r01 critique.
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
and paths of interest (or name them in a seeded `SPEC.md`) so the agents
don't drown in an unfamiliar tree.

### Skills

A workspace file can name a skill as the standard (for example "the make-cli
skill is the standard"). Both agents can load it with no setup. Claude gets
its Skill tool and read-only allow rules for `~/.claude/skills` (or
`$CLAUDE_CONFIG_DIR/skills`) and for the target of each linked skill there.
Its system prompt names the skills folder and tells it to search there, not
in a parent folder. Codex reads skills from `$CODEX_HOME/skills`; a scratch
`CODEX_HOME` needs its own `skills/` links.

### Steering a running loop

Drop a `HUMAN.md` into the workspace at any time. At the next round it is
injected into both role prompts as a directive that outranks the critic —
any point it settles is settled, and neither agent may re-litigate it — then
archived to `rounds/rNN.human.md` so it applies exactly once. This is the
only way to steer a run without killing it (`^C` discards an in-flight
round).

### Questions for you

No one reads an agent's reply while the loop runs. So the planner is told to
write any decision only you can make to `QUESTIONS.md`, with options and a
recommendation, and to use its recommendation in `SPEC.md` for now. After a
planner turn that leaves a non-empty `QUESTIONS.md`, the loop prints it and
stops with exit `3`. With the gashki backend the panes stay up.

To answer, write `HUMAN.md` and rerun. A `HUMAN.md` older than
`QUESTIONS.md` does not count. The next round gives both agents the
questions and your answers as one directive, then archives them to
`rounds/rNN.questions.md` and `rounds/rNN.human.md`. A rerun with no answer
stops again. To go on without answers, delete `QUESTIONS.md` and rerun.

Only the planner writes `QUESTIONS.md`. The critic cannot write files; it
names such points in its critique.

Exit codes: `0` converged (critic approved), `2` impasse (round cap reached),
`3` the planner needs your decision (see `QUESTIONS.md`), `1` setup or
invocation failure.

## Files produced

| Path | Contents |
| --- | --- |
| `SPEC.md` | The living plan/artifact requested by `BRIEF.md` |
| `rounds/rNN.critique.md` | The critic's objections each round |
| `rounds/r00.response.md` | The planner's final reply after the first draft |
| `rounds/rNN.response.md` | The planner's final reply after it revised for that critique |
| `rounds/rNN.spec.md` | Spec snapshot after each revision |
| `rounds/second-opinion.md` | The swapped critic's advisory review (only with `VOLLEY_SECOND_OPINION=1`) |
| `rounds/rNN.closing-response.md` | The planner's final reply after a closing pass, if one ran |
| `rounds/rNN.human.md` | Archived one-shot `HUMAN.md` directive, if you steered round NN |
| `rounds/rNN.questions.md` | The planner's `QUESTIONS.md` that `rNN.human.md` answers, if any |
| `QUESTIONS.md` | Open questions from the planner; present only while the loop waits on you |
| `state/provenance.md` | Run provenance: role assignment, CLI versions, explicit model pins if any, context/profile settings |
| `state/*.log` | Full planner/critic transcripts and the loop log |
| `state/IMPASSE.md` | Written only if the round cap is hit without approval |

## Knobs (environment variables)

| Var | Default | Meaning |
| --- | --- | --- |
| `VOLLEY_PLANNER` | `claude` | Which agent plans (`claude` or `codex`); the other critiques |
| `MAX_ROUNDS` | `8` | Hard cap on critique/revise rounds |
| `CALL_TIMEOUT` | unset | No time limit by default. Set it to limit each agent invocation to that many seconds (CLI backend needs `timeout`/`gtimeout`; skipped if absent) |
| `CLAUDE_BIN` / `CODEX_BIN` | `claude` / `codex` | Binary overrides |
| `VOLLEY_CLAUDE_MODEL` / `VOLLEY_CODEX_MODEL` | unset | Optional explicit model pins passed as `--model`; when unset, `state/provenance.md` records that the CLI default was used and not known to Volley |
| (1M context) | | A Claude model that ends in `[1m]` (for example `claude-sonnet-5-5[1m]`) gets the 1M window. volley then also passes `--settings` with `CLAUDE_CODE_DISABLE_1M_CONTEXT=0`, so a global cap in your settings or env does not apply to volley's Claude calls |
| `VOLLEY_CLAUDE_EFFORT` / `VOLLEY_CODEX_EFFORT` | unset | Optional reasoning-effort pins. Claude takes `low`\|`medium`\|`high`\|`xhigh`\|`max` via `--effort` (validated up front); codex gets the value as `-c model_reasoning_effort=…` and validates it itself (valid set depends on the model). Recorded in `state/provenance.md`; unset means CLI default |
| `VOLLEY_CLOSING_PASS` | `1` | After APPROVE, one extra planner pass addresses or declines the critic's non-blocking remarks; `0` disables |
| `VOLLEY_SECOND_OPINION` | `0` | After APPROVE, the *other* agent reviews the final spec once (`rounds/second-opinion.md`); advisory only — its remarks feed the closing pass, it cannot flip the verdict |
| `VOLLEY_CONTEXT_DIR` | unset | Absolute path to an existing codebase both agents read (claude via `--add-dir`; codex's sandboxes read outside cwd natively). Read-only by contract: writes stay in the workspace. Name entry points in `BRIEF.md` to avoid context dilution |
| `VOLLEY_PROFILE` | unset | Append `prompts/profiles/<name>.md` to every critic prompt. Shipped: `security`, `data`, `decision-memo`, `plan-spec` |
| `VOLLEY_PERSISTENT` | `0` | `1` keeps one CLI session per role across rounds (claude `--session-id`/`--resume`, codex `exec resume`), so later rounds carry working memory instead of cold-starting from the files. Session ids live in `state/session.<role>`; the mode is pinned per workspace like the role assignment. The second opinion stays one-shot: fresh eyes are its point |
| `VOLLEY_BACKEND` | `cli` | `gashki` runs each role in a live tmux pane through the gashki CLI instead of one-shot `claude -p` / `codex exec` calls. See "gashki backend" below. Pinned per workspace |
| `GASHKI_BIN` | `gashki` | gashki binary for `VOLLEY_BACKEND=gashki` |
| `VOLLEY_TRUST_FOLDER` | unset | Set to `1` to pass `--trust-folder` when gashki starts an agent. Other values do not pass the flag. This gives gashki permission to answer a folder trust screen for the full workspace path. |

## gashki backend

With `VOLLEY_BACKEND=gashki`, volley starts one pane per role
(`volley-<run>/planner`, `volley-<run>/critic`) with `gashki spawn`, sends
each prompt with `gashki send` and waits for the turn with `gashki wait`. The
loop, the prompts and the verdict rule stay the same.

When `$TMUX_PANE` is set, volley asks gashki to place these panes beside the
caller with `--here`. A rerun from the same tmux window reuses its live panes.
A rerun from another window stops with `CONFLICT` and keeps the earlier panes.

- Needs `gashki` and `jq`, and a running tmux server on gashki's socket.
  gashki finds `claude` and `codex` on PATH; `CLAUDE_BIN` and `CODEX_BIN` are
  unused. `VOLLEY_PERSISTENT=1` is refused: the panes already persist.
- Each prompt is written to `state/prompts/<key>.md`; the pane gets a one-line
  pointer to it. The key (`<run>-init`, `<run>-rNN-critique`,
  `<run>-rNN-critique-2`, `<run>-rNN-revise`, `<run>-rNN-closing`,
  `<run>-second`) is the send's idempotency key, so a rerun after a crash
  reuses the same panes and never pastes a prompt twice.
- The critic writes its reply to the critique file itself. volley fails the
  run if the file is missing after the turn, or if SPEC.md changed during a
  critic turn. Claude runs with `--tools` limited to file tools and Skill,
  in `--permission-mode dontAsk`: a call that no rule allows is denied and
  the agent goes on, so no permission prompt can stop an unattended turn.
  Its rules allow edits in the workspace only; reads in the workspace and
  the context dir need no rule. Codex runs in gashki's `workspace-write`
  sandbox.
- A send that exits 7 (the agent may not have the prompt) is not resent:
  volley waits from the barrier cursor and lets the file check decide. With
  `CALL_TIMEOUT` unset, a wait has no limit: volley waits again while the
  pane is still working. With it set, a wait that uses it up stops the run. Any other gashki error stops the run with its code.
- On converge or impasse volley kills its panes and removes `state/run`.
  If a wait fails (timeout, approval prompt, dead pane), the turn may still
  be running, so the panes stay up: rerun to resume, or kill them with
  `gashki kill volley-<run>/<role> --yes` and remove `state/run` to start
  fresh. Any other failure kills the panes and removes `state/run`, so a
  rerun starts with new panes and new keys.

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
  missing. Prior critiques and spec snapshots give later rounds enough
  context to avoid re-litigating settled points.
- **Planner replies are kept, never requested.** No prompt asks the planner
  to write notes. volley saves its final reply itself: from stdout (claude)
  or `--output-last-message` (codex) on the CLI backend, and from the
  agent's own transcript on the gashki backend (`~/.claude/projects/...` or
  `$CODEX_HOME/sessions/...`). A reply it cannot find is logged, not fatal.
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
