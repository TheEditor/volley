# Bash behavior fixtures

`baseline.json` maps every observed assertion from the pinned Bash matrix to
a target acceptance case. Scenario numbers group assertions from one owned
workspace. `bash-capture.json.gz` contains the complete captured prompts,
argv records, artifacts, mock transport records, and their original hashes.
These are Bash observations, not Go acceptance results or live vendor proof.

Run `python3 tests/capture-baseline.py` to create a new capture from Git commit
`a7252e474023f986d9912469b1e58742e644afc8`. The driver uses installed Bash and jq
with owned mock executables, isolated HOME/XDG/temp/tmux roots, and no vendor
executable fallback. It removes its own temporary root on completion.

The historical 588 assertions are evidence. They are not a Go test-count goal.
The target tests must check the applicable behavior and the declared changes.

Intentional changes from the recorded Bash expectations:

| Change | Target expectation |
| --- | --- |
| C01 | Commit phases, turn intents, receipts and artifact hashes. |
| C02 | Bind answers to question generations and stable observations. |
| C03 | Enforce a monotonic turn deadline and settle owned children. |
| C04 | Accept exactly one final non-empty verdict line. |
| C05 | Record a missing verdict as MISSING after one reminder. |
| C06 | Run enabled closing once when a confirmation round remains. |
| C07 | Review changed closing bytes before approval. |
| C08 | Apply each accepted directive through the planner before critique. |
| C09 | Recover missing send receipts only through checked same-key bindings. |
| C10 | Save intended/observed session identities before progression. |
| C11 | Record action ownership, retention and cleanup separately. |
| C12 | Freeze flag/file/default settings and executable bindings. |
| C13 | Declare flags, JSON responses, codes and native/wrapper exit mappings. |
| C14 | Check effective vendor identity roots without recording credentials. |
| C15 | Use declared permission rules and check all protected files. |

Setting environment variables in these fixtures belong to the Bash baseline.
Native setting variables are retired; direct boolean flags use explicit values.
The native wrapper maps approval 0 to 0, impasse 7 to 2, and other failures to 1.
Legacy workspace import and session reuse are deferred. Existing evidence must
remain unchanged; use the read-only legacy report and a fresh workspace.
