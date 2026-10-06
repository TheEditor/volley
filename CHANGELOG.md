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

The root launchers now invoke one Go executable with explicit roles. They have
no runtime build, download or Bash engine fallback. The required direct and
terminal live checks passed, including answers, resume, protected-file checks
and final-byte confirmation. The terminal test setup removes inherited
NO_COLOR before creating its owned server. Gashki source is unchanged.
Release publication needs separate approval; no release was published.
