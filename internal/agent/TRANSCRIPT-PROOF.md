# Bounded reply capture

T14 owns A-REPLY-01 and A-REPLY-02 (tier A, F-FILES). The synthetic JSONL samples
contain no private history. The checked identity root, workspace, session and
immutable pointer text bind discovery. A newest file does not supply identity.
Claude candidates are confined to the workspace project directory. Codex
candidates must have matching session metadata. Multiple prompt matches are
unavailable. Symlink, non-regular, oversized or changed candidates are refused.

The supported Claude subset has typed user prompts, assistant content blocks
and tool results. The last assistant text before the next typed user prompt is
selected. Sidechains, metadata prompts, compact summaries and team records do
not supply a reply. Supported Codex v1 records have session metadata, user
response items, task/turn start and task/turn completion events. The first
matching completion supplies the text. A new user turn, wrong turn identity,
failed completion or unknown record cannot supply that reply. This is an
explicit format subset, not a promise to parse every vendor version.

Public format references inspected for this implementation:

- [Claude Agent SDK session reader](https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/_internal/sessions.py)
- [Claude Code session locations](https://code.claude.com/docs/en/sessions)
- [Codex 0.159.0 protocol](https://github.com/openai/codex/blob/rust-v0.159.0/codex-rs/protocol/src/protocol.rs)

Capture starts at checked completion. Its two-second deadline also applies on
resume. An injected clock checks 1.5-second arrival, late arrival, absence,
read failure and an incomplete turn that later gains final text. File, byte,
record and directory limits bound discovery work. Reads use nonblocking opens
and require regular files; no FIFO can block the reply reader.

The receipt retains root, source path, supported format, observed vendor
version, session, prompt path, selected text hash and availability reason. Only
selected reply bytes and this metadata enter the workspace. The direct adapter
continues to use owned Claude print output or checked Codex events and the
owned last-message output. It now records the same format and source facts.

A missing planner reply warns only if the required artifact is valid. A
required critique remains mandatory. No specification text becomes invented
reply text. T17 and T22 wire capture into the complete engine.

Run the cases with:

```sh
bash scripts/test-isolated.sh test ./internal/agent -run '^TestTranscript' \
  -count=1 -v -args -- --reply-evidence OWNED_EVIDENCE_DIRECTORY
```

Cross-compiled tests use the same synthetic samples in the owned Linux machine.
No installed vendor command or live conversation is used. Stable snapshots and
hashes check consistency; they cannot prove who wrote a vendor transcript.
