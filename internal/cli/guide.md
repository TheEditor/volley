# Volley command guide

Use `volley plan WORKSPACE` to inspect settings and proposed arguments without a process launch or run creation. Use `volley run WORKSPACE` to start a review. A bare workspace is shorthand for run. Use explicit `run ./status` or `-- ./status` for a path whose name matches a command. A near command spelling is treated as a path only when that path exists. A bare invocation prints help.

Use `volley capabilities --json` for the parser declarations. Use `volley schema NAME --json` for a response or input schema. Every machine call has seven keys: `ok`, `tool_version`, `data`, `meta`, `warnings`, `commands`, and `errors`. The metadata includes request ID, UTC time, elapsed milliseconds, contract version, schema version and the hash of data. Success has no errors. Failure has a declared code; stderr repeats its first message. Child output is retained separately.

A planner writes the specification. A critic reviews its exact hash. A missing verdict is recorded as MISSING. It grants no approval. Final approval requires a checked critic receipt for the exact final spec. Advisory and closing passes cannot replace that approval. A round cap produces impasse. An uncertain send, completion, process or session produces handover. Saved state and partial artifacts remain.

A foreground run does not create a detached worker. At an unanswered question, it returns ANSWER_REQUIRED with the same run handle. `--wait` watches for an answer. `--interactive` also permits a terminal answer and requires `--wait` and human output. Stdin never starts an answer reader without this selector. Both roles receive each applied answer or directive. A question has no answer deadline.

The run/resume `--wait-timeout` is the per-turn execution budget. On get/events it is a read-wait budget. A read wait does not become an owner or stop one. Terminal approved attachments make no new agent calls. Resume uses frozen settings; a higher round cap and observation controls are permitted changes.

Default status, get, questions and doctor read saved files. They do not repair state or launch an agent. `--probe` permits declared external checks and reports their effects. A Gashki observation can append observation events. Stop uses a durable cooperative request. It retains uncertain processes and panes. It does not kill a saved PID. Explicit resolve requires checked evidence; a human assertion cannot permit retry. Abandon keeps artifacts and stops the run.

Native commands refuse a legacy Bash workspace. Read it with `workspace legacy-report`. An old APPROVE line is historical. Settle old execution and preserve its files. Copy the brief, constraints and decided requirements to a fresh workspace, then use `run NEW_WORKSPACE --seed OLD_WORKSPACE/SPEC.md --config SETTINGS.toml`. Do not copy old state or sessions. Import and reuse of legacy sessions are deferred.

Settings resolve flag, then file, then default. Config inspection starts no dependency. Config edits preserve comments and unknown bytes outside the edited values. Named config files must exist when read. A command prints active or inactive settings with their reasons. Display commands quote every argument. The program never executes those display strings.

Use `--deliver=stdout`, `--deliver=file:PATH`, or `--deliver=null` on declared inspection commands. File and null delivery serialize the result data as JSON. `config show --toml` selects raw TOML. File and null responses contain delivery metadata with byte count and SHA-256. An identical file is accepted; a different file needs `--force`. Output cannot replace a workspace manifest or source config. Raw TOML stdout cannot use `--json`; file-delivered TOML can use a machine metadata envelope. Unsupported schemes refuse before output writes.

Feedback saves only the supplied text in the local state directory. An exact repeated key is idempotent. Changed text with that key is refused. No feedback is sent over a network. No prompts or credentials are collected automatically.

The command has these departures from the general CLI standard:

- D01: settings have no environment-variable precedence or settings profiles.
- D02: run creation and execution stay in the foreground.
- D03: ownership uses OS locks, without time-based reservations.
- D04: durable reviews use `runs`; there is no `jobs` alias.
- D05: delivery supports stdout, file and null, without a webhook.
- D06: boolean settings take `true` or `false`; selectors take no value.

Retired setting variables are ignored with a warning. `SOURCE_DATE_EPOCH` sets response metadata only. It cannot change execution deadlines. Output has no ANSI codes or bells.

Recoverable Go panics return INTERNAL. Runtime fatal errors, SIGKILL and failed output writes cannot guarantee a complete envelope. Saved storage records remain the recovery authority. No deep package exits the process.

Real Gashki engine integration and generated conformance pins are separate checks in the implementation queue. Required live vendor checks need explicit approval for a concrete procedure with at most eight new conversations. A stub pass does not satisfy that gate. Publication needs separate approval. The source Bash launchers remain the default until the final gates pass.


Prepared legacy launchers accept native commands. Historical positional use waits.
Role launchers pass explicit planner flags. Installed launchers require an explicit
workspace; source-checkout launchers with no arguments select the checkout.
Legacy 0/2/1 mapping preserves native error evidence in JSON and leaves signal
exits 130/143 unchanged. Live vendor enforcement and the default switch remain
gated. See the repository migration guide for C01–C15 and D01–D06.
