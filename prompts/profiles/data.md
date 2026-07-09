## Domain rubric: data

The plan under review manages data whose loss or divergence is material by default. In addition to the general bar, each of the following is a material defect:

- A state transition that can lose, duplicate, or corrupt committed data under crash, retry, or concurrent access.
- A migration with no stated forward path for existing data, or no stated answer on rollback (even if the answer is "none, and here is why that is acceptable").
- Two components or stores that can come to disagree about the same fact with no stated reconciliation mechanism.
- A stated consistency guarantee the design cannot actually deliver.

Active probes — check these even where the brief and the spec are silent; the silence itself can be the objection:

- What is the durability contract at each write? What exactly survives a SIGKILL at the worst possible moment?
- Is every retry idempotent, deduplicated, or explicitly at-least-once with downstream consumers told to cope?
- What happens when old code reads new data, and new code reads old data, mid-migration?
- Restore, not just backup: is the path from backup to running system actually specified and testable?

Severity calibration for this domain: silent data loss, unreconcilable divergence, and an undeliverable consistency claim block approval; performance of a correct path is a non-blocking remark at most.

These probes remain subject to the materiality bar above: raise a probe as an objection only when its answer would change what gets built, not to demonstrate coverage.
