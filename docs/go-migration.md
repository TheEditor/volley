# Go migration and limits

The Go executable has completed local stub and real-Gashki integration checks.
The prepared launchers are in `packaging/`. The default switch, live vendor
check and release readiness remain gated. Publication needs separate approval.
The approved build-order change permits Volley implementation before the Gashki
parser replacement. It does not claim that parser replacement is complete.

## Behavior changes

| ID | Go behavior |
| --- | --- |
| C01 | Saved phase, round, intent, receipt and artifact hashes replace file-count inference. `state/manifest.json` is the checked checkpoint. |
| C02 | Answers bind to question generations and observed files. Modification times do not authorize an answer. |
| C03 | The process runner enforces the turn duration. It does not need `timeout` or `gtimeout`. |
| C04 | The critic must supply exactly one final nonempty verdict line. An earlier APPROVE line is not approval. |
| C05 | After one missing-verdict retry, the recorded verdict is MISSING. It requests revision and never becomes approval. |
| C06 | The enabled closing pass reads ordinary and advisory reviews once, if a confirmation round remains. It saves a skip reason otherwise. |
| C07 | A closing change to specification bytes needs another ordinary critic approval. |
| C08 | A new directive gets a planner application turn before critique. Both roles then receive the same directive bytes and IDs. |
| C09 | A lost Gashki send can recover only with a checked same-key call and matching bindings. Unverifiable evidence hands over. An unqualified idle wait is insufficient. |
| C10 | Intended persistent identity is saved before launch. Observed identity is saved when received. |
| C11 | Each external action has ownership and retention records. Uncertain targets are retained. Approval cleans only checked owned idle panes. Impasse retains resumable panes. |
| C12 | Tool settings are frozen before dependent calls. Unknown vendor choices remain unknown. Reusable settings preserve types. |
| C13 | Commands use a versioned envelope, registry, schemas and generated conformance checks. Legacy exit mapping is explicit. |
| C14 | Billing checks inspect effective direct roots and the checked Gashki execution identity. An absent root variable stays absent. |
| C15 | Claude planners use `dontAsk`, declared Skill access and role-specific path rules. Context, skill sources, state, frozen configs, binding inputs and committed history are protected. New ordinary planner history is allowed; controller output names are reserved. A detected planner change returns `PLANNER_MUTATION` / 8 before another action. Live vendor enforcement is unverified. |

## Command-standard departures

| ID | Departure |
| --- | --- |
| D01 | Settings use flag > file > default. No setting environment precedence or settings profiles. |
| D02 | New reviews run in the foreground. There is no detached service. |
| D03 | Local OS locks hold controller ownership. There are no time-based reservations. Lock release does not prove that an agent stopped. |
| D04 | Durable reviews use `runs`. There are no `jobs` aliases. |
| D05 | Output delivery supports stdout, file and null. There is no webhook sink. |
| D06 | Boolean setting flags take `true` or `false`. Selectors take no value. |

Volley uses `--trust-folder=true` or `--trust-folder false`. Gashki's
`--trust-folder` and `--here` selectors take no value. Do not transfer their
arity to Volley. Trust applies to the full selected workspace. Read the
resolved plan before execution.

## Costs and safety limits

An eligible closing pass costs one planner turn. Changed closing bytes require
a critic confirmation and can require further revision turns within the cap.
An enabled advisory review uses one fresh session per run. It cannot change the
ordinary verdict. See [the result guide](go-review.md) for restored approvals.

Direct completion lost without a checked receipt hands over. Source-verified
Gashki missing-receipt recovery checks the saved key, prompt, pane, server and
inputs again. An arbitrary version string is not source proof. Abrupt controller
death with unanchored output files can require handover. There is no general
exactly-once delivery guarantee.

The inbox journal binds entries and receipts to the checked state chain. Input
submitted after final approval is pending and is outside that approval. A local
writer can forge `HUMAN.md`; filesystem access is not proof of a person's
identity. The command input path records exact accepted bytes and a receipt.
Pane answers require a later qualified completion before consumption. A changed
specification or question during that answer turn is a conflict.

Codex protection is detection after a turn. Its workspace sandbox does not
provide Volley's exact path rules. Claude permission argument checks are local
stub evidence until the live gate verifies actual vendor denial precedence and
edit/write behavior. Neither vendor is claimed to be safely enforced by stubs.

Native import, old session reuse, stream output and webhook delivery are
deferred. There is no daemon, remote state store, implementation agent or
credential setup. Approval covers the exact specification under the saved
review contract. It does not approve implementation or future gated actions.

Keep legacy evidence. Use `workspace legacy-report`, then start a fresh native
workspace from preserved inputs. Do not copy old state or session identities.

## Dependency program checks

Volley records the selected Claude, Codex, and Gashki command paths and their
reported versions. Preparation queries `--version`. Resume checks the same paths
and reported versions. Normal calls check command availability without reading
program contents. Default inspection checks availability without a version
process. Program-content fingerprints and file-size limits are removed.

A program update that keeps the same path and reported version is accepted.
Review files, settings, input receipts, conversation identity, and output evidence
retain their existing checks. Offline build and test evidence can still record
artifact hashes.

The pre-release manifest and preparation records now omit dependency `sha256`
fields. Earlier Go workspace records use the old format and are not migrated
automatically. Preserve their artifacts and start a fresh workspace with the
brief, constraints, and current specification.
