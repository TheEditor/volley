# Checked Gashki adapter proof

T13 supplies pane primitives. It has no review round loop. T16 owns the first
direct review loop. T22 owns Gashki executor integration and protected-set proof.

The public source pin is `8eaecc9b31c965bab6c63a6a5562ebf38c43e363`.
The source archive hash is
`7c67c1c039bf9fff4db5d6be1f2ee03f897b9e88a0262111f75b7c0fcea267b8`.
The embedded contract assets come from the pinned public capabilities and schema
responses. The client checks required verbs, flags, schemas and registry entries.
It checks strict JSON, the envelope, the exit, known error and warning codes, and
the canonical data hash. Public data numbers use RFC 8785. Owned records use the
separate integer-only Volley record format.

Every call retains its immutable intent, kernel start identity, raw output,
stderr, return fact and checked classification. Stream hashes must match the
retained files. Recovery checks the same paths and hashes. No adapter operation
reads the upstream private send ledger. All calls use the frozen config path and
an explicit environment. The outer engine must commit its ready intent through
the `BeforeMutation` callback and supply its protected-set executor.

The client accepts source proof only for the pinned source archive and recorded
command path. The fixture checks its build evidence separately before supplying those facts. A
caller must supply checked build provenance before source-based no-paste recovery.
A matching capability list alone does not prove the source behavior.

## Acceptance matrix

| Case | Tier and fixture | Stimuli and required observations |
| --- | --- | --- |
| A-GK-03 | A, canned owned process | Before spawn; lost reply; foreign existing UUID, provider and caller window; no uncertain kill |
| A-GK-04 | B, real pinned Gashki | Actual controller death after spawn return; same selector and ready UUID; changed caller window; unset socket with another actual server; 30-character group |
| A-GK-05 | B, real pinned Gashki | Actual controller SIGKILL before send and after return before delivery save; one paste; same-key replay and qualified wait |
| A-GK-06 | B, tagged Gashki | `send-barrier:kill`, `send-paste:kill`, `send-enter:kill`; initial paste counts 0, 1, 1; cleared trigger; public SEND_INTERRUPTED and barrier cursor; no additional paste |
| A-GK-07 | A, canned owned process | Changed key, payload, pane, frozen config, saved/queried state root, server and unsettled call; no recovery send |
| A-GK-08 | A, canned process and pure policy | All contextual mapping variants; retained upstream metadata/raw bytes; wrong-verb and registry changes; missing, malformed, hash/cursor and unverified warning; saved record changes |
| A-GK-09 | B plus A contradictory evidence | Real busy-to-idle recovery, foreign composer and dead pane; checked source refusal path; zero refused paste; same-key retry; contradictory fields forbid retry |
| A-GK-10 | B plus A idle-only evidence | Actual controller death after confirmation while a turn runs and after Stop; saved cursor re-wait; no new spawn/send; idle-only, wrong UUID and old sequence refused |
| A-GK-11 | A, fake clock and canned process | 500ms, 1s, residual under 1s, over 24h and unlimited; fixed elapsed budget; working/compacting continuation; idle/dead do not continue |
| A-GK-12 | B plus A policy | Owned terminal cleanup, already dead, tagged kill failure, changed ownership, active and unfinished turn; synthetic approved bytes remain exact |
| A-GK-14 | A, pure argument builder | Both providers and all three roles; custom tools, model, effort, context/skill roots and literal path patterns; one Claude settings object; no duplicate hook installation |

Each real case uses owned regular provider stubs and a unique isolated tmux
server. Provider PATH has no fallback to an installed vendor executable. The
real binary performs hook handling, pane discovery, paste, confirmation and
wait. Stub argv, keys and hook files supply independent process, paste and turn
counts. The separate tagged binary supplies named fault sites. Release-style
Gashki fault controls remain inert, as proved by T12.

The test controller itself dies by SIGKILL in the crash cases. The owner lock is
acquired again before recovery. Original raw files remain. A saved confirmation
is checked against its original call before reuse. A saved completion is checked
against the cursor-qualified wait records. A changed label is checked before
public observation can repair it. A missing tmux target cannot select another
pane by default. Cleanup warnings retain an inspection command and saved server
identity. They do not replace the final result.

## Test commands

Ordinary checks use `bash scripts/test-isolated.sh test ./...` and the race checks.
They skip the real fixture tier. A skip supplies no acceptance evidence.

Explicit fixture runs use:

```sh
bash scripts/test-isolated.sh test ./tests/gashkireal \
  -run '^TestAdapter' -count=1 -v -args -- --fixture FIXTURE_DIRECTORY
bash scripts/test-isolated.sh test ./internal/gashki \
  -count=1 -v -args -- --evidence OWNED_EVIDENCE_DIRECTORY
```

Linux runs use cross-compiled test executables in the owned Linux machine, with
no shared home or source mount. The proof collector requires passing logs and
hashes all regular retained artifacts. Failed attempts remain separate.

These tests prove the adapter and real Gashki mechanics with owned stubs. They
make no claim about live vendor permission enforcement. A Gashki Codex pane is
workspace-write for each role. The protected-set check detects changes after a
turn; it does not prevent changes during that turn. Crash recovery trusts
validated records on disk. Coordinated changes to a manifest and matching
transaction can evade consistency checks. The inbox journal proves consistency,
not writer identity; a workspace-write planner can forge matching records.

Normal Gashki calls and saved-call recovery record the command path instead of
a program-content fingerprint. Availability checks use file metadata. Source
proof describes the fixture build checked before execution; it does not attest
to current program contents. Stream, config, contract, prompt, and receipt hashes
still verify review data. Offline fixture evidence keeps its original hashes.
