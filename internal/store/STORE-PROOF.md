# Durable store proof

T07 owns storage consistency. The review engine owns model selection, delivery
recovery, answer selection, and approval policy.

## Authority and recovery

`state/manifest.json` is the committed checkpoint. Immutable transactions bind
its previous bytes, next bytes, staged artifacts, and optional turn receipt.
Transactions and files are synced before checkpoint replacement. The replacement
uses a file in the same directory, then directory sync. `ReadyIntent` validates
committed history and syncs the checkpoint before an action can be released.
Recovery can finish a fully prepared transaction. It returns an active intent as
unfinished and returns a matching durable receipt separately. It cannot invent a
result from a nonempty output or restore partial `SPEC.md` bytes.

Immutable artifacts use exclusive publication. Mutable SPEC replacements use a
separate file, so later in-place edits cannot change the saved stage. Existing
unexpected round outputs are conflicts. Partial candidates stay as evidence.
Text cannot be staged over checkpoint authority, journal, or lock records.

The event ledger is a projection of committed transactions. Its complete prefix
must agree with transaction IDs and revisions. Only a final incomplete line can
be removed. A missing projection is repaired without an agent action. Projection
failure records `audit_pending` through a transaction when storage permits that
write. `EvidenceComplete` also compares the ledger to history, so failure of that
flag write cannot create a complete-evidence claim. Index failure is a separate
warning.

## Ownership and input commits

All owned path operations use directory descriptors and refuse symlinks at each
component. The store checks the physical workspace and named owner lock identity.
Owned directories are private. Locks use OS flock with a bounded wait. The same
physical workspace has one owner through path aliases. Process death releases
that lock; it does not settle a supplied active intent.

Human input uses a separate short lock. An immutable input receipt is synced
before its consecutive journal entry is appended and synced. A receipt without a
complete journal entry is pending evidence. Identical keyed retries preserve the
first receipt bytes and can finish its commit; conflicting payloads fail. Readers
validate the complete prefix, hash chain, run identity, and receipt bindings under
the inbox lock. Only an incomplete suffix can be truncated. Complete corruption
is retained and reported.

## Protected files

The controller keeps its pre-action inventory in memory. It includes state files,
state directories, owner lock, frozen configuration files, brief, constraints,
existing history, and the human channels required by the actor and turn kind.
After-action comparison does not read a changed manifest as expected state.
Authorized controller changes require exact before and after observations.
Concurrent input permissions cover only validated journal extensions and their
exact committed receipts. The earlier prefix bytes must remain identical.
Unreferenced receipts and new state files are changes. New history needs an
explicit permitted path; reserved controller names remain protected. Answer
turns protect SPEC and QUESTIONS and permit only a HUMAN append.

Journal consistency does not prove writer identity. A process with workspace
write permission can forge a coherent receipt and matching journal entry. The
store cannot authenticate such a writer. Backend write restrictions and review
engine policy are separate checks.

## Acceptance harness

- A-STATE-01: faults and abrupt test-process death before and after transaction,
  receipt, manifest, and directory sync boundaries; the active-intent gate;
  partial transaction rejection; retained partial SPEC bytes.
- A-STATE-02: synthetic result and final snapshots, saved receipt recovery,
  immutable output promotion, mutable SPEC replacement, repeated recovery,
  mismatched stages, missing receipts, and unexpected existing outputs.
- A-STATE-03: missing audit, trailing partial audit, corrupt complete audit,
  projection failure, complete-evidence refusal, and an actual failed filesystem
  index write.
- A-STATE-04: owner subprocesses through path aliases, owner SIGKILL with an
  unsettled intent, symlink refusal, separate input writers, interrupted input
  publication and journal acknowledgment, exact keyed retries, and corruption.
- A-STATE-05: actor and path observations for manifest, locks, transactions,
  configuration, prior critique, undeclared state, every reserved round form,
  unsafe paths and invalid text; exact controller deltas, journal extensions,
  rewritten prefixes, unreferenced receipts, and new permitted history.

These are tier A, F-FILES tests on macOS arm64 and Linux arm64. The subprocesses
are the compiled Go test executable. No vendor or model process is used. Faults
are local harness callbacks; production has no fault environment variable. These
tests prove abrupt process interruption and filesystem protocol behavior. They
cannot simulate every hardware power-loss or filesystem implementation. Later
loop tests prove public mutation exits and actual backend action order; later
approval tests prove approval policy.
