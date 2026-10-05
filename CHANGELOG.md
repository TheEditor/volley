# Change log

## Unreleased Go conversion

The Go engine records durable review state, frozen TOML settings, exact human
input, checked turn receipts, protected files and final approval. Direct and
Gashki adapters use the same review loop. Gashki recovery and cleanup keep exact
owned identities. Native commands supply structured inspection and recovery.

All deliberate changes C01–C15 and command departures D01–D06 are listed in
[the migration guide](docs/go-migration.md). This includes strict final verdicts,
missing-verdict handling, closing confirmation, planner mutation detection,
trust flag arity, legacy exit mapping, inbox consistency and crash limits.

Prepared launchers invoke one Go executable with explicit roles. They have no
runtime build, download or Bash fallback. The default-entry-point switch is
pending. Live vendor behavior remains unverified. Release publication needs
separate approval. No release-ready claim is made.
