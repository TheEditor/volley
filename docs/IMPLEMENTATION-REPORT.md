# Implementation and gate report

T01 through T23 have checked local implementation evidence. T24 completed the
concrete procedure and approval request. Approved execution failed during
preparation. The live gate is failed.
No live conversation or release ran. T25 is not complete.

The audit covers all 148 declared cases: 99 local implementation cases, 43
registry reproductions, one live preparation case, three conditional live
execution cases and two final audit cases. Each case retains its original
owner. This audit reuses checked evidence. It does not repeat every old test.

All 588 baseline IDs retain target cases and owners. No unexplained map loss
was found. C01-C15 and D01-D06 are documented. Current release and full-ci
profiles have zero failures with unchanged pins. Source checks found no deep
production exit/stdout calls, initializer functions or impure review imports.

The Go distribution contains one engine and prepared thin launchers. The root
Bash defaults still exist. Their replacement is deferred until the required
gates pass. The test-only Bash baseline is separately pinned and verified.
The canonical handoff remains outside the repository in the planning root,
as required by U14. There is no second current handoff.

The approved build-order change permits Volley implementation before Gashki
parser replacement. rr-run completion is verified. Gashki parser replacement
is not complete. No predecessor pass is inferred from that order change.

Required live facts remain unverified. The live gate is failed, and the final
default gate is pending. Release readiness
is not claimed. Publication needs separate approval and is not performed here.

The local implementation evidence is complete through T23. The complete
conversion remains incomplete until the gated default change is done.
macOS arm64 and Linux arm64 have runtime evidence. amd64 runtime is unverified.
Vendor permission, billing, session and model behavior are not proved by stubs.

The audit archive SHA-256 is `691383c28907e7324d65d5622719d902167695618829c977b028644d480b00a0`. It records source and evidence hashes,
nested record validation, profile results, baseline ownership and gate status.

## Remaining T25 work

The completed audit, baseline map, task-output checks, packaging behavior and
conformance profiles are reused. They are not scheduled again. When the
required gates permit the switch, copy the three checked launchers to the root,
verify their bytes and executable modes, and build one current release binary.
Check help, version, legacy metadata and installed no-argument behavior in one
small temporary layout. These startup checks make no provider call.

Update only the pending entries in this report and the existing handoff.
T25 does not repeat the T24 live procedure. It does not implement the Gashki
parser, publish a release, collect another complete evidence archive, or add a
collector. No full suite, race/fault campaign, role/session matrix or Linux VM
restart is scheduled for unchanged code. Broader checks need a relevant source
change or observed failure. Required behavior and approval gates remain intact.

## T25 startup result

One release build from commit `ba75ad97070cd6cf13b89328510152c2a7deaba7` passed all 12
native/launcher help, version and no-argument startup checks on macOS arm64.
The temporary installed layout made zero provider calls and no writes to its
binary directory. The fixture was removed. At that check, the engine, contract, prompts and
prepared launchers had no changes since the checked T23 implementation.
The existing integration and conformance results are reused.

Binary SHA-256: `7b40cc8c1ff644347363ba084a3916416432d335bd1d276a80cfaa84dd982eb0`.
Compact startup record SHA-256: `720b8658d375af90c3f04ca09974fc514bad9b520169c71af64c8d997263f238`.
The default-switch patch is prepared. Root commands remain unchanged pending
the required live result. Approval was received, but preparation failed.
T25 remains incomplete. The focused executable-hashing correction and its
checks are recorded in [the live result](LIVE-GATE-PROOF.md). No broad rerun
is required for this correction.

## Acceptance map

| Case | Owner | Status | Proof |
| --- | --- | --- | --- |
| A-SRC-01 | T01 | checked local evidence | [docs/go-migration.md](../docs/go-migration.md) |
| A-SRC-02 | T01 | checked local evidence | [docs/go-migration.md](../docs/go-migration.md) |
| A-BASE-01 | T02 | checked local evidence | [tests/scenarios/baseline.json](../tests/scenarios/baseline.json) |
| A-BASE-02 | T02 | checked local evidence | [tests/scenarios/baseline.json](../tests/scenarios/baseline.json) |
| A-CLI-01 | T03 | checked local evidence | [internal/contract/registry.json](../internal/contract/registry.json) |
| A-ARCH-01 | T04 | checked local evidence | [internal/cli/CLI-PROOF.md](../internal/cli/CLI-PROOF.md) |
| A-CFG-01 | T05 | checked local evidence | [internal/config/ADAPTER-PROOF.md](../internal/config/ADAPTER-PROOF.md) |
| A-CFG-02 | T05 | checked local evidence | [internal/config/ADAPTER-PROOF.md](../internal/config/ADAPTER-PROOF.md) |
| A-CFG-03 | T05 | checked local evidence | [internal/config/ADAPTER-PROOF.md](../internal/config/ADAPTER-PROOF.md) |
| A-PROC-01 | T06 | checked local evidence | [internal/process/PROCESS-PROOF.md](../internal/process/PROCESS-PROOF.md) |
| A-PROC-02 | T06 | checked local evidence | [internal/process/PROCESS-PROOF.md](../internal/process/PROCESS-PROOF.md) |
| A-PROC-03 | T06 | checked local evidence | [internal/process/PROCESS-PROOF.md](../internal/process/PROCESS-PROOF.md) |
| A-STATE-01 | T07 | checked local evidence | [internal/store/STORE-PROOF.md](../internal/store/STORE-PROOF.md) |
| A-STATE-02 | T07 | checked local evidence | [internal/store/STORE-PROOF.md](../internal/store/STORE-PROOF.md) |
| A-STATE-03 | T07 | checked local evidence | [internal/store/STORE-PROOF.md](../internal/store/STORE-PROOF.md) |
| A-STATE-04 | T07 | checked local evidence | [internal/store/STORE-PROOF.md](../internal/store/STORE-PROOF.md) |
| A-STATE-05 | T07 | checked local evidence | [internal/store/STORE-PROOF.md](../internal/store/STORE-PROOF.md) |
| A-CFG-04 | T08 | checked local evidence | [internal/config/SETTINGS-PROOF.md](../internal/config/SETTINGS-PROOF.md) |
| A-CFG-05 | T08 | checked local evidence | [internal/config/SETTINGS-PROOF.md](../internal/config/SETTINGS-PROOF.md) |
| A-CFG-06 | T08 | checked local evidence | [internal/config/SETTINGS-PROOF.md](../internal/config/SETTINGS-PROOF.md) |
| A-CFG-07 | T08 | checked local evidence | [internal/config/SETTINGS-PROOF.md](../internal/config/SETTINGS-PROOF.md) |
| A-CFG-08 | T08 | checked local evidence | [internal/config/SETTINGS-PROOF.md](../internal/config/SETTINGS-PROOF.md) |
| A-REC-01 | T09 | checked local evidence | [internal/agent/PREPARATION-PROOF.md](../internal/agent/PREPARATION-PROOF.md) |
| A-REC-02 | T09 | checked local evidence | [internal/agent/PREPARATION-PROOF.md](../internal/agent/PREPARATION-PROOF.md) |
| A-REC-03 | T09 | checked local evidence | [internal/agent/PREPARATION-PROOF.md](../internal/agent/PREPARATION-PROOF.md) |
| A-REC-04 | T09 | checked local evidence | [internal/agent/PREPARATION-PROOF.md](../internal/agent/PREPARATION-PROOF.md) |
| A-PROMPT-01 | T10 | checked local evidence | [internal/prompt/PROMPT-PROOF.md](../internal/prompt/PROMPT-PROOF.md) |
| A-PROMPT-02 | T10 | checked local evidence | [internal/prompt/PROMPT-PROOF.md](../internal/prompt/PROMPT-PROOF.md) |
| A-DIRECT-01 | T11 | checked local evidence | [internal/agent/DIRECT-PROOF.md](../internal/agent/DIRECT-PROOF.md) |
| A-DIRECT-02 | T11 | checked local evidence | [internal/agent/DIRECT-PROOF.md](../internal/agent/DIRECT-PROOF.md) |
| A-DIRECT-03 | T11 | checked local evidence | [internal/agent/DIRECT-PROOF.md](../internal/agent/DIRECT-PROOF.md) |
| A-DIRECT-04 | T11 | checked local evidence | [internal/agent/DIRECT-PROOF.md](../internal/agent/DIRECT-PROOF.md) |
| A-DIRECT-05 | T11 | checked local evidence | [internal/agent/DIRECT-PROOF.md](../internal/agent/DIRECT-PROOF.md) |
| A-GK-01 | T12 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-02 | T12 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-03 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-04 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-05 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-06 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-07 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-08 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-09 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-10 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-11 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-12 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-GK-14 | T13 | checked local evidence | [internal/gashki/GASHKI-ADAPTER-PROOF.md](../internal/gashki/GASHKI-ADAPTER-PROOF.md) |
| A-REPLY-01 | T14 | checked local evidence | [internal/agent/TRANSCRIPT-PROOF.md](../internal/agent/TRANSCRIPT-PROOF.md) |
| A-REPLY-02 | T14 | checked local evidence | [internal/agent/TRANSCRIPT-PROOF.md](../internal/agent/TRANSCRIPT-PROOF.md) |
| A-HUMAN-01 | T15 | checked local evidence | [internal/human/HUMAN-PROOF.md](../internal/human/HUMAN-PROOF.md) |
| A-HUMAN-02 | T15 | checked local evidence | [internal/human/HUMAN-PROOF.md](../internal/human/HUMAN-PROOF.md) |
| A-HUMAN-03 | T15 | checked local evidence | [internal/human/HUMAN-PROOF.md](../internal/human/HUMAN-PROOF.md) |
| A-HUMAN-06 | T15 | checked local evidence | [internal/human/HUMAN-PROOF.md](../internal/human/HUMAN-PROOF.md) |
| A-LOOP-01 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-LOOP-02 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-LOOP-03 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-LOOP-04 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-LOOP-05 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-LOOP-06 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-LOOP-07 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-HUMAN-05 | T16 | checked local evidence | [internal/engine/ENGINE-PROOF.md](../internal/engine/ENGINE-PROOF.md) |
| A-FINAL-01 | T17 | checked local evidence | [internal/engine/AUXILIARY-PROOF.md](../internal/engine/AUXILIARY-PROOF.md) |
| A-FINAL-02 | T17 | checked local evidence | [internal/engine/AUXILIARY-PROOF.md](../internal/engine/AUXILIARY-PROOF.md) |
| A-FINAL-03 | T17 | checked local evidence | [internal/engine/AUXILIARY-PROOF.md](../internal/engine/AUXILIARY-PROOF.md) |
| A-FINAL-04 | T17 | checked local evidence | [internal/engine/AUXILIARY-PROOF.md](../internal/engine/AUXILIARY-PROOF.md) |
| A-FINAL-05 | T17 | checked local evidence | [internal/engine/AUXILIARY-PROOF.md](../internal/engine/AUXILIARY-PROOF.md) |
| A-FINAL-06 | T17 | checked local evidence | [internal/engine/AUXILIARY-PROOF.md](../internal/engine/AUXILIARY-PROOF.md) |
| A-OPS-01 | T18 | checked local evidence | [internal/ops/OPS-PROOF.md](../internal/ops/OPS-PROOF.md) |
| A-OPS-02 | T18 | checked local evidence | [internal/ops/OPS-PROOF.md](../internal/ops/OPS-PROOF.md) |
| A-OPS-03 | T18 | checked local evidence | [internal/ops/OPS-PROOF.md](../internal/ops/OPS-PROOF.md) |
| A-OPS-04 | T18 | checked local evidence | [internal/ops/OPS-PROOF.md](../internal/ops/OPS-PROOF.md) |
| A-MIG-01 | T19 | checked local evidence | [internal/migration/MIGRATION-PROOF.md](../internal/migration/MIGRATION-PROOF.md) |
| A-MIG-02 | T19 | checked local evidence | [internal/migration/MIGRATION-PROOF.md](../internal/migration/MIGRATION-PROOF.md) |
| A-MIG-03 | T19 | checked local evidence | [internal/migration/MIGRATION-PROOF.md](../internal/migration/MIGRATION-PROOF.md) |
| A-MIG-04 | T19 | checked local evidence | [internal/migration/MIGRATION-PROOF.md](../internal/migration/MIGRATION-PROOF.md) |
| A-CLI-03 | T20 | checked local evidence | [internal/cli/CLI-PROOF.md](../internal/cli/CLI-PROOF.md) |
| A-CLI-04 | T20 | checked local evidence | [internal/cli/CLI-PROOF.md](../internal/cli/CLI-PROOF.md) |
| A-CLI-05 | T20 | checked local evidence | [internal/cli/CLI-PROOF.md](../internal/cli/CLI-PROOF.md) |
| A-CLI-06 | T20 | checked local evidence | [internal/cli/CLI-PROOF.md](../internal/cli/CLI-PROOF.md) |
| A-CLI-07 | T20 | checked local evidence | [internal/cli/CLI-PROOF.md](../internal/cli/CLI-PROOF.md) |
| A-CLI-02 | T21 | checked local evidence | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-01 | T21 | checked local evidence | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-02 | T21 | checked local evidence | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-03 | T21 | checked local evidence | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-04 | T21 | checked local evidence | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-05 | T21 | checked local evidence | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-GK-13 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-HUMAN-04 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-01 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-02 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-03 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-04 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-05 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-06 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-07 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-08 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-ALL-09 | T22 | checked local evidence | [internal/engine/INTEGRATION-PROOF.md](../internal/engine/INTEGRATION-PROOF.md) |
| A-PACK-01 | T23 | checked local evidence | [packaging/PACKAGING-PROOF.md](../packaging/PACKAGING-PROOF.md) |
| A-PACK-02 | T23 | checked local evidence | [packaging/PACKAGING-PROOF.md](../packaging/PACKAGING-PROOF.md) |
| A-PACK-03 | T23 | checked local evidence | [packaging/PACKAGING-PROOF.md](../packaging/PACKAGING-PROOF.md) |
| A-LIVE-01 | T24 | preparation complete; live gate failed | [docs/LIVE-GATE-PROOF.md](../docs/LIVE-GATE-PROOF.md) |
| A-LIVE-02 | T24 | failed at preparation | [docs/LIVE-GATE-PROOF.md](../docs/LIVE-GATE-PROOF.md) |
| A-LIVE-03 | T24 | not run; approved procedure stopped | [docs/LIVE-GATE-PROOF.md](../docs/LIVE-GATE-PROOF.md) |
| A-LIVE-04 | T24 | not run; conditional live approval | [docs/LIVE-GATE-PROOF.md](../docs/LIVE-GATE-PROOF.md) |
| A-DONE-01 | T25 | partial audit; default switch deferred | [docs/IMPLEMENTATION-REPORT.md](../docs/IMPLEMENTATION-REPORT.md) |
| A-DONE-02 | T25 | partial audit; default switch deferred | [docs/IMPLEMENTATION-REPORT.md](../docs/IMPLEMENTATION-REPORT.md) |
| A-CONTRACT-R01 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R02 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R03 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R04 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R05 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R06 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R07 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R08 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R09 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R10 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R11 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R12 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R13 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R14 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R15 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R16 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R17 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R18 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R19 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R20 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R21 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R22 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R23 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R24 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R25 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R26 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R27 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R28 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R29 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R30 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R31 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R32 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R33 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R34 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R35 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R36 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R37 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R38 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R39 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R40 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R41 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R42 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
| A-CONTRACT-R43 | T21 | checked registry reproduction | [internal/conformance/CONFORMANCE-PROOF.md](../internal/conformance/CONFORMANCE-PROOF.md) |
