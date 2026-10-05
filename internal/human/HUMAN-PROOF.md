# Question and input proof

T15 owns A-HUMAN-01, 02, 03 and 06. T16 owns whole-loop waiting and directive
application. T22 owns real Gashki pane settlement. No live vendor command runs
in this tier.

A generation records exact question bytes, a random ID, the originating planner
turn, next review round, and the baseline identity and content of HUMAN.md.
The baseline includes a canonical path. Whitespace-only questions do not open
a gate. A heading is nonempty and still opens a gate. File input requires two
equal reads separated by 250 ms. A new inode with identical bytes counts as new
input. Identical bytes in the same inode cannot prove another write. Use the
exact `human answer` command for that case.

The file interval detects changes during the two reads. It cannot prove that
an arbitrary writer has finished. Exact completion uses the command path.
Inputs have a 1 MiB limit. JSON input rejects unknown/repeated keys, nulls,
trailing objects, wrong types and invalid text. File input requires a regular
UTF-8 file. Terminal input preserves line breaks; an empty line completes the
candidate. EOF retains the candidate but does not submit it.

The short inbox lock checks the current question ID and commits the immutable
receipt and journal before acknowledgment. The first committed answer wins.
Later candidates remain in the journal and are not selected. Identical keyed
retries retain the first timestamp, including a fully written pending receipt
from a crash before publication. The same key with different input is refused. Steering
does not answer a question. Withdrawal requires acknowledgment or a stable
absent/empty question file. It archives the original question and does not
approve the proposed choice.

Application archives live in `state/human`, outside the inbox authority path.
Question, answer and prior directive bytes are immutable. A checkpoint binds
the application record and one fixed planner turn to the committed input hash.
A pending receipt or archive cannot select an application. Recovery reuses the
same application. Role delivery records require checked saved turn receipts
that name the input. The result checkpoint must settle before delivery is
recorded. The engine supplies the external turn and protected-set guard.

Source removal requires the consumed observation. Changed or replaced input
remains pending. The removed inode remains in private quarantine. Late writes
through an open descriptor are retained. A replacement during the rename is
restored only if no newer source exists; the retained inode is never deleted.
The engine also removes separately consumed prior input only under its saved
observation. Exact concurrent submissions use the inbox command path.

Answer settlement has a pane interface with no spawn or send method. A working
pane requires a subsequent checked turn-ended event after the saved observation
cursor. Checked current idle can settle a user-started answer turn. It cannot
complete a loop turn without its delivery cursor. A missing completion retains
an uncommitted answer candidate and returns handover. Clarification without an
answer keeps the gate. The protected-set comparison runs after failed calls as
well as successful calls. SPEC.md and QUESTIONS.md changes are refused.

The crash harness kills its actual controller by SIGKILL at receipt sync,
journal append/sync, each archive boundary, checkpoint publication and source
removal. It then reacquires OS ownership and checks recovery. The fixture
contains owned files and the test executable only. All subprocess environments
have an empty tool PATH. Tiny synthetic files supply no private conversations.

These checks prove record consistency, not writer identity. A planner with
workspace write access can forge coherent records. Post-turn protection detects
changes after the turn and does not prevent writes during the turn.

Run the tests with:

```sh
bash scripts/test-isolated.sh test ./internal/human -count=1 -v \
  -args -- --human-evidence OWNED_EVIDENCE_DIRECTORY
```

Cross-compiled tests run in the owned Linux machine with no shared home or
source mount. Failed attempts remain separate from passing evidence.
