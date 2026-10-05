# Contract proof

T21 implements `volley conformance`. The command generates probes from the
command and flag declarations. The parser manifest checks supported parser
nodes and real handler coverage. It does not use help text as a manifest.

The generated checks cover flag forms, aliases, scalar repetition, missing
values, empty values, enums, identifiers, literal tokens, command paths, output
modes, help, error envelopes, stage faults, and the report contract. Shared
global flag grammar runs once. Command flag scopes have their own probes.
Help permits parser checks without a model call. The CI registry checks exercise
filesystem and execution behavior through owned fixtures.

## Profiles and pins

- `full-ci` uses a build with `-tags=volleytest`. Each stage fault and the entry
  fault returns an `INTERNAL` envelope. Adjacent stage checks prove that the
  first reached fault wins.
- `release-self-check` uses an ordinary build. S-01, S-02, and X-02 have the fixed
  reason `release-build-fault-trigger-unavailable`. X-03 attempts the test
  environment variable and checks the release result. The release capabilities
  do not declare a test trigger. Full CI does not instantiate X-03.
- Streaming cases have the explicit reason `streams-deferred`.

[Profile pins](pins/) record instance IDs, targets, verdicts, and reasons.
IDs have sorted key segments. A new, removed, renamed, or changed instance fails
X-04. The failure also changes the report count. The negative pin test checks
these changes, including pass-to-not-applicable and a changed reason.

The initial pin review corrected family names and sorted ID segments. It added
machine-selector adjacency, identifier, literal free-text, output, stage, report,
and registry checks. Raw mode now calls the config handler and sends TOML to the
null sink. Every accepted probe passed. No failed result or unavailable change
was accepted. Tests do not write pins.

The report states its limits. Runtime probes cannot detect every direct
read of an environment variable. Source review must check new reads and their
declarations. Settings have no environment source. Raw TOML goes to file or
null; stdout carries a receipt envelope. Process and file error reproductions
run in CI, not in the deployed self-check.

## Native errors and warnings

A-CONTRACT-R01 through R43 have observed results. Each failure has its own request
ID, registered native exit, first error code, seven-key envelope, schema check,
and stderr message. R38 checks a real approved owned review. Signal cases use
separate controller processes and preserve a checkpoint. Lock refusal uses a
separate lock holder. The subsecond Gashki budget refuses a new wait.

The canned Gashki cases validate and retain raw subprocess responses. They check
server and pane conflicts, uncertain send retention, lost panes, split failure,
and cleanup warnings. Two real owned Unix sockets prove the server mismatch.
An uncertain send is checked a second time with no second send process.
The direct protocol failure occurs after turn intent. Real Gashki handler
integration remains the work of T22.

All six warning codes have observed causes. Turn warnings are saved and appear
in later status reads. Index failure leaves workspace reads available. An
uncertain mutation remains an error. Cleanup warning success retains the owned
pane and workspace artifact.

These checks found and corrected missing-value classification, dependency
classification, direct failure and timeout codes, context identity checks,
warning persistence, and mismatches between actual responses and their schemas.

## Response examples and artifact checks

[Golden index](../contract/testdata/goldens-index.json) pins the compressed
[response examples](../contract/testdata/goldens.json.gz). The examples come from
actual commands and durable records. They cover every command response schema,
help, version, errors, warnings, settings, manifest, receipt, input receipt,
inbox entry, final result, event page, and raw TOML delivery.

Only owned fixture paths and envelope elapsed time are normalized. The public
envelope data hash is recomputed after the path substitution. Raw evidence keeps
the original responses and hashes. Typed values, array order, artifact hashes,
and outcomes are kept. Tests verify each schema, each example pin, completeness,
and the envelope data hash. The extraction script is a manual review tool.

Repeated reads of the same saved data have identical typed data and `data_hash`.
Human and machine status output contain the same data. Built artifact checks
change `SOURCE_DATE_EPOCH` and observe a metadata change with unchanged data.
The incompatible raw/machine selector is refused before delivery.

macOS checks cover the shared contract packages, affected direct and engine
behavior, stage order, response witnesses, and both built profiles. Linux checks
cover file identity, locks, signals, a small approved review, and the release
artifact. Vet and diff checks pass. Earlier platform checks remain applicable.
Fixtures delete automatically. Owned fixture checks require 2 GiB free space and
limit files to 512 MiB. These checks start no live vendor conversation.
