# Process runner proof

The runner uses argument vectors and null standard input. It starts a new
process group. It has no dependency on `timeout` or `gtimeout`.

A positive budget includes the saved consumed duration. The remaining budget
uses Go's monotonic clock. An exhausted saved budget prevents a new launch.
After deadline or cancellation, the runner sends TERM to a verified owned
group. Remaining members have five seconds to stop. The runner then sends
KILL and uses a bounded settlement wait. Output pipes have a bounded drain.
A failed drain has an uncertain outcome. Cleanup time can exceed the turn
budget by the declared grace and bounded settlement intervals.

macOS identity uses the kernel process start time and boot time. Linux
identity uses `/proc` start ticks and boot ID. A saved PID or PGID alone does
not permit a signal. Group members are admitted only while a known start
identity anchors the group. If that identity cannot be proved, the runner
returns uncertainty. An absent saved PID is not proof of turn completion.

The test executable supplies all child fixtures. It tests a descendant that
ignores TERM and retains output, exact arguments with spaces and control
text, null input, failed starts, SIGINT, SIGTERM, controller SIGKILL, changed
start identity, partial identity, and failed or panicking start records.
Every test cleans up only its verified child group. macOS and Linux tests
record the observed group members and require zero remaining live members.

The tests do not claim public review checkpoint recovery or vendor behavior.
Those checks belong to the later engine and adapter tasks.
