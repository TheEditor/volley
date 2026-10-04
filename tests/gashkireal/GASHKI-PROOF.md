# Pinned Gashki mechanics proof

T12 owns A-GK-01 and A-GK-02. These are tier-B cases with F-GK-REAL.
They passed on macOS arm64 and Linux arm64. Each OS run has 12 passing Go
test results and ten owned fixtures. No vendor conversation ran.

The fixture builder extracts tracked source from Gashki commit
`8eaecc9b31c965bab6c63a6a5562ebf38c43e363`. It builds one release binary with
no tags and one fault binary with `gashkitest`. It records the source archive,
binary, build-info, stub and public-contract hashes. It does not change the
Gashki checkout. Build identity comes from the archive and binary hash. The
version string is `0.0.0-dev` with contract 2.

The test compiles the pinned public schemas with a loader that cannot fetch
external resources. It checks every response envelope, success data schema,
exit binding, contract version and payload hash. Before a pane starts, it
checks version, exact capabilities, exact public schemas, required verbs,
flag arities, and the selected config root. A version string alone cannot pass.
The independent payload-hash verifier follows [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785).

Every fixture has new HOME, XDG, config, state and temporary roots. PATH holds
only owned Claude and Codex stub files and explicit utility links. The test
checks each provider file before spawn. Removing Codex gives AGENT_CLI_MISSING;
there is no vendor fallback. Inherited TMUX is absent at server creation.
The fresh named server uses a short, canonical socket path and `/dev/null`
configuration. Cleanup checks its recorded socket inode and saved process
identities. It stops only that server. Records and failed attempts remain.

The hook-capable Perl stub comes from the same pinned source. One added line
records each hook before it runs the real Gashki hook command. Both original
and changed stub hashes are saved. The test counts process argv, bracketed
pastes and hook events independently from Gashki responses. Codex app-server
probes are counted separately from agent processes.
The summary's `provider_spawns` counts successful fresh spawns. Independent
argv counts also include refused startup processes.

| Case | Stimulus | Required observations |
| --- | --- | --- |
| A-GK-01 | Release and fault builds; numeric and named fault triggers; named hold trigger | Tags and hashes differ; fault build injects INTERNAL; release capabilities and send succeed; hold directory stays empty; one process and paste |
| A-GK-02 | Both planner assignments, normal/here placement, idle/trust screens | Sixteen accepted agent processes and pastes; role tools, model and effort remain in argv; one merged Claude settings object retains caller hook, nine Gashki hooks and exact role rule arrays |
| A-GK-01/02 | Absent provider; unapproved trust; wrong workspace trust | No vendor fallback; two startup processes, zero pastes; refused panes removed; caller retained |

Each accepted pane has a checked UUID and tmux UUID option. The public idle
wait has a matching `pane_ready` event, name, actual socket and cursor. The
socket agrees with tmux metadata. A completed turn needs a later
`turn_ended` event from the hook source, qualified by the original turn cursor.
Each accepted agent independently records SessionStart, UserPromptSubmit and
Stop exactly once. UUID, group/name and tmux-target selectors are exercised.
An exact repeat retains its pane. A provider conflict returns exit 5.

The test window is 320 columns by 60 rows. Earlier 80-column here tests refused
wrapped folder-trust text. Those failed attempts remain. The passing result
applies to the declared test geometry. It does not prove all terminal layouts.

The stubs prove Gashki mechanisms. They do not prove vendor `dontAsk`
enforcement, effective settings precedence, or refusal of out-of-scope writes.
Those vendor facts still need the separately approved T24 live procedure.

To reproduce, supply a Gashki checkout and a new output directory:

```sh
python3 scripts/prepare-gashki-fixture.py \
  --source "$GASHKI_CHECKOUT" --output "$PROOF_ROOT" --target darwin/arm64
bash scripts/test-isolated.sh test -c -o "$PROOF_ROOT/proof-final.test" ./tests/gashkireal
env -i HOME="$PROOF_ROOT/home" PATH="$PROOF_ROOT/tools" TMPDIR="$PROOF_ROOT/tmp" \
  "$PROOF_ROOT/proof-final.test" -test.v -test.timeout=8m -- --fixture "$PROOF_ROOT"
```

For Linux, build with `--target linux/arm64` and compile the test with
`scripts/test-isolated.sh --target=linux/arm64`. Copy only the built binaries,
fixture records, stub and public-contract files to an isolated Linux machine.
The test needs tmux, Perl and standard utilities at the declared OS paths.
Ordinary `go test ./...` skips the tier-B cases when `--fixture` is absent.
That skip supplies no acceptance evidence. The collector refuses failed or
skipped runs. It records each stimulus, target, output and artifact hash.
