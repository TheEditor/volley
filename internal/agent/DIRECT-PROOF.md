# Direct adapter proof

The adapter supplies primitive operations. It has no review loop or second
protected-file comparator. Execute calls the supplied GuardedTurnExecutor.
The later engine must check the protected set after successful and failed
operations before it trusts a completion or permits another action.

Arguments are arrays. Both providers use the workspace as cwd and null stdin.
Claude uses print text output, dontAsk, a declared --tools list, and one JSON
settings object. Only available read tools receive bare read-tool allowances.
Write access uses the escaped absolute workspace Edit rule. Planner rules deny
state, frozen configs, binding inputs, reference and skill roots, and each
committed history path. A direct critic has no Edit or Write tool or allow rule.
The same settings object carries the optional 1m context override.

The builder refuses unknown inherited permissions and broader write rules.
Local user and workspace settings sources are inspected and observed again
before launch. A caller must declare and check any additional selected policy
sources. This is not a claim to inspect remotely supplied managed policy.
An unverified inheritance selection refuses preparation. Rule enforcement and
the Edit-covers-Write relationship remain required live checks.

Codex uses exec --json, an explicit workspace-write or read-only sandbox,
skip-git-repo-check, and an owned output-last-message path. Resume supplies
the exact UUID and a sandbox_mode config override. It never selects --last.
The [official non-interactive guide](https://developers.openai.com/codex/noninteractive)
documents the JSONL event stream, final-message file, and exact-ID resume.
The parser saves a valid thread.started identity immediately. It rejects
malformed or incomplete JSON, unknown events, invalid identities, conflicting
or duplicate creation events, and incomplete success protocol. Raw output is
retained. Unknown model observations remain explicit warnings.

For persistent Claude calls, a UUID and its provider/role/model/effort/root/
command path and reported version are saved before launch. Codex saves the same binding when its
creation event arrives. Resume must match it. Failed resume and missing initial
identity never create another session. A durable launch intent prevents a
second process when completion is missing. A saved validated direct result
can finish without another process; retained files are synced before use.
Changed evidence or completed artifact bytes block that recovery.

Raw stdout, stderr, process start/exit facts, the final output, and identity
records have exact sink registrations. Raw output is exclusive and bounded.
The validated final reply is staged with same-directory immutable publication.
An absent planner reply can warn when its required specification is valid.
An absent or invalid specification or required critique fails the turn.
Failed and partial files remain in the owned evidence directory.

The T09 frozen gate runs at the process-call boundary. The billing guard also
runs before the call. The current Codex guide names CODEX_API_KEY; T11 adds
that indicator to the named environment and settings checks. No credential
values are saved. Other authentication-source limits remain in the preparation
proof. An API override still requires explicit selection.

Owned metadata checks read installed help for Claude 2.1.285 and Codex 0.159.3.
They use isolated HOME/XDG/temp folders and start no model conversation.
The fixture uses owned wrapper programs and the Go test executable only.
It cannot resolve a real provider from PATH. Tests independently record argv,
null stdin, cwd, process-start facts, stub launch counts, and artifact hashes.
A-DIRECT-01 through 05 cover both providers and roles, one-shot and persistent
calls, default and explicit choices, interrupted and invalid protocol, identity
sync/publication faults, optional and required artifacts, permission grammar,
guard/sink registration, changed settings, and unchanged launch counts on replay.
Stub results do not verify vendor permission enforcement or live model behavior.
