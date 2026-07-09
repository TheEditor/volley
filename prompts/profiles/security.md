## Domain rubric: security

The plan under review sits in a domain where security defects are material by default. In addition to the general bar, each of the following is a material defect:

- A trust boundary crossed without stated validation or authentication.
- Attacker-supplied or externally-sourced strings reaching a shell, interpreter, query, template, or deserializer without a stated handling model.
- A secret (credential, token, key) whose storage, scope, or lifetime the spec leaves unstated, or that can reach logs or run artifacts.
- A privileged operation reachable without a stated authorization check.

Active probes — check these even where the brief and the spec are silent; the silence itself can be the objection:

- What is the trust model? Who or what is assumed hostile, and is that assumption written down anywhere?
- Where does input cross from untrusted to trusted context, and what happens to it at that crossing?
- How are secrets provisioned, rotated, and kept out of logs, error messages, and persisted state?
- Do the error and recovery paths preserve the security posture of the happy path, or quietly bypass it?

Severity calibration for this domain: a missing trust model, an unvalidated boundary crossing, or a secret with unstated lifetime blocks approval; hardening beyond the stated threat model is a non-blocking remark at most.

These probes remain subject to the materiality bar above: raise a probe as an objection only when its answer would change what gets built, not to demonstrate coverage.
