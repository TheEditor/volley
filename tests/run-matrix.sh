#!/usr/bin/env bash
# Mock test matrix for volley.sh.
#
# Runs the loop against tests/mocks/{claude,codex,gashki} in throwaway workspaces,
# under both VOLLEY_PLANNER assignments, plus the billing-guard refusal paths.
# No real agent is invoked and no network is touched. Prints PASS/FAIL per
# assertion; exits nonzero if anything failed.
#
# Usage: tests/run-matrix.sh
set -u

TESTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(dirname "$TESTS_DIR")"
VOLLEY="$REPO/volley.sh"
MOCKS="$TESTS_DIR/mocks"

pass=0; fail=0
ok()  { echo "PASS: $*"; pass=$(( pass + 1 )); }
bad() { echo "FAIL: $*"; fail=$(( fail + 1 )); }
assert() { # assert <description> <command...>
  local d="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi
}

new_ws() { # fresh workspace with a brief, mock state dir, and isolated HOME
  WS="$(mktemp -d "${TMPDIR:-/tmp}/volley-matrix.XXXXXX")"
  echo "Build a mock thing that does mock work." >"$WS/BRIEF.md"
  MOCK="$WS/mock-state"
  FAKEHOME="$WS/home"
  mkdir -p "$MOCK" "$FAKEHOME"
}

volley_argv() { # volley_argv <planner> <max-rounds> <verdicts> [VAR=VAL ...] — sets CMD
  local planner="$1" max="$2" verdicts="$3"; shift 3
  CMD=(env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY -u VOLLEY_ALLOW_API_KEY -u VOLLEY_TRUST_FOLDER
    -u CLAUDE_CONFIG_DIR -u CODEX_HOME -u CALL_TIMEOUT -u VOLLEY_POLL -u TMUX_PANE
    HOME="$FAKEHOME" MOCK_STATE="$MOCK" MOCK_VERDICTS="$verdicts"
    CLAUDE_BIN="$MOCKS/claude" CODEX_BIN="$MOCKS/codex"
    VOLLEY_PLANNER="$planner" MAX_ROUNDS="$max"
    "$@" "$VOLLEY" "$WS")
}

run_volley() { # run_volley <planner> <max-rounds> <verdicts> [VAR=VAL ...]
  volley_argv "$@"
  "${CMD[@]}" </dev/null >"$WS/run.out" 2>&1
}

# A loop that waits for an answer runs in the background: start_volley starts
# it, the test answers, and end_volley collects the exit code.
start_volley() { # start_volley <planner> <max-rounds> <verdicts> [VAR=VAL ...]
  volley_argv "$@" VOLLEY_POLL=0.1
  "${CMD[@]}" </dev/null >"$WS/run.out" 2>&1 &
  VPID=$!
}

wait_for() { # wait_for <file> <pattern> — true once the file matches, within 10s
  local i
  for (( i = 0; i < 100; i++ )); do
    grep -qs -- "$2" "$1" && return 0
    sleep 0.1
  done
  return 1
}

end_volley() { # exit code of the background loop; 124 if it still runs after 10s
  local i
  for (( i = 0; i < 100; i++ )); do
    kill -0 "$VPID" 2>/dev/null || { wait "$VPID"; return; }
    sleep 0.1
  done
  kill "$VPID" 2>/dev/null; wait "$VPID" 2>/dev/null
  return 124
}

still_waiting() { # the background loop runs on, with no new agent call
  local calls="$(cat "$MOCK/critic-calls" 2>/dev/null):$(cat "$MOCK/planner-calls")"
  sleep 0.5
  kill -0 "$VPID" 2>/dev/null \
    && [[ "$calls" == "$(cat "$MOCK/critic-calls" 2>/dev/null):$(cat "$MOCK/planner-calls")" ]]
}

tty_volley() { # tty_volley <input> <planner> <max-rounds> <verdicts> [VAR=VAL ...]
  # Runs the loop on a pseudo-terminal in the background. Once the loop
  # waits for an answer, the input is typed on that terminal.
  local input="$1"; shift
  volley_argv "$@" VOLLEY_POLL=0.1
  if [[ "$(uname)" == Darwin ]]; then CMD=(script -q /dev/null "${CMD[@]}")
  else CMD=(script -qec "$(printf '%q ' "${CMD[@]}")" /dev/null); fi
  { wait_for "$WS/state/volley.log" 'waiting for an answer' && printf '%s' "$input"
    wait_for "$WS/state/volley.log" 'converged\|impasse'; } \
    | "${CMD[@]}" >"$WS/run.out" 2>&1 &
  VPID=$!
}

other() { [[ "$1" == claude ]] && echo codex || echo claude; }

for planner in claude codex; do
  critic="$(other "$planner")"

  # --- approve on first round ------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE"
  assert "[$planner] approve-first: exit 0" test $? -eq 0
  assert "[$planner] approve-first: SPEC.md written" test -f "$WS/SPEC.md"
  assert "[$planner] approve-first: r01 critique has verdict" \
    grep -q 'VERDICT: APPROVE' "$WS/rounds/r01.critique.md"
  assert "[$planner] approve-first: critic was $critic" \
    test -f "$MOCK/critic-$critic-01.prompt"
  assert "[$planner] approve-first: planner was $planner" \
    test -f "$MOCK/planner-$planner-01.prompt"
  assert "[$planner] approve-first: clean approval skips closing pass" \
    test "$(cat "$MOCK/planner-calls")" = 1
  assert "[$planner] approve-first: no second opinion by default" \
    test ! -e "$WS/rounds/second-opinion.md"
  assert "[$planner] approve-first: provenance written" \
    test -f "$WS/state/provenance.md"
  assert "[$planner] approve-first: provenance records role assignment" \
    grep -q "Planner: $planner" "$WS/state/provenance.md"
  assert "[$planner] approve-first: provenance records claude version" \
    grep -q "Claude version: mock-claude 1.2.3" "$WS/state/provenance.md"
  assert "[$planner] approve-first: provenance records codex version" \
    grep -q "Codex version: mock-codex 4.5.6" "$WS/state/provenance.md"
  assert "[$planner] approve-first: provenance records unpinned models" \
    grep -q "default/unrecorded by volley" "$WS/state/provenance.md"
  assert "[$planner] approve-first: planner prompt stays free of loop framing" \
    sh -c "! grep -qiE 'automated|counterpart|critic' '$MOCK/planner-$planner-01.prompt'"

  # --- revise then approve ---------------------------------------------------
  new_ws
  run_volley "$planner" 8 "REVISE APPROVE"
  assert "[$planner] revise-approve: exit 0" test $? -eq 0
  assert "[$planner] revise-approve: r01 spec snapshot kept" \
    test -f "$WS/rounds/r01.spec.md"
  assert "[$planner] revise-approve: spec revised" \
    grep -q 'mock revision entry' "$WS/SPEC.md"
  assert "[$planner] revise-approve: revise prompt asks for no response file" \
    bash -c 'grep -q "critique of SPEC.md" "$1" && ! grep -q response "$1"' _ "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] revise-approve: volley saved the init reply" \
    grep -q "mock $planner planner: call 1 done" "$WS/rounds/r00.response.md"
  assert "[$planner] revise-approve: volley saved the revise reply" \
    grep -q "mock $planner planner: call 2 done" "$WS/rounds/r01.response.md"
  assert "[$planner] revise-approve: planner log keeps the replies" \
    grep -q "mock $planner planner: call 2 done" "$WS/state/planner.log"
  assert "[$planner] revise-approve: two critiques" \
    test -f "$WS/rounds/r02.critique.md"
  assert "[$planner] revise-approve: no closing pass on clean approve" \
    test "$(cat "$MOCK/planner-calls")" = 2

  # --- closing pass: approve with non-blocking remarks --------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE_REMARKS"
  assert "[$planner] closing-pass: exit 0" test $? -eq 0
  assert "[$planner] closing-pass: one extra planner call" \
    test "$(cat "$MOCK/planner-calls")" = 2
  assert "[$planner] closing-pass: prompt asks for no response file" \
    bash -c 'grep -q "non-blocking remarks" "$1" && ! grep -q response "$1"' _ "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] closing-pass: volley saved the closing reply" \
    grep -q "mock $planner planner: call 2 done" "$WS/rounds/r01.closing-response.md"
  assert "[$planner] closing-pass: prompt points at approving critique" \
    grep -q 'rounds/r01.critique.md' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] closing-pass: spec got a disposition edit" \
    grep -q 'mock revision entry' "$WS/SPEC.md"
  assert "[$planner] closing-pass: critic not re-run" \
    test "$(cat "$MOCK/critic-calls")" = 1

  # --- closing pass disabled -----------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE_REMARKS" VOLLEY_CLOSING_PASS=0
  assert "[$planner] closing-pass-off: exit 0" test $? -eq 0
  assert "[$planner] closing-pass-off: no extra planner call" \
    test "$(cat "$MOCK/planner-calls")" = 1

  # --- second opinion: swapped critic reviews, closing pass covers both -----------
  new_ws
  run_volley "$planner" 8 "APPROVE_REMARKS APPROVE_REMARKS" VOLLEY_SECOND_OPINION=1
  assert "[$planner] second-opinion: exit 0" test $? -eq 0
  assert "[$planner] second-opinion: review written" \
    test -s "$WS/rounds/second-opinion.md"
  assert "[$planner] second-opinion: reviewer is the non-incumbent ($planner)" \
    test -f "$MOCK/critic-$planner-02.prompt"
  assert "[$planner] second-opinion: exactly two critic calls" \
    test "$(cat "$MOCK/critic-calls")" = 2
  assert "[$planner] second-opinion: one closing pass covers both" \
    test "$(cat "$MOCK/planner-calls")" = 2
  assert "[$planner] second-opinion: closing prompt points at it" \
    grep -q 'rounds/second-opinion.md' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] second-opinion: no placeholder residue in its prompt" \
    sh -c "! grep -q '{{HUMAN}}' '$MOCK/critic-$planner-02.prompt'"

  # --- second opinion clean + clean approval: nothing to dispose ------------------
  new_ws
  run_volley "$planner" 8 "APPROVE APPROVE" VOLLEY_SECOND_OPINION=1
  assert "[$planner] second-opinion-clean: exit 0" test $? -eq 0
  assert "[$planner] second-opinion-clean: two critic calls" \
    test "$(cat "$MOCK/critic-calls")" = 2
  assert "[$planner] second-opinion-clean: closing pass skipped" \
    test "$(cat "$MOCK/planner-calls")" = 1

  # --- HUMAN.md steering: applies to both roles for exactly one round --------------
  new_ws
  echo "Settled: output must be TSV. Drop the CSV idea." >"$WS/HUMAN.md"
  run_volley "$planner" 8 "REVISE APPROVE"
  assert "[$planner] steering: exit 0" test $? -eq 0
  assert "[$planner] steering: HUMAN.md consumed" test ! -e "$WS/HUMAN.md"
  assert "[$planner] steering: archived to rounds/r01.human.md" \
    test -f "$WS/rounds/r01.human.md"
  assert "[$planner] steering: critic r1 got the directive" \
    grep -q 'HUMAN DIRECTIVE' "$MOCK/critic-$critic-01.prompt"
  assert "[$planner] steering: critic r1 got the directive body" \
    grep -q 'Drop the CSV idea' "$MOCK/critic-$critic-01.prompt"
  assert "[$planner] steering: planner revise got the directive" \
    grep -q 'Drop the CSV idea' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] steering: round 2 prompt clean" \
    sh -c "! grep -q 'HUMAN DIRECTIVE' '$MOCK/critic-$critic-02.prompt'"
  assert "[$planner] steering: no placeholder residue" \
    sh -c "! grep -q '{{HUMAN}}' '$MOCK/critic-$critic-02.prompt'"

  # --- QUESTIONS.md: a planner question makes the loop wait for HUMAN.md ---------
  new_ws
  start_volley "$planner" 8 "REVISE APPROVE" MOCK_QUESTIONS=2
  assert "[$planner] questions: waits after the revision" \
    wait_for "$WS/state/volley.log" 'r01: planner left questions.*waiting for an answer'
  assert "[$planner] questions: no agent call while it waits" still_waiting
  assert "[$planner] questions: QUESTIONS.md kept for the user" test -f "$WS/QUESTIONS.md"
  assert "[$planner] questions: questions printed" \
    grep -q 'Mock question from planner call 2' "$WS/run.out"
  assert "[$planner] questions: user told to write HUMAN.md" \
    grep -q "write it to $(cd "$WS" && pwd)/HUMAN.md" "$WS/run.out"
  assert "[$planner] questions: no typing hint without a terminal" \
    sh -c "! grep -q 'type it here' '$WS/run.out'"
  assert "[$planner] questions: no pane hint without gashki" \
    sh -c "! grep -q 'planner pane' '$WS/run.out'"
  assert "[$planner] questions: revision snapshot written before the wait" \
    test -f "$WS/rounds/r01.spec.md"
  assert "[$planner] questions: no round 2 critique" test ! -e "$WS/rounds/r02.critique.md"
  assert "[$planner] questions: init prompt names QUESTIONS.md" \
    grep -q 'Write the questions to QUESTIONS.md' "$MOCK/planner-$planner-01.prompt"
  assert "[$planner] questions: revise prompt names QUESTIONS.md" \
    grep -q 'Write the questions to QUESTIONS.md' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] questions: cli prompt says the loop waits" \
    grep -q 'waits for an answer' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] questions: cli prompt has no pane answer rule" \
    sh -c "! grep -q 'word for word to HUMAN.md' '$MOCK/planner-$planner-02.prompt'"
  assert "[$planner] questions: critic prompt does not" \
    sh -c "! grep -q 'QUESTIONS.md' '$MOCK/critic-$critic-01.prompt'"
  echo "Old directive." >"$WS/HUMAN.md"; touch -t 202501010000 "$WS/HUMAN.md"
  assert "[$planner] questions: a HUMAN.md older than QUESTIONS.md is no answer" still_waiting
  echo "Answer to 1: use TSV." >"$WS/HUMAN.md"
  end_volley
  assert "[$planner] questions: answer ends the wait and the loop converges" test $? -eq 0
  assert "[$planner] questions: answer logged" \
    grep -q 'answer received in HUMAN.md' "$WS/state/volley.log"
  assert "[$planner] questions: QUESTIONS.md consumed" test ! -e "$WS/QUESTIONS.md"
  assert "[$planner] questions: wait marker removed" test ! -e "$WS/state/asked"
  assert "[$planner] questions: archived beside the answer" \
    test -f "$WS/rounds/r02.questions.md" -a -f "$WS/rounds/r02.human.md"
  assert "[$planner] questions: critic gets the questions" \
    grep -q 'Mock question from planner call 2' "$MOCK/critic-$critic-02.prompt"
  assert "[$planner] questions: critic gets the answer" \
    grep -q 'Answer to 1: use TSV.' "$MOCK/critic-$critic-02.prompt"
  assert "[$planner] questions: answer label on its own line" \
    grep -qx 'The user answered:' "$MOCK/critic-$critic-02.prompt"
  assert "[$planner] questions: answer starts its own line" \
    grep -qx 'Answer to 1: use TSV.' "$MOCK/critic-$critic-02.prompt"

  # --- QUESTIONS.md: an answer typed in volley's terminal, on a rerun ----------------
  new_ws
  start_volley "$planner" 8 "REVISE APPROVE" MOCK_QUESTIONS=2
  wait_for "$WS/state/volley.log" 'waiting for an answer'
  kill "$VPID"; wait "$VPID" 2>/dev/null
  echo "Steer: keep it short." >"$WS/HUMAN.md"; touch -t 202501010000 "$WS/HUMAN.md"
  tty_volley $'\n  \nAnswer: B\nline 2\n\n' "$planner" 8 "REVISE APPROVE"
  end_volley
  assert "[$planner] questions-tty: typed answer ends the wait" test $? -eq 0
  assert "[$planner] questions-tty: typing hint shown" grep -q 'type it here' "$WS/run.out"
  assert "[$planner] questions-tty: blank line before text is skipped" \
    test "$(grep -c . "$WS/rounds/r02.human.md")" = 3
  assert "[$planner] questions-tty: older directive kept above the answer" \
    grep -qx 'Steer: keep it short.' "$WS/rounds/r02.human.md"
  assert "[$planner] questions-tty: first typed line kept" \
    grep -qx 'Answer: B' "$WS/rounds/r02.human.md"
  assert "[$planner] questions-tty: second typed line kept" \
    grep -qx 'line 2' "$WS/rounds/r02.human.md"
  assert "[$planner] questions-tty: critic gets the typed answer" \
    grep -qx 'line 2' "$MOCK/critic-$critic-02.prompt"
  assert "[$planner] questions-tty: no temp file left" test ! -e "$WS/state/HUMAN.md.tmp"

  # --- QUESTIONS.md: the planner touches QUESTIONS.md as it copies the answer -------
  new_ws
  start_volley "$planner" 8 "REVISE APPROVE" MOCK_QUESTIONS=2
  wait_for "$WS/state/volley.log" 'waiting for an answer'
  touch -t 203001010000 "$WS/QUESTIONS.md"
  echo "Answer: A." >"$WS/HUMAN.md"
  end_volley
  assert "[$planner] questions-touched: HUMAN.md newer than the wait start answers" \
    test $? -eq 0
  assert "[$planner] questions-touched: critic gets the answer" \
    grep -q 'Answer: A.' "$MOCK/critic-$critic-02.prompt"

  # --- QUESTIONS.md: deleting it goes on without answers ----------------------------
  new_ws
  start_volley "$planner" 8 "REVISE APPROVE" MOCK_QUESTIONS=2
  wait_for "$WS/state/volley.log" 'waiting for an answer'
  rm -f "$WS/QUESTIONS.md"
  end_volley
  assert "[$planner] questions-deleted: loop goes on and converges" test $? -eq 0
  assert "[$planner] questions-deleted: logged" \
    grep -q 'QUESTIONS.md removed; going on without answers' "$WS/state/volley.log"
  assert "[$planner] questions-deleted: no directive in round 2" \
    sh -c "! grep -q 'HUMAN DIRECTIVE' '$MOCK/critic-$critic-02.prompt'"

  # --- QUESTIONS.md: from the initial draft, before any critique ---------------------
  new_ws
  start_volley "$planner" 8 "APPROVE" MOCK_QUESTIONS=1
  assert "[$planner] questions-init: waits after the draft" \
    wait_for "$WS/state/volley.log" 'r00: planner left questions.*waiting for an answer'
  assert "[$planner] questions-init: no critique yet" test ! -e "$WS/rounds/r01.critique.md"
  echo "Answer: yes." >"$WS/HUMAN.md"
  end_volley
  assert "[$planner] questions-init: answer ends the wait and the loop converges" test $? -eq 0
  assert "[$planner] questions-init: archived with round 1" \
    test -f "$WS/rounds/r01.questions.md"

  # --- QUESTIONS.md: a stopped wait waits again on rerun ----------------------------
  new_ws
  start_volley "$planner" 8 "REVISE APPROVE" MOCK_QUESTIONS=2
  wait_for "$WS/state/volley.log" 'waiting for an answer'
  kill "$VPID"; wait "$VPID" 2>/dev/null
  start_volley "$planner" 8 "REVISE APPROVE"
  assert "[$planner] questions-rerun: rerun waits at the start" \
    wait_for "$WS/state/volley.log" 'QUESTIONS.md has no answer yet; waiting for an answer'
  assert "[$planner] questions-rerun: no agent call before the answer" still_waiting
  echo "Answer to 1: use TSV." >"$WS/HUMAN.md"
  end_volley
  assert "[$planner] questions-rerun: answered rerun converges" test $? -eq 0
  assert "[$planner] questions-rerun: critic gets the answer" \
    grep -q 'Answer to 1: use TSV.' "$MOCK/critic-$critic-02.prompt"

  # --- QUESTIONS.md: a closing-pass question holds back convergence -----------------
  new_ws
  start_volley "$planner" 8 "APPROVE_REMARKS APPROVE" MOCK_QUESTIONS=2
  assert "[$planner] questions-closing: waits after the closing pass" \
    wait_for "$WS/state/volley.log" 'r01: planner left questions.*waiting for an answer'
  assert "[$planner] questions-closing: not logged as converged" \
    sh -c "! grep -q converged '$WS/state/volley.log'"
  echo "Answer: no." >"$WS/HUMAN.md"
  end_volley
  assert "[$planner] questions-closing: answer gets another review, then converges" \
    test $? -eq 0 -a -f "$WS/rounds/r02.critique.md"
  assert "[$planner] questions-closing: critic gets the answer" \
    grep -q 'Answer: no.' "$MOCK/critic-$critic-02.prompt"

  new_ws
  start_volley "$planner" 8 "APPROVE_REMARKS APPROVE" MOCK_QUESTIONS=2
  wait_for "$WS/state/volley.log" 'waiting for an answer'
  rm -f "$WS/QUESTIONS.md"
  end_volley
  assert "[$planner] questions-closing-deleted: converges with no new round" \
    test $? -eq 0 -a ! -e "$WS/rounds/r02.critique.md"

  # --- QUESTIONS.md: no round left to apply an answer ends in impasse ---------------
  new_ws
  start_volley "$planner" 1 "REVISE" MOCK_QUESTIONS=2
  end_volley
  assert "[$planner] questions-cap: last round does not wait; exits 2" test $? -eq 2
  assert "[$planner] questions-cap: logged" \
    grep -q 'r01: planner left questions in QUESTIONS.md, but no round is left' "$WS/state/volley.log"
  assert "[$planner] questions-cap: impasse report has the last critique" \
    grep -q 'VERDICT: REVISE' "$WS/state/IMPASSE.md"
  assert "[$planner] questions-cap: impasse report lists the open questions" \
    grep -q 'Mock question from planner call 2' "$WS/state/IMPASSE.md"
  start_volley "$planner" 1 "REVISE"
  end_volley
  assert "[$planner] questions-cap: rerun at the cap does not wait; exits 2" test $? -eq 2
  start_volley "$planner" 2 "REVISE APPROVE"
  assert "[$planner] questions-cap: rerun with a higher cap waits" \
    wait_for "$WS/state/volley.log" 'QUESTIONS.md has no answer yet; waiting'
  echo "Answer: yes." >"$WS/HUMAN.md"
  end_volley
  assert "[$planner] questions-cap: answered rerun converges in round 2" \
    test $? -eq 0 -a -f "$WS/rounds/r02.human.md"

  new_ws
  start_volley "$planner" 1 "APPROVE_REMARKS" MOCK_QUESTIONS=2
  end_volley
  assert "[$planner] questions-closing-cap: approval with open questions at the cap exits 2" \
    test $? -eq 2
  assert "[$planner] questions-closing-cap: report says the critic approved" \
    grep -q 'The critic approved, but' "$WS/state/IMPASSE.md"
  assert "[$planner] questions-closing-cap: not logged as converged" \
    sh -c "! grep -q converged '$WS/state/volley.log'"

  # --- CONSTRAINTS.md: injected into both roles and retained across rounds ---------
  new_ws
  echo "The plan must name the TSV wire format." >"$WS/CONSTRAINTS.md"
  run_volley "$planner" 8 "REVISE APPROVE"
  assert "[$planner] constraints: exit 0" test $? -eq 0
  assert "[$planner] constraints: initial planner got binding block" \
    grep -q 'CONSTRAINTS.md (binding)' "$MOCK/planner-$planner-01.prompt"
  assert "[$planner] constraints: critic got constraint body" \
    grep -q 'TSV wire format' "$MOCK/critic-$critic-01.prompt"
  assert "[$planner] constraints: revision planner got constraint body" \
    grep -q 'TSV wire format' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] constraints: no placeholder residue" \
    sh -c "! grep -q '{{CONSTRAINTS}}' '$MOCK/critic-$critic-02.prompt'"

  # --- second opinion: impasse path unaffected -------------------------------------
  new_ws
  run_volley "$planner" 2 "REVISE" VOLLEY_SECOND_OPINION=1
  assert "[$planner] second-opinion-impasse: exit 2" test $? -eq 2
  assert "[$planner] second-opinion-impasse: no second opinion" \
    test ! -e "$WS/rounds/second-opinion.md"

  # --- impasse at round cap ---------------------------------------------------
  new_ws
  run_volley "$planner" 2 "REVISE"
  assert "[$planner] impasse: exit 2" test $? -eq 2
  assert "[$planner] impasse: IMPASSE.md written" test -f "$WS/state/IMPASSE.md"

  # --- missing verdict triggers one re-ask -------------------------------------
  new_ws
  run_volley "$planner" 8 "NONE APPROVE"
  assert "[$planner] verdict-retry: exit 0" test $? -eq 0
  assert "[$planner] verdict-retry: critic asked twice" \
    test "$(cat "$MOCK/critic-calls")" = 2
  assert "[$planner] verdict-retry: still round 1" \
    test ! -e "$WS/rounds/r02.critique.md"

  # --- repo context: both agents pointed at a read-only codebase -------------------
  new_ws
  mkdir -p "$WS-ctx"
  CTXDIR="$(cd "$WS-ctx" && pwd -P)"
  echo "package main" >"$CTXDIR/main.go"
  run_volley "$planner" 8 "APPROVE" VOLLEY_CONTEXT_DIR="$CTXDIR"
  assert "[$planner] context: exit 0" test $? -eq 0
  assert "[$planner] context: planner prompt names the dir" \
    grep -q "$CTXDIR" "$MOCK/planner-$planner-01.prompt"
  assert "[$planner] context: critic prompt names the dir" \
    grep -q "$CTXDIR" "$MOCK/critic-$critic-01.prompt"
  if [[ "$planner" == claude ]]; then claude_argv="$MOCK/planner-claude-01.argv"
  else claude_argv="$MOCK/critic-claude-01.argv"; fi
  assert "[$planner] context: claude invoked with --add-dir" \
    grep -qx -- '--add-dir' "$claude_argv"
  assert "[$planner] context: --add-dir points at the dir" \
    grep -qx -- "$CTXDIR" "$claude_argv"
  rm -rf "$CTXDIR"

  # --- repo context: guards ---------------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE" VOLLEY_CONTEXT_DIR="relative/path"
  assert "[$planner] context-guard: relative path refused" test $? -eq 1

  new_ws
  run_volley "$planner" 8 "APPROVE" VOLLEY_CONTEXT_DIR="$WS/does-not-exist"
  assert "[$planner] context-guard: unreadable path refused" test $? -eq 1

  new_ws
  mkdir -p "$WS/inner"
  run_volley "$planner" 8 "APPROVE" VOLLEY_CONTEXT_DIR="$WS/inner"
  assert "[$planner] context-guard: dir inside workspace refused" test $? -eq 1

  new_ws
  run_volley "$planner" 8 "APPROVE" VOLLEY_CONTEXT_DIR="$WS"
  assert "[$planner] context-guard: workspace itself refused" test $? -eq 1

  # --- critic rubric profile ---------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE" VOLLEY_PROFILE=security
  assert "[$planner] profile: exit 0" test $? -eq 0
  assert "[$planner] profile: critic prompt carries the rubric" \
    grep -q 'Domain rubric: security' "$MOCK/critic-$critic-01.prompt"
  assert "[$planner] profile: planner prompt does not" \
    sh -c "! grep -q 'Domain rubric' '$MOCK/planner-$planner-01.prompt'"

  new_ws
  run_volley "$planner" 8 "APPROVE" VOLLEY_PROFILE=no-such-profile
  assert "[$planner] profile-guard: unknown profile refused" test $? -eq 1

  new_ws
  run_volley "$planner" 8 "APPROVE"
  assert "[$planner] profile-off: unprofiled critic prompt clean" \
    sh -c "! grep -q 'Domain rubric' '$MOCK/critic-$critic-01.prompt'"

  # --- explicit model pins ----------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE" \
    VOLLEY_CLAUDE_MODEL=mock-sonnet VOLLEY_CODEX_MODEL=mock-gpt
  assert "[$planner] models: exit 0" test $? -eq 0
  assert "[$planner] models: provenance records claude model" \
    grep -q 'Claude model: mock-sonnet' "$WS/state/provenance.md"
  assert "[$planner] models: provenance records codex model" \
    grep -q 'Codex model: mock-gpt' "$WS/state/provenance.md"
  if [[ "$planner" == claude ]]; then claude_argv="$MOCK/planner-claude-01.argv"; codex_argv="$MOCK/critic-codex-01.argv"
  else claude_argv="$MOCK/critic-claude-01.argv"; codex_argv="$MOCK/planner-codex-01.argv"; fi
  assert "[$planner] models: claude invoked with --model" \
    grep -qx -- '--model' "$claude_argv"
  assert "[$planner] models: claude model value passed" \
    grep -qx -- 'mock-sonnet' "$claude_argv"
  assert "[$planner] models: codex invoked with --model" \
    grep -qx -- '--model' "$codex_argv"
  assert "[$planner] models: codex model value passed" \
    grep -qx -- 'mock-gpt' "$codex_argv"

  # --- explicit effort pins ---------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE" \
    VOLLEY_CLAUDE_EFFORT=xhigh VOLLEY_CODEX_EFFORT=high
  assert "[$planner] effort: exit 0" test $? -eq 0
  assert "[$planner] effort: provenance records claude effort" \
    grep -q 'Claude effort: xhigh' "$WS/state/provenance.md"
  assert "[$planner] effort: provenance records codex effort" \
    grep -q 'Codex effort: high' "$WS/state/provenance.md"
  if [[ "$planner" == claude ]]; then claude_argv="$MOCK/planner-claude-01.argv"; codex_argv="$MOCK/critic-codex-01.argv"
  else claude_argv="$MOCK/critic-claude-01.argv"; codex_argv="$MOCK/planner-codex-01.argv"; fi
  assert "[$planner] effort: claude invoked with --effort" \
    grep -qx -- '--effort' "$claude_argv"
  assert "[$planner] effort: claude effort value passed" \
    grep -qx -- 'xhigh' "$claude_argv"
  assert "[$planner] effort: codex gets model_reasoning_effort override" \
    grep -qx -- 'model_reasoning_effort=high' "$codex_argv"

  # --- effort pins survive persistent resume ------------------------------------
  new_ws
  run_volley "$planner" 8 "REVISE APPROVE" VOLLEY_PERSISTENT=1 \
    VOLLEY_CLAUDE_EFFORT=xhigh VOLLEY_CODEX_EFFORT=high
  assert "[$planner] effort-persistent: exit 0" test $? -eq 0
  if [[ "$planner" == claude ]]; then claude_resume="$MOCK/planner-claude-02.argv"; codex_resume="$MOCK/critic-codex-02.argv"
  else claude_resume="$MOCK/critic-claude-02.argv"; codex_resume="$MOCK/planner-codex-02.argv"; fi
  assert "[$planner] effort-persistent: claude resume keeps --effort" \
    grep -qx -- '--effort' "$claude_resume"
  assert "[$planner] effort-persistent: codex resume re-receives the override" \
    grep -qx -- 'model_reasoning_effort=high' "$codex_resume"

  new_ws
  run_volley "$planner" 8 "APPROVE" VOLLEY_CLAUDE_EFFORT=extreme
  assert "[$planner] effort-guard: bad claude effort refused" test $? -eq 1
  assert "[$planner] effort-guard: message names the variable" \
    grep -q 'VOLLEY_CLAUDE_EFFORT' "$WS/run.out"

  new_ws
  run_volley "$planner" 8 "APPROVE"
  assert "[$planner] effort-off: claude argv carries no --effort" \
    sh -c "! grep -qx -- '--effort' \"$MOCK\"/*.argv"
  assert "[$planner] effort-off: codex argv carries no effort override" \
    sh -c "! grep -q 'model_reasoning_effort' \"$MOCK\"/*.argv"

  # --- role pinning ------------------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE"
  run_volley "$(other "$planner")" 8 "APPROVE"
  assert "[$planner] role-pin: swapped rerun refused" test $? -eq 1
  assert "[$planner] role-pin: message names original role" \
    grep -q "planner=$planner" "$WS/run.out"

  # --- persistent sessions: each role keeps one CLI session across rounds -------
  new_ws
  run_volley "$planner" 8 "REVISE APPROVE" VOLLEY_PERSISTENT=1
  assert "[$planner] persistent: exit 0" test $? -eq 0
  assert "[$planner] persistent: planner session recorded" \
    test -s "$WS/state/session.planner"
  assert "[$planner] persistent: critic session recorded" \
    test -s "$WS/state/session.critic"
  assert "[$planner] persistent: provenance records mode" \
    grep -q 'Persistent sessions: 1' "$WS/state/provenance.md"
  psid="$(cat "$WS/state/session.planner" 2>/dev/null)"
  csid="$(cat "$WS/state/session.critic" 2>/dev/null)"
  if [[ "$planner" == claude ]]; then
    assert "[$planner] persistent: claude planner opens with --session-id" \
      grep -qx -- '--session-id' "$MOCK/planner-claude-01.argv"
    assert "[$planner] persistent: recorded id matches the opener" \
      grep -qx -- "$psid" "$MOCK/planner-claude-01.argv"
    assert "[$planner] persistent: claude planner resumes on revision" \
      grep -qx -- '--resume' "$MOCK/planner-claude-02.argv"
    assert "[$planner] persistent: revision resumes the recorded id" \
      grep -qx -- "$psid" "$MOCK/planner-claude-02.argv"
    assert "[$planner] persistent: codex critic round 1 is a fresh exec" \
      grep -qx -- '--sandbox' "$MOCK/critic-codex-01.argv"
    assert "[$planner] persistent: codex critic round 2 resumes" \
      grep -qx -- 'resume' "$MOCK/critic-codex-02.argv"
    assert "[$planner] persistent: codex resume names the recorded id" \
      grep -qx -- "$csid" "$MOCK/critic-codex-02.argv"
    assert "[$planner] persistent: codex resume re-imposes read-only sandbox" \
      grep -qx -- 'sandbox_mode=read-only' "$MOCK/critic-codex-02.argv"
  else
    assert "[$planner] persistent: codex planner round 0 is a fresh exec" \
      grep -qx -- '--sandbox' "$MOCK/planner-codex-01.argv"
    assert "[$planner] persistent: codex planner resumes on revision" \
      grep -qx -- 'resume' "$MOCK/planner-codex-02.argv"
    assert "[$planner] persistent: codex resume names the recorded id" \
      grep -qx -- "$psid" "$MOCK/planner-codex-02.argv"
    assert "[$planner] persistent: codex resume re-imposes write sandbox" \
      grep -qx -- 'sandbox_mode=workspace-write' "$MOCK/planner-codex-02.argv"
    assert "[$planner] persistent: claude critic opens with --session-id" \
      grep -qx -- '--session-id' "$MOCK/critic-claude-01.argv"
    assert "[$planner] persistent: claude critic resumes round 2" \
      grep -qx -- '--resume' "$MOCK/critic-claude-02.argv"
    assert "[$planner] persistent: critic resume names the recorded id" \
      grep -qx -- "$csid" "$MOCK/critic-claude-02.argv"
  fi

  # --- persistent: verdict re-ask stays in the critic session --------------------
  new_ws
  run_volley "$planner" 8 "NONE APPROVE" VOLLEY_PERSISTENT=1
  assert "[$planner] persistent-retry: exit 0" test $? -eq 0
  assert "[$planner] persistent-retry: re-ask resumes the critic session" \
    sh -c "grep -qxE -- '--resume|resume' '$MOCK/critic-$critic-02.argv'"

  # --- persistent: second opinion and closing pass ------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE_REMARKS APPROVE_REMARKS" \
    VOLLEY_PERSISTENT=1 VOLLEY_SECOND_OPINION=1
  assert "[$planner] persistent-second: exit 0" test $? -eq 0
  assert "[$planner] persistent-second: second opinion stays one-shot" \
    sh -c "! grep -qxE -- '--resume|--session-id|resume' '$MOCK/critic-$planner-02.argv'"
  assert "[$planner] persistent-second: closing pass resumes the planner session" \
    sh -c "grep -qxE -- '--resume|resume' '$MOCK/planner-$planner-02.argv'"

  # --- persistent: mode is pinned per workspace ---------------------------------
  new_ws
  run_volley "$planner" 8 "REVISE APPROVE" VOLLEY_PERSISTENT=1
  run_volley "$planner" 8 "APPROVE"
  assert "[$planner] persistent-pin: mode flip on rerun refused" test $? -eq 1
  assert "[$planner] persistent-pin: message names VOLLEY_PERSISTENT" \
    grep -q 'VOLLEY_PERSISTENT=1' "$WS/run.out"

  # --- resume finishes an interrupted revision before re-running the critic -------
  new_ws
  run_volley "$planner" 1 "REVISE"
  rm "$WS/rounds/r01.spec.md" "$WS/state/IMPASSE.md"
  run_volley "$planner" 8 "REVISE APPROVE"
  assert "[$planner] resume-revision: exit 0" test $? -eq 0
  assert "[$planner] resume-revision: log names the catch-up" \
    grep -q 'resuming interrupted revision' "$WS/run.out"
  assert "[$planner] resume-revision: r01 spec snapshot recreated" \
    test -f "$WS/rounds/r01.spec.md"
  assert "[$planner] resume-revision: critic then reviews the revised spec" \
    test -f "$WS/rounds/r02.critique.md"
  assert "[$planner] resume-revision: no wasted critic call" \
    test "$(cat "$MOCK/critic-calls")" = 2

  # --- persistent off by default -------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE"
  assert "[$planner] persistent-off: no planner session file" \
    test ! -e "$WS/state/session.planner"
  assert "[$planner] persistent-off: no critic session file" \
    test ! -e "$WS/state/session.critic"
  assert "[$planner] persistent-off: provenance records mode off" \
    grep -q 'Persistent sessions: 0' "$WS/state/provenance.md"
done

# --- gashki backend: the same loop through a fake gashki (tests/mocks/gashki) --
GK=(VOLLEY_BACKEND=gashki GASHKI_BIN="$MOCKS/gashki")
gk_count() { grep -c -- "$1" "$MOCK/gk/calls" 2>/dev/null || true; }
gk_spawns() { wc -l <"$MOCK/gk/spawns" | tr -d ' '; }

for planner in claude codex; do
  critic="$(other "$planner")"

  new_ws
  run_volley "$planner" 8 "APPROVE" "${GK[@]}"
  assert "[gashki $planner] converge: exit 0" test $? -eq 0
  assert "[gashki $planner] converge: SPEC.md written" test -f "$WS/SPEC.md"
  assert "[gashki $planner] converge: critic wrote the critique file" \
    grep -q 'VERDICT: APPROVE' "$WS/rounds/r01.critique.md"
  assert "[gashki $planner] converge: critic was $critic" \
    test -f "$MOCK/critic-$critic-01.prompt"
  assert "[gashki $planner] converge: critic told where to write" \
    grep -q 'to the file rounds/r01.critique.md' "$MOCK/critic-$critic-01.prompt"
  assert "[gashki $planner] converge: prompt kept in state/prompts" \
    sh -c "ls '$WS/state/prompts/' | grep -q -- '-init.md\$'"
  assert "[gashki $planner] converge: planner pane spawned in the workspace" \
    test "$(gk_count "^spawn volley-.*/planner --agent=$planner --cwd=$(cd "$WS" && pwd)")" -ge 1
  assert "[gashki $planner] converge: sends carry an idempotency key" \
    test "$(gk_count '^send .*--idempotency-key=.*-r01-critique')" = 1
  assert "[gashki $planner] converge: planner pane killed" \
    test "$(gk_count '^kill volley-.*/planner --yes')" = 1
  assert "[gashki $planner] converge: critic pane killed" \
    test "$(gk_count '^kill volley-.*/critic --yes')" = 1
  assert "[gashki $planner] converge: state/run removed" test ! -e "$WS/state/run"
  assert "[gashki $planner] converge: provenance records backend" \
    grep -q 'Backend: gashki' "$WS/state/provenance.md"

  new_ws
  run_volley "$planner" 8 "REVISE APPROVE" "${GK[@]}"
  assert "[gashki $planner] revise-approve: exit 0" test $? -eq 0
  assert "[gashki $planner] revise-approve: second critique" \
    test -f "$WS/rounds/r02.critique.md"
  assert "[gashki $planner] revise-approve: panes reused across rounds" \
    test "$(gk_spawns)" = 2
  assert "[gashki $planner] revise-approve: init reply saved from the transcript" \
    grep -qx "mock $planner planner: call 1 done" "$WS/rounds/r00.response.md"
  assert "[gashki $planner] revise-approve: revise reply saved from the transcript" \
    grep -qx "mock $planner planner: call 2 done" "$WS/rounds/r01.response.md"

  new_ws
  start_volley "$planner" 8 "REVISE APPROVE" "${GK[@]}" MOCK_QUESTIONS=2
  assert "[gashki $planner] questions: waits after the revision" \
    wait_for "$WS/state/volley.log" 'r01: planner left questions.*waiting for an answer'
  assert "[gashki $planner] questions: no agent call while it waits" still_waiting
  assert "[gashki $planner] questions: panes stay up" test "$(gk_count '^kill ')" = 0
  assert "[gashki $planner] questions: user told to answer in the planner pane" \
    grep -q 'type it in the planner pane' "$WS/run.out"
  assert "[gashki $planner] questions: planner told to list the questions in its reply" \
    grep -q 'list the questions at the end of your reply' "$WS/state/prompts/$(cat "$WS/state/run")-r01-revise.md"
  assert "[gashki $planner] questions: planner told to copy a pane answer to HUMAN.md" \
    grep -q 'add the answer word for word to HUMAN.md' "$WS/state/prompts/$(cat "$WS/state/run")-r01-revise.md"
  echo "Answer to 1: use TSV." >"$WS/HUMAN.md"
  end_volley
  assert "[gashki $planner] questions: answer ends the wait and the loop converges" test $? -eq 0
  assert "[gashki $planner] questions: panes reused" test "$(gk_spawns)" = 2
  assert "[gashki $planner] questions: critic gets the answer" \
    grep -q 'Answer to 1: use TSV.' "$MOCK/critic-$critic-02.prompt"
  settle="$(grep -n '^wait volley-[^ ]*/planner --until=idle --wait-timeout' "$MOCK/gk/calls" | head -1 | cut -d: -f1)"
  crit2="$(grep -n '^send .*-r02-critique' "$MOCK/gk/calls" | head -1 | cut -d: -f1)"
  assert "[gashki $planner] questions: planner pane idle before the next turn" \
    test -n "$settle" -a -n "$crit2" -a "${settle:-0}" -lt "${crit2:-0}"

  new_ws
  run_volley "$planner" 8 "APPROVE_REMARKS" "${GK[@]}"
  assert "[gashki $planner] closing-pass: exit 0" test $? -eq 0
  assert "[gashki $planner] closing-pass: closing reply saved from the transcript" \
    grep -qx "mock $planner planner: call 2 done" "$WS/rounds/r01.closing-response.md"

  new_ws
  run_volley "$planner" 8 "REVISE APPROVE" "${GK[@]}" GK_FAULTS="r01-revise:notranscript"
  assert "[gashki $planner] no transcript: run still converges" test $? -eq 0
  assert "[gashki $planner] no transcript: no reply file" test ! -e "$WS/rounds/r01.response.md"
  assert "[gashki $planner] no transcript: logged" \
    grep -q "no planner reply found in the $planner transcript for r01-revise" "$WS/run.out"
done

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" TMUX_PANE=
assert "[gashki] outside tmux: exit 0" test $? -eq 0
assert "[gashki] outside tmux: no --here" \
  test "$(gk_count '^spawn .* --here')" = 0

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" TMUX_PANE=%human1
assert "[gashki] inside tmux: exit 0" test $? -eq 0
assert "[gashki] inside tmux: both spawns use --here" \
  test "$(gk_count '^spawn .* --here')" = 2

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}"
assert "[gashki] trust unset: no --trust-folder" \
  test "$(gk_count '^spawn .* --trust-folder')" = 0

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" VOLLEY_TRUST_FOLDER=1
assert "[gashki] trust 1: both spawns use --trust-folder" \
  test "$(gk_count '^spawn .* --trust-folder')" = 2

for value in '' 0 true 01; do
  new_ws
  run_volley claude 8 "APPROVE" "${GK[@]}" "VOLLEY_TRUST_FOLDER=$value"
  assert "[gashki] trust $value: no --trust-folder" \
    test "$(gk_count '^spawn .* --trust-folder')" = 0
done

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" VOLLEY_CLAUDE_MODEL=mock-sonnet
assert "[gashki] agent-args: claude planner gets model" \
  grep -q '"--model","mock-sonnet"' "$MOCK"/gk/panes/*_planner.args
assert "[gashki] agent-args: claude planner tools limited" \
  grep -q '"--tools=Read,Write,Edit,Glob,Grep,Skill"' "$MOCK"/gk/panes/*_planner.args
assert "[gashki] agent-args: no 1M override without [1m]" \
  sh -c "! grep -q 'DISABLE_1M' '$MOCK'/gk/panes/*_planner.args"

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" 'VOLLEY_CLAUDE_MODEL=mock-sonnet[1m]'
assert "[gashki] 1M model: exit 0" test $? -eq 0
assert "[gashki] 1M model: planner gets the model" \
  grep -q '"--model","mock-sonnet\[1m\]"' "$MOCK"/gk/panes/*_planner.args
assert "[gashki] 1M model: planner gets the cap override" \
  bash -c 'jq -r "index(\"--settings\") as \$i | .[\$i+1]" "$1" | jq -e ".env.CLAUDE_CODE_DISABLE_1M_CONTEXT == \"0\""' \
  _ "$(ls "$MOCK"/gk/panes/*_planner.args)"

new_ws
run_volley claude 8 "APPROVE" 'VOLLEY_CLAUDE_MODEL=mock-sonnet[1m]'
assert "[cli] 1M model: exit 0" test $? -eq 0
assert "[cli] 1M model: claude gets the cap override" \
  grep -qxF '{"env":{"CLAUDE_CODE_DISABLE_1M_CONTEXT":"0"}}' "$MOCK/planner-claude-01.argv"

new_ws
run_volley codex 8 "APPROVE" "${GK[@]}"
assert "[gashki] agent-args: claude critic tools limited" \
  grep -q '"--tools=Read,Glob,Grep,Write,Skill"' "$MOCK"/gk/panes/*_critic.args

new_ws
mkdir -p "$WS-ctx"
CTXDIR="$(cd "$WS-ctx" && pwd -P)"
run_volley claude 8 "APPROVE" "${GK[@]}" VOLLEY_CONTEXT_DIR="$CTXDIR"
assert "[gashki] context: claude gets --add-dir" \
  grep -q "\"--add-dir=$CTXDIR\"" "$MOCK"/gk/panes/*_planner.args
rm -rf "$CTXDIR"

# --- skills: claude may read the skills dir and each linked skill's target ---
new_skills() { # a skills dir in FAKEHOME: one plain skill, one linked skill
  mkdir -p "$FAKEHOME/.claude/skills/plain" "$WS/skill-src/linked"
  ln -s "$WS/skill-src/linked" "$FAKEHOME/.claude/skills/linked"
  SKDIR="$FAKEHOME/.claude/skills"
  SKREAL="$(cd "$SKDIR" && pwd -P)"
  SKLINK="$(cd "$WS/skill-src/linked" && pwd -P)"
}

new_ws; new_skills
run_volley claude 8 "APPROVE"
assert "[cli] skills: exit 0" test $? -eq 0
assert "[cli] skills: one --settings flag" \
  test "$(grep -cx -- '--settings' "$MOCK/planner-claude-01.argv")" = 1
assert "[cli] skills: read rule for the skills dir" \
  grep -qF "\"Read(/$SKDIR/**)\"" "$MOCK/planner-claude-01.argv"
assert "[cli] skills: read rule for the resolved skills dir" \
  grep -qF "\"Read(/$SKREAL/**)\"" "$MOCK/planner-claude-01.argv"
assert "[cli] skills: read rule for the linked skill's target" \
  grep -qF "\"Read(/$SKLINK/**)\"" "$MOCK/planner-claude-01.argv"
assert "[cli] skills: no rule for a plain skill" \
  sh -c "! grep -qF 'skills/plain' '$MOCK/planner-claude-01.argv'"
assert "[cli] skills: no write rules" \
  sh -c "! grep -qE '(Write|Edit)\(' '$MOCK/planner-claude-01.argv'"

new_ws; new_skills
run_volley claude 8 "APPROVE" 'VOLLEY_CLAUDE_MODEL=mock-sonnet[1m]'
assert "[cli] skills+1M: one --settings flag" \
  test "$(grep -cx -- '--settings' "$MOCK/planner-claude-01.argv")" = 1
assert "[cli] skills+1M: settings keep the cap override and the read rules" \
  bash -c 'grep -x "{.*}" "$1" | jq -e --arg r "Read(/$2/**)" ".env.CLAUDE_CODE_DISABLE_1M_CONTEXT == \"0\" and (.permissions.allow | index(\$r)) != null"' \
  _ "$MOCK/planner-claude-01.argv" "$SKLINK"

assert "[cli] skills: hint names the skills dir" \
  grep -qF "Skills are folders under $SKDIR," "$MOCK/planner-claude-01.argv"

new_ws
run_volley claude 8 "APPROVE"
assert "[cli] no skills dir: no --settings" \
  sh -c "! grep -qx -- '--settings' '$MOCK/planner-claude-01.argv'"
assert "[cli] no skills dir: no skills hint" \
  sh -c "! grep -qx -- '--append-system-prompt' '$MOCK/planner-claude-01.argv'"

new_ws; new_skills
run_volley codex 8 "APPROVE"
assert "[cli] skills: claude critic gets no write rule" \
  sh -c "! grep -qE '(Write|Edit)\\(' '$MOCK/critic-claude-01.argv'"

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}"
GKP="$(ls "$MOCK"/gk/panes/*_planner.args)"
gk_settings() { jq -r 'index("--settings") as $i | .[$i+1]' "$1"; }
assert "[gashki] dontAsk: planner runs in dontAsk mode" \
  grep -qF '"--permission-mode","dontAsk"' "$GKP"
assert "[gashki] dontAsk: one --settings flag" \
  test "$(jq '[.[] | select(. == "--settings")] | length' "$GKP")" = 1
assert "[gashki] dontAsk: workspace Edit rule" \
  bash -c 'jq -r "index(\"--settings\") as \$i | .[\$i+1]" "$1" | jq -e --arg r "Edit(/$2/**)" ".permissions.allow | index(\$r) != null"' \
  _ "$GKP" "$(cd "$WS" && pwd)"
assert "[gashki] dontAsk: no Edit rule for the context dir or home" \
  test "$(gk_settings "$GKP" | jq '[.permissions.allow[] | select(startswith("Edit("))] | length')" = 1

new_ws; new_skills
run_volley claude 8 "APPROVE" "${GK[@]}"
assert "[gashki] skills: planner gets the skills hint" \
  grep -qF "Skills are folders under $SKDIR," "$MOCK"/gk/panes/*_planner.args
assert "[gashki] skills: planner gets the read rules" \
  grep -qF "Read(/$SKLINK/**)" "$MOCK"/gk/panes/*_planner.args

new_ws; new_skills
run_volley codex 8 "APPROVE" "${GK[@]}"
assert "[gashki] skills: claude critic gets the read rules" \
  grep -qF "Read(/$SKLINK/**)" "$MOCK"/gk/panes/*_critic.args
assert "[gashki] dontAsk: claude critic runs in dontAsk mode" \
  grep -qF '"--permission-mode","dontAsk"' "$MOCK"/gk/panes/*_critic.args

new_ws
run_volley claude 8 "NONE APPROVE" "${GK[@]}"
assert "[gashki] re-ask: exit 0" test $? -eq 0
assert "[gashki] re-ask: critic asked twice" test "$(cat "$MOCK/critic-calls")" = 2
assert "[gashki] re-ask: own key" test "$(gk_count '^send .*-r01-critique-2')" = 1

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="r01-critique:send7"
assert "[gashki] send exit 7, file present: exit 0" test $? -eq 0
assert "[gashki] send exit 7: logged" grep -q 'unconfirmed' "$WS/run.out"
assert "[gashki] send exit 7: no resend" \
  test "$(gk_count '^send .*-r01-critique')" = 1

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="r01-critique:send7-lost"
assert "[gashki] send exit 7, file missing: exit 1" test $? -eq 1
assert "[gashki] send exit 7, file missing: names the file" \
  grep -q 'wrote no rounds/r01.critique.md' "$WS/run.out"

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="r01-critique:timeout-working3"
assert "[gashki] no limit, timeouts while working: exit 0" test $? -eq 0
assert "[gashki] no limit: waited again 3 times" \
  test "$(grep -c 'waiting again' "$WS/run.out")" = 3
assert "[gashki] no limit: observed the pane each time" \
  test "$(gk_count '^observe ')" = 3
assert "[gashki] no limit: 24h per wait" \
  test "$(gk_count '^wait .*--wait-timeout=24h')" = "$(gk_count '^wait ')"

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="r01-critique:timeout-working" CALL_TIMEOUT=5
assert "[gashki] CALL_TIMEOUT set, timeout while working: exit 1" test $? -eq 1
assert "[gashki] CALL_TIMEOUT set: names WAIT_TIMEOUT" grep -q 'WAIT_TIMEOUT' "$WS/run.out"
assert "[gashki] CALL_TIMEOUT set: passes the budget" \
  test "$(gk_count '^wait .*--wait-timeout=5s')" -ge 1
assert "[gashki] CALL_TIMEOUT set: no wait again" \
  test "$(grep -c 'waiting again' "$WS/run.out")" = 0

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="r01-critique:timeout-idle"
assert "[gashki] timeout while idle: exit 1" test $? -eq 1
assert "[gashki] timeout while idle: names WAIT_TIMEOUT" grep -q 'WAIT_TIMEOUT' "$WS/run.out"

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="init:approval"
assert "[gashki] approval: exit 1" test $? -eq 1
assert "[gashki] approval: names APPROVAL_REQUIRED" grep -q 'APPROVAL_REQUIRED' "$WS/run.out"

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="init:send2"
assert "[gashki] send exit 2: exit 1" test $? -eq 1
assert "[gashki] send exit 2: names the code" grep -q 'COMPOSER_NOT_EMPTY' "$WS/run.out"

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="init:send8"
assert "[gashki] send exit 8: exit 1" test $? -eq 1
assert "[gashki] send exit 8: names the code" grep -q 'SEND_INPUT_MIXED' "$WS/run.out"

assert "[gashki] send exit 8: panes killed" test "$(gk_count '^kill volley-.*/planner --yes')" = 1
assert "[gashki] send exit 8: state/run removed" test ! -e "$WS/state/run"
run_volley claude 8 "APPROVE" "${GK[@]}"
assert "[gashki] send exit 8: rerun exit 0" test $? -eq 0
assert "[gashki] send exit 8: rerun gets new panes" test "$(gk_spawns)" = 3

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="r01-critique:editspec"
assert "[gashki] critic edits SPEC.md: exit 1" test $? -eq 1
assert "[gashki] critic edits SPEC.md: says so" grep -q 'changed SPEC.md' "$WS/run.out"
assert "[gashki] critic edits SPEC.md: panes killed" test "$(gk_count '^kill volley-.*/critic --yes')" = 1

new_ws
run_volley claude 8 "REVISE APPROVE" "${GK[@]}" GK_FAULTS="r01-revise:timeout-idle"
assert "[gashki] resume: first run fails mid-revision" test $? -eq 1
run1="$(cat "$WS/state/run" 2>/dev/null)"
assert "[gashki] resume: run id kept after a failure" test -n "$run1"
assert "[gashki] resume: panes not killed after a failed wait" test "$(gk_count '^kill ')" = 0
assert "[gashki] resume: says how to discard the panes" grep -q "kept for a rerun" "$WS/run.out"
run_volley claude 8 "REVISE APPROVE" "${GK[@]}"
assert "[gashki] resume: rerun exit 0" test $? -eq 0
assert "[gashki] resume: no new panes" test "$(gk_spawns)" = 2
assert "[gashki] resume: revise key sent twice to the same pane" \
  test "$(gk_count "^send volley-$run1/planner .*--idempotency-key=$run1-r01-revise")" = 2
assert "[gashki] resume: revision turn ran once" \
  test "$(cat "$MOCK/planner-calls")" = 2
assert "[gashki] resume: panes killed at the end" \
  test "$(gk_count "^kill volley-$run1/")" = 2
assert "[gashki] resume: state/run removed" test ! -e "$WS/state/run"

new_ws
run_volley claude 8 "REVISE APPROVE" "${GK[@]}" GK_FAULTS="r01-revise:timeout-idle" TMUX_PANE=%human1
assert "[gashki] other window: first run keeps panes" test $? -eq 1
run1="$(cat "$WS/state/run" 2>/dev/null)"
run_volley claude 8 "REVISE APPROVE" "${GK[@]}" TMUX_PANE=%human2
assert "[gashki] other window: resumed spawn refused" test $? -eq 1
assert "[gashki] other window: conflict reported" grep -q CONFLICT "$WS/run.out"
assert "[gashki] other window: existing panes kept" test "$(gk_count '^kill ')" = 0
assert "[gashki] other window: run id kept" test "$(cat "$WS/state/run" 2>/dev/null)" = "$run1"
run_volley claude 8 "REVISE APPROVE" "${GK[@]}" TMUX_PANE=%human1
assert "[gashki] original window: resumed run exits 0" test $? -eq 0
assert "[gashki] original window: no new panes" test "$(gk_spawns)" = 2

new_ws
run_volley claude 8 "APPROVE_REMARKS APPROVE_REMARKS" "${GK[@]}" VOLLEY_SECOND_OPINION=1
assert "[gashki] second opinion: exit 0" test $? -eq 0
assert "[gashki] second opinion: review written" test -s "$WS/rounds/second-opinion.md"
assert "[gashki] second opinion: own pane, killed after" \
  test "$(gk_count '^kill volley-.*/second --yes')" = 1
assert "[gashki] second opinion: closing pass ran" \
  test "$(cat "$MOCK/planner-calls")" = 2

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" VOLLEY_PERSISTENT=1
assert "[gashki] guard: VOLLEY_PERSISTENT=1 refused" test $? -eq 1

new_ws
run_volley claude 8 "APPROVE" VOLLEY_BACKEND=tmux
assert "[gashki] guard: unknown backend refused" test $? -eq 1

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" GASHKI_BIN="$WS/no-such-gashki"
assert "[gashki] guard: missing gashki refused" test $? -eq 1

new_ws
run_volley claude 1 "REVISE"
run_volley claude 8 "APPROVE" "${GK[@]}"
assert "[gashki] backend-pin: switch on rerun refused" test $? -eq 1
assert "[gashki] backend-pin: names VOLLEY_BACKEND" grep -q 'VOLLEY_BACKEND=cli' "$WS/run.out"

# --- billing guard (role-independent) -----------------------------------------
new_ws
run_volley claude 8 "APPROVE" ANTHROPIC_API_KEY=sk-test
assert "guard: ANTHROPIC_API_KEY refused" test $? -eq 1
assert "guard: names the variable" grep -q ANTHROPIC_API_KEY "$WS/run.out"

new_ws
run_volley claude 8 "APPROVE" OPENAI_API_KEY=sk-test
assert "guard: OPENAI_API_KEY refused" test $? -eq 1

new_ws
mkdir -p "$FAKEHOME/.claude"
echo '{"apiKeyHelper": "/usr/local/bin/helper"}' >"$FAKEHOME/.claude/settings.json"
run_volley claude 8 "APPROVE"
assert "guard: apiKeyHelper refused" test $? -eq 1

new_ws
mkdir -p "$FAKEHOME/.codex"
echo '{"OPENAI_API_KEY": "sk-live-string"}' >"$FAKEHOME/.codex/auth.json"
run_volley claude 8 "APPROVE"
assert "guard: auth.json string key refused" test $? -eq 1

new_ws
mkdir -p "$FAKEHOME/.codex"
echo '{"OPENAI_API_KEY": null}' >"$FAKEHOME/.codex/auth.json"
run_volley claude 8 "APPROVE"
assert "guard: auth.json null key allowed" test $? -eq 0

# --- shipped profile fragments exist and restate the materiality bar -----------
for prof in security data decision-memo plan-spec; do
  assert "profiles: $prof fragment shipped" \
    test -s "$REPO/prompts/profiles/$prof.md"
done

# --- seeded spec: SPEC.md without BRIEF.md starts at the r01 critique ----------
new_ws
rm "$WS/BRIEF.md"
echo "# Seed spec" >"$WS/SPEC.md"
run_volley claude 8 "APPROVE" VOLLEY_CONTEXT_DIR="$REPO"
assert "seed: SPEC.md without BRIEF.md runs, exit 0" test $? -eq 0
assert "seed: no initial draft (planner not called)" test ! -f "$MOCK/planner-calls"
assert "seed: r01 critique has verdict" \
  grep -q 'VERDICT: APPROVE' "$WS/rounds/r01.critique.md"
assert "seed: SPEC.md kept as given" grep -qx '# Seed spec' "$WS/SPEC.md"
assert "seed: context block without BRIEF.md in the critic prompt" \
  bash -c 'grep -q "reference codebase" "$1" && ! grep -q BRIEF "$1"' \
  _ "$MOCK/critic-codex-01.prompt"

# --- setup failure -------------------------------------------------------------
new_ws
rm "$WS/BRIEF.md"
run_volley claude 8 "APPROVE"
assert "setup: missing BRIEF.md and SPEC.md refused" test $? -eq 1

for poll in 0 0.0 -1 abc 1.; do
  new_ws
  run_volley claude 8 "APPROVE" VOLLEY_POLL="$poll"
  assert "setup: VOLLEY_POLL=$poll refused" test $? -eq 1
done
new_ws
run_volley claude 8 "APPROVE" VOLLEY_POLL=0.5
assert "setup: VOLLEY_POLL=0.5 accepted" test $? -eq 0

echo
echo "matrix: $pass passed, $fail failed"
test "$fail" -eq 0
