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
  assert "[$planner] revise-approve: planner response written" \
    test -f "$WS/rounds/r01.response.md"
  assert "[$planner] revise-approve: response records disposition" \
    grep -q 'mock response entry' "$WS/rounds/r01.response.md"
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
  assert "[$planner] closing-pass: prompt asks for the closing response" \
    grep -q 'closing-response.md' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] closing-pass: prompt points at approving critique" \
    grep -q 'rounds/r01.critique.md' "$MOCK/planner-$planner-02.prompt"
  assert "[$planner] closing-pass: spec got a disposition edit" \
    grep -q 'mock revision entry' "$WS/SPEC.md"
  assert "[$planner] closing-pass: closing response written" \
    test -f "$WS/rounds/r01.closing-response.md"
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
  rm "$WS/rounds/r01.response.md" "$WS/rounds/r01.spec.md" "$WS/state/IMPASSE.md"
  run_volley "$planner" 8 "REVISE APPROVE"
  assert "[$planner] resume-revision: exit 0" test $? -eq 0
  assert "[$planner] resume-revision: log names the catch-up" \
    grep -q 'resuming interrupted revision' "$WS/run.out"
  assert "[$planner] resume-revision: r01 response recreated" \
    test -f "$WS/rounds/r01.response.md"
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
  assert "[gashki $planner] revise-approve: response written" \
    test -f "$WS/rounds/r01.response.md"
  assert "[gashki $planner] revise-approve: second critique" \
    test -f "$WS/rounds/r02.critique.md"
  assert "[gashki $planner] revise-approve: panes reused across rounds" \
    test "$(gk_spawns)" = 2
done

new_ws
run_volley claude 8 "APPROVE" "${GK[@]}" VOLLEY_CLAUDE_MODEL=mock-sonnet
assert "[gashki] agent-args: claude planner gets model" \
  grep -q '"--model","mock-sonnet"' "$MOCK"/gk/panes/*_planner.args
assert "[gashki] agent-args: claude planner tools limited" \
  grep -q '"--tools=Read,Write,Edit,Glob,Grep"' "$MOCK"/gk/panes/*_planner.args

new_ws
run_volley codex 8 "APPROVE" "${GK[@]}"
assert "[gashki] agent-args: claude critic tools limited" \
  grep -q '"--tools=Read,Glob,Grep,Write"' "$MOCK"/gk/panes/*_critic.args

new_ws
mkdir -p "$WS-ctx"
CTXDIR="$(cd "$WS-ctx" && pwd -P)"
run_volley claude 8 "APPROVE" "${GK[@]}" VOLLEY_CONTEXT_DIR="$CTXDIR"
assert "[gashki] context: claude gets --add-dir" \
  grep -q "\"--add-dir=$CTXDIR\"" "$MOCK"/gk/panes/*_planner.args
rm -rf "$CTXDIR"

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
run_volley claude 8 "APPROVE" "${GK[@]}" GK_FAULTS="r01-critique:timeout-working"
assert "[gashki] timeout while working: exit 0" test $? -eq 0
assert "[gashki] timeout while working: waited again" \
  grep -q 'waiting once more' "$WS/run.out"
assert "[gashki] timeout while working: observed the pane" \
  test "$(gk_count '^observe ')" = 1

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

# --- setup failure -------------------------------------------------------------
new_ws
rm "$WS/BRIEF.md"
run_volley claude 8 "APPROVE"
assert "setup: missing BRIEF.md refused" test $? -eq 1

echo
echo "matrix: $pass passed, $fail failed"
test "$fail" -eq 0
