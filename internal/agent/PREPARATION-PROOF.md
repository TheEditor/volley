# Preparation proof

T09 separates preparation checks from model adapters. It has no model launch
implementation. The later engine and backend tasks must use the same gate before
an external call.

The resolved Volley input carries the hash observed during settings resolution.
Preparation checks that hash and descriptor identity before each initial metadata
query and before committing records. It resolves only active executables once,
checks bounded version queries, and records their selected paths and reported versions. It
reads Gashki's TOML through `config show --toml`, parses the four pinned mechanism
settings, and obtains state_dir from `config get state_dir --json` data.value.
An unset tmux_socket remains absent in TOML. The queried state directory is not
recomputed from an environment setting.

Frozen Volley and Gashki records are private files promoted in a store
transaction. Different existing bytes are conflicts. Caller config files remain
unchanged. A private preparation receipt records original setting sources,
caller and server identity observations, executable bindings, requested model
and effort, and null observed model and effort with a reason. It never contains
auth-file bytes, credential values, or an environment dump. The manifest links
the receipt and frozen files to their exact observations.

The retained frozen gate checks files before every labeled direct launch or
Gashki config/spawn/send/wait/observe/status/kill callback. Later Gashki queries
use the saved file and verify the same mechanism values. Expected observations
do not come from a changed manifest. The gate also checks executable availability without reading program contents.
Resume checks root presence and values, HOME, socket path and file identity, and
active path/version records before a dependent callback. A cap increase
creates a control amendment; it does not change the cap in the saved settings.

Identity metadata distinguishes absent, present-empty, present-with-value, and
unknown. An absent root is not exported as an assumed default. Direct effective
roots use the checked caller identity. Gashki effective roots use explicit caller
root exports when present and read-only tmux metadata otherwise. Unknown roots
stay null with a reason and require an explicit billing override.

The billing guard checks named API environment indicators, Claude helper and
API environment indicators in effective and workspace settings, and a non-null
OPENAI_API_KEY in the effective Codex auth source. Unreadable or malformed guard
sources cause BILLING_REFUSED. Diagnostics contain indicator names, paths, and
fixed reasons. The explicit override stores two booleans. The direct nested
Claude environment builder removes only ANTHROPIC_BASE_URL, CLAUDECODE, and
CLAUDE_CODE_ENTRYPOINT when the nested marker is nonempty. It records those
names and preserves identity roots. Gashki keeps ownership of its own stripping.

Official [Claude settings documentation](https://code.claude.com/docs/en/settings)
confirms the user and workspace settings sources. Official
[Codex authentication documentation](https://developers.openai.com/codex/auth)
confirms the file auth source under CODEX_HOME and describes other credential
stores. This guard checks the declared sources; it does not claim to inspect an
OS credential store, remote managed policy, or every third-party provider.
Actual vendor behavior remains a separate live gate.

A-REC-01 through 04 use tier A F-FILES and F-GK-CANNED fixtures. Tests cover
record sync and promotion failures, unchanged caller files, round-cap replay,
active-only lookup, roots and unknown contexts, server and binary changes,
credential indicators and unreadable sources, every external-call label, and
an agent-changed manifest that cannot replace trusted expectations. Owned
metadata subprocesses run both backend preparation paths with an empty vendor
PATH. Other cases use recording callbacks. No vendor conversation is started.
The server metadata fixture records simulated binding files; real socket and
pane settlement are checked by the later real-Gashki fixture task.

Program-content fingerprints were removed by volley-23g.26. An execute-only
owned program fixture proves that preparation and resume need no program-file
read. Replaced contents at the same path and reported version are accepted.
Changed paths, versions, settings, billing sources, and conversation identity
retain their existing refusal rules. Program updates within a running review
are not detected by a content comparison. Resume checks the reported version.
