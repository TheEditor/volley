# Contract declaration boundary

`registry.json` is the one declaration source for command parsing, settings,
flags, output modes, error exits and generated probes. It describes the final
surface. Runtime capabilities publish only commands with registered handlers.
The unfinished declaration does not advertise an executable implementation.

The parser must project its manifest from these same declarations. Conformance
must compare all paths, flags, aliases, positionals and modes with that manifest.
It must fail on missing or extra paths. No handwritten second flag list is used.

All selector flags have arity zero. Setting booleans have arity one. Flag forms
and alias conflicts are declared. The same `--wait-timeout` spelling has a turn
budget meaning on run/resume and a read budget meaning on get/events.

Owned response and record schemas are closed JSON Schema 2020-12 objects.
Explicit external-format payloads are JSON Schema documents and upstream
evidence; those formats can have their documented additional fields. Every
required field of the checked upstream subset still requires validation.
Later record and handler tasks can narrow these initial declarations before
publication. They must keep the registry, schemas and generated checks aligned.

The 43 reproduction records are declarations for later installed probes.
`python3 tests/check-contract.py` checks this declaration boundary (A-CLI-01),
including malformed declarations. It does not claim parser or handler coverage.

The top-level handler must cover parsing, config loading and execution. It can
catch recoverable panics. Runtime fatal errors, SIGKILL and failed output writes
are outside its response guarantee. Native and legacy exits remain separate.
