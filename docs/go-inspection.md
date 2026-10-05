# Inspect a Go review

Use `volley status WORKSPACE`, `volley runs get SELECTOR`, or
`volley human questions WORKSPACE` to read saved review state. A selector is a
complete run ID or a workspace path. ID selection checks the workspace identity
after reading the derived index. A path works without that index. Inspection
does not start an agent or repair a checkpoint.

`volley doctor --workspace WORKSPACE` checks the saved settings and record
integrity. Without a workspace, it checks the selected configuration. Live
authentication and pane state remain `not checked`. An explicit `--probe`
result names the external checks and their possible effects. A Gashki
observation can append events; it is not a filesystem-only read.

`volley runs list` returns at most 25 entries by default, with a maximum of 100.
Use `--fields=status,created_at` to choose fields. Run ID and workspace are
always included. A page cursor fixes the registration bound and selected
fields. It cannot be reused with a different list or field selection.

`volley runs events SELECTOR` returns saved events and a resume cursor.
`--wait` waits for another event. A quiet event timeout returns an empty page
and the same cursor. `runs get --wait` instead returns `WAIT_TIMEOUT` when its
wait expires. A run without a controller returns its current action at once.
An interrupted inspection wait does not stop the controller.

Use `volley runs stop SELECTOR --dry-run` to read the effects. `--yes` submits a
durable stop request. A running controller cancels through its owned executor
and records acknowledgment. An absent controller records stopped state under
the workspace lock. An unknown owner blocks the request. The command retains
pane, process and uncertain-turn evidence. It does not kill a saved PID or an
unverified pane. A timeout means that acknowledgment is still pending; inspect
the same run.

`volley runs prune --older-than=24h --dry-run` lists eligible entries. `--yes`
removes only index entries for old terminal runs with no active owner, uncertain
turn, question or pending cleanup. It does not remove workspace files or vendor
data. The workspace remains readable by path.
