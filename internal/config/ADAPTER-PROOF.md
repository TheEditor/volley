# Input adapter proof

The selected TOML module is `github.com/pelletier/go-toml/v2` v2.4.3.
The stable decoder parses grammar and values. The `unstable` node API stays in
`adapter.go`. The review package does not import it.

`TestACFG01Grammar` checks empty input, CRLF, Unicode, escaped strings,
multiline basic and literal strings, arrays, and integer literals. It also
checks duplicate, dotted, quoted, and unknown keys, tables, invalid types,
bounds, invalid UTF-8, syntax errors, and the size limit. Diagnostics identify
the file, key, and 1-based key line for forbidden settings.

`TestACFG02ValueRangesAndFailedEdits` checks the exact bytes before and after
each changed value. The library's key-value node supplies the end of the full
value. This includes multiline arrays. Only the key separator is skipped to
find the start. No TOML grammar is reconstructed. Unchanged values keep their
original spelling. Missing keys are appended in registry order. Failed edits
return no candidate and leave the source bytes unchanged.

`TestACFG03StrictJSON` checks repeated names at all tested depths, escaped
repeated names, trailing values, invalid UTF-8, nulls, wrong types, unknown
fields, integer precision, and bounded depth. A valid 1 MiB input is accepted.
A 1 MiB plus one byte input is rejected. `TestReadStopsAtLimit` verifies that
an unlimited input source can supply no more than the limit plus one byte.

These are adapter checks. They do not claim CLI integration, atomic file
replacement, or the later settings resolver. A library update must repeat
these checks before use.
