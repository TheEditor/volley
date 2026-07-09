#!/usr/bin/env bash
# Mock test matrix for volley.sh.
#
# Runs the loop against tests/mocks/{claude,codex} in throwaway workspaces,
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

run_volley() { # run_volley <planner> <max-rounds> <verdicts> [VAR=VAL ...]
  local planner="$1" max="$2" verdicts="$3"; shift 3
  env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY -u VOLLEY_ALLOW_API_KEY \
    HOME="$FAKEHOME" MOCK_STATE="$MOCK" MOCK_VERDICTS="$verdicts" \
    CLAUDE_BIN="$MOCKS/claude" CODEX_BIN="$MOCKS/codex" \
    VOLLEY_PLANNER="$planner" MAX_ROUNDS="$max" \
    "$@" "$VOLLEY" "$WS" >"$WS/run.out" 2>&1
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

  # --- revise then approve ---------------------------------------------------
  new_ws
  run_volley "$planner" 8 "REVISE APPROVE"
  assert "[$planner] revise-approve: exit 0" test $? -eq 0
  assert "[$planner] revise-approve: r01 spec snapshot kept" \
    test -f "$WS/rounds/r01.spec.md"
  assert "[$planner] revise-approve: spec revised" \
    grep -q 'mock revision entry' "$WS/SPEC.md"
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
  assert "[$planner] closing-pass: prompt names closing pass" \
    grep -q 'closing pass' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] closing-pass: prompt points at approving critique" \
    grep -q 'rounds/r01.critique.md' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] closing-pass: spec got a decision-log disposition" \
    grep -q 'mock revision entry' "$WS/SPEC.md"
  assert "[$planner] closing-pass: critic not re-run" \
    test "$(cat "$MOCK/critic-calls")" = 1

  # --- closing pass disabled -----------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE_REMARKS" VOLLEY_CLOSING_PASS=0
  assert "[$planner] closing-pass-off: exit 0" test $? -eq 0
  assert "[$planner] closing-pass-off: no extra planner call" \
    test "$(cat "$MOCK/planner-calls")" = 1

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

  # --- role pinning ------------------------------------------------------------
  new_ws
  run_volley "$planner" 8 "APPROVE"
  run_volley "$(other "$planner")" 8 "APPROVE"
  assert "[$planner] role-pin: swapped rerun refused" test $? -eq 1
  assert "[$planner] role-pin: message names original role" \
    grep -q "planner=$planner" "$WS/run.out"
done

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

# --- setup failure -------------------------------------------------------------
new_ws
rm "$WS/BRIEF.md"
run_volley claude 8 "APPROVE"
assert "setup: missing BRIEF.md refused" test $? -eq 1

echo
echo "matrix: $pass passed, $fail failed"
test "$fail" -eq 0
