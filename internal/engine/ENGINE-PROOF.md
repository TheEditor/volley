# Ordinary engine proof

T16 owns A-LOOP-01 through A-LOOP-07 and A-HUMAN-05. The review package
contains pure transition and verdict rules. The engine owns file operations,
input selection, external turns, receipts and final approval.

Every external turn has a committed intent. The direct adapter uses the same
turn guard for draft, directive, critique, reminder and revision purposes. The
guard compares protected files after success and failure. A registered output
path alone does not permit a change. The controller checks its output bytes
and open file identity before it records an allowed change. Agent changes to
the manifest, frozen config, input files, prior artifacts or reserved paths
block advancement. A config change before launch starts no agent process.

The first runnable slice uses the direct backend with both auxiliary settings
disabled. The Bash wrappers remain the default. T17 adds auxiliary passes.
T22 adds the complete Gashki loop. These tests start no live vendor conversation.

The owned Claude and Codex stubs run as real child processes. They read the
committed turn and private prompt, check null stdin, record arguments, write
the planner artifact and produce provider output. The tests check the saved
hashes, exact inputs, turn order and process counts. Both planner assignments,
seed and brief starts, and persistent and one-shot sessions are covered.

The verdict tests accept only an exact final verdict line. They retain CRLF
bytes. Quoted, embedded, fenced, lower-case and commented verdicts cause a
separate reminder. Two missing verdicts retain both replies and cause a
conservative revision. A final-round revision is saved before impasse.

Directive tests use one saved application and exact archives for both roles.
Question tests cover command, file and terminal answers, EOF, withdrawal and
steering. Steering alone does not answer a question. A waiting owner starts no
extra model turn. Interactive input requires a compatible terminal mode.

Attachment tests cover the status table, request keys, seed identity, frozen
settings, higher and lower caps, live owner exclusion and saved approval.
Invalid initial input creates no manifest and binds no key. Changed saved
artifacts are refused. An explicit recorded spec amendment causes a new review.
An input received after final commit is reported as pending.

The controller crash tests require actual SIGKILL at six boundaries for each
planner assignment: application commit, turn intent, checked completion, saved
receipt, result commit and final commit. Recovery uses checked evidence. An
uncertain direct turn is not repeated. A saved complete receipt can finish
its result without another agent call. A saved approval returns with zero calls.

The installed CLI matrix checks grammar, exact JSON envelopes and data hashes,
human input submission, keyed and unkeyed attachments, four actual SIGINT or
SIGTERM cases, and two private terminal cases. Each target OS retains 201
engine cases, 62 installed CLI cases, 12 controller SIGKILL cases, four
controller signal cases and two terminal cases. The pure verdict tests also
run on both targets. Each evidence record names its case, settings, target OS,
binary hash, artifact hashes and process count.

Run local checks with isolated state:

```sh
bash scripts/test-isolated.sh test ./internal/engine ./internal/review ./internal/cli
bash scripts/test-isolated.sh test -race ./internal/engine ./internal/agent ./internal/store ./internal/cli
bash scripts/test-isolated.sh vet ./...
```

Build a retained test binary and executable for each target. Execute the Linux
binaries in an owned Linux machine. Give the test binary an owned evidence
directory with `-- --engine-evidence DIRECTORY`. Run
`scripts/prove-engine-cli.py` with that binary and executable. Use
`scripts/collect-engine-proof.py` to check every retained artifact against its
record and collect source, binary and log hashes. A failed, skipped or incomplete
matrix cannot produce a complete proof record.

Hash and inode checks detect persistent changes between observations. They
cannot authenticate an arbitrary writer who can replace all related evidence,
or prove that a temporary write occurred and was fully restored between reads.
EOF is not a submitted answer. No partial artifact supplies completion or approval.
