# Use the Go review result

The Go engine records approval for exact specification bytes under the saved
review contract. Approval means that no material objection remains under that
contract. It does not validate the premises, approve implementation, or prove
that the proposed tool works. The engine does not add an approval badge to
`SPEC.md`.

The Bash command remains the default until the final gates pass.
This page describes the Go direct and Gashki backends. Local integration
checks passed. Live vendor behavior remains unverified.

## Read the additional reviews

If `second_opinion` is enabled, one fresh critic session supplies an advisory
review after the first ordinary approval. Read the complete review at
`rounds/second-opinion.md`. Its verdict cannot change the ordinary verdict.
The JSON result records the exact specification hash that this session reviewed.

`closing_pass` is enabled by default. When an ordinary confirmation round
remains, one planner turn reads the ordinary review and any advisory review.
The planner can accept or decline optional suggestions. The pass runs without
a search for particular words in the reviews. Read its reply at
`rounds/closing-response.md`.

The closing pass costs one planner turn. If it changes the specification, at
least one additional ordinary critic turn must approve the changed bytes.
A rejected change can need further planner and critic turns within the round
cap. If closing leaves the bytes unchanged and raises no question, the original
ordinary approval remains the basis. If it raises a question, answer the question
before confirmation. Each additional pass can run only once per run.

If the first approval uses the last allowed ordinary round, the engine skips
closing. The saved reason is `no_round_left`. It keeps the exact approved bytes
and any advisory review. Disabling closing also keeps the advisory review and
requires no additional planner turn.

## Read a restored result

If optional closing changes fail confirmation at the round cap, the engine can
restore `rounds/closing-approved.spec.md`. It does this only when the binding
inputs and identities are unchanged, no input or question is pending, every
owned turn is settled, and the current file still has the accepted planner
result's exact bytes and file identity. An unrelated edit is a conflict.
Changed inputs or an open question leave the run at impasse. An unsettled turn
requires recovery.

For a restored result, `closing_result` is `rejected_at_cap`. Read the rejected
bytes at `rounds/closing-rejected.spec.md`. Read each linked critique in
`rejected_review_evidence`. Each entry records the critique's hash, reviewed
specification hash, verdict, round, and receipt. These critiques reviewed the
rejected changes. They did not review the restored final artifact. Their
objections can remain useful. Restoration does not claim that the objections
were resolved.

The engine saves a restoration intent before replacement and a receipt before
final approval. A restart can finish that recorded replacement without another
agent turn. The final approval uses the original ordinary receipt for the exact
restored bytes. The JSON `final_result` also supplies the approval meaning,
input hash, additional pass records, and warnings. Input submitted after the
final commit is reported as pending and is not part of that approval.
