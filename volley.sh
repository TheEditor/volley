#!/usr/bin/env bash
# volley — automated plan/critique loop between Claude Code and Codex.
#
# Usage:  ./volley.sh [workspace-dir]
#
# The workspace (default: this script's directory) must contain BRIEF.md.
# The loop drafts SPEC.md, then alternates critic review and planner revision
# until the critic emits VERDICT: APPROVE or MAX_ROUNDS is reached.
#
# Roles: VOLLEY_PLANNER=claude (default) or codex chooses which agent drafts
# and revises the spec; the other agent critiques. The cc-volley and
# codex-volley wrappers preset this.
#
# Env overrides: VOLLEY_PLANNER (claude|codex), MAX_ROUNDS (default 8),
#                CALL_TIMEOUT seconds (default 900), CLAUDE_BIN, CODEX_BIN,
#                VOLLEY_CLOSING_PASS (default 1; 0 skips the closing pass),
#                VOLLEY_SECOND_OPINION (default 0; 1 has the other agent
#                review the approved spec once, feeding the closing pass).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${1:-$SCRIPT_DIR}" && pwd)"
PROMPTS="$SCRIPT_DIR/prompts"

BRIEF="$ROOT/BRIEF.md"
SPEC="$ROOT/SPEC.md"
ROUNDS="$ROOT/rounds"
STATE="$ROOT/state"

MAX_ROUNDS="${MAX_ROUNDS:-8}"
VOLLEY_CLOSING_PASS="${VOLLEY_CLOSING_PASS:-1}"
VOLLEY_SECOND_OPINION="${VOLLEY_SECOND_OPINION:-0}"
CALL_TIMEOUT="${CALL_TIMEOUT:-900}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
CODEX_BIN="${CODEX_BIN:-codex}"
VOLLEY_PLANNER="${VOLLEY_PLANNER:-claude}"

die() { echo "volley: $*" >&2; exit 1; }

[[ -f "$BRIEF" ]] || die "no BRIEF.md in $ROOT — write the brief first"

case "$VOLLEY_PLANNER" in
  claude) PLAN_FN=claude_plan; CRIT_FN=codex_critique;  CRITIC=codex
          SECOND_FN=claude_critique; SECOND_AGENT=claude ;;
  codex)  PLAN_FN=codex_plan;  CRIT_FN=claude_critique; CRITIC=claude
          SECOND_FN=codex_critique;  SECOND_AGENT=codex ;;
  *) die "VOLLEY_PLANNER must be 'claude' or 'codex' (got '$VOLLEY_PLANNER')" ;;
esac

# --- Billing guard: refuse to run if a pay-per-token API credential could be
# picked up by either CLI instead of the subscription login. Override with
# VOLLEY_ALLOW_API_KEY=1 if metered billing is actually intended.
if [[ -z "${VOLLEY_ALLOW_API_KEY:-}" ]]; then
  [[ -n "${ANTHROPIC_API_KEY:-}" ]] \
    && die "ANTHROPIC_API_KEY is set — claude would bill the API, not your subscription. Unset it or set VOLLEY_ALLOW_API_KEY=1."
  [[ -n "${OPENAI_API_KEY:-}" ]] \
    && die "OPENAI_API_KEY is set — codex may bill the API, not your ChatGPT plan. Unset it or set VOLLEY_ALLOW_API_KEY=1."
  grep -qs '"apiKeyHelper"' "$HOME/.claude/settings.json" \
    && die "apiKeyHelper found in ~/.claude/settings.json — claude would bill the API. Remove it or set VOLLEY_ALLOW_API_KEY=1."
  grep -qsE '"OPENAI_API_KEY"[[:space:]]*:[[:space:]]*"' "$HOME/.codex/auth.json" \
    && die "API key found in ~/.codex/auth.json — codex would bill the API. Re-run 'codex login' with ChatGPT or set VOLLEY_ALLOW_API_KEY=1."
fi

mkdir -p "$ROUNDS" "$STATE"

# Role pinning: an interrupted run must resume with the same role assignment.
ROLES_FILE="$STATE/roles"
if [[ -f "$ROLES_FILE" ]]; then
  prev="$(cat "$ROLES_FILE")"
  [[ "$prev" == "planner=$VOLLEY_PLANNER" ]] \
    || die "this workspace was started with $prev — rerun with that, or remove rounds/ and state/ to start over"
else
  echo "planner=$VOLLEY_PLANNER" >"$ROLES_FILE"
fi

log() { echo "[volley $(date +%H:%M:%S)] $*" | tee -a "$STATE/volley.log"; }

# macOS ships no GNU timeout; use it (or gtimeout) when available.
if command -v timeout >/dev/null 2>&1; then TIMEOUT=(timeout "$CALL_TIMEOUT")
elif command -v gtimeout >/dev/null 2>&1; then TIMEOUT=(gtimeout "$CALL_TIMEOUT")
else TIMEOUT=(); fi

render() { # render <prompt-file> [KEY=value ...] — substitute {{KEY}} placeholders
  local out; out="$(cat "$1")"; shift
  local kv
  for kv in "$@"; do out="${out//\{\{${kv%%=*}\}\}/${kv#*=}}"; done
  printf '%s' "$out"
}

# When volley is launched from inside a Claude Code session, harness-injected
# env (proxy base URL, session markers) breaks the nested CLI's auth. Strip it.
NESTED_ENV=()
[[ -n "${CLAUDECODE:-}" ]] && NESTED_ENV=(env -u ANTHROPIC_BASE_URL -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT)

# --- Agent invocations: one function per agent per role. The loop below
# calls roles ($PLAN_FN/$CRIT_FN), never agents. ------------------------------

claude_plan() { # <prompt>
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} ${NESTED_ENV[@]+"${NESTED_ENV[@]}"} "$CLAUDE_BIN" -p "$1" \
    --permission-mode acceptEdits \
    --allowedTools "Read,Write,Edit,Glob,Grep" \
    </dev/null >>"$STATE/planner.log" 2>&1)
}

claude_critique() { # <prompt> <critique-file> — claude -p prints its final reply on stdout
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} ${NESTED_ENV[@]+"${NESTED_ENV[@]}"} "$CLAUDE_BIN" -p "$1" \
    --allowedTools "Read,Glob,Grep" \
    </dev/null >"$2" 2>>"$STATE/critic.log")
}

codex_plan() { # <prompt> — write access limited to the workspace
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec \
    --sandbox workspace-write --skip-git-repo-check --cd "$ROOT" \
    "$1" </dev/null >>"$STATE/planner.log" 2>&1)
}

codex_critique() { # <prompt> <critique-file>
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec \
    --sandbox read-only --skip-git-repo-check --cd "$ROOT" \
    --output-last-message "$2" \
    "$1" </dev/null >>"$STATE/critic.log" 2>&1)
}

verdict_of() { # print APPROVE or REVISE from the file's last verdict line, if any
  grep -Eo 'VERDICT:[[:space:]]*(APPROVE|REVISE)' "$1" 2>/dev/null \
    | tail -1 | grep -Eo 'APPROVE|REVISE' || true
}

REMARK_RE='non.?blocking|minor|remark|nitpick'

second_opinion() { # <round> — after APPROVE, the other agent reviews SPEC.md
  # once. Advisory only: it cannot flip the verdict; its remarks are input to
  # the closing pass, which keeps this lightweight and non-recursive.
  [[ "$VOLLEY_SECOND_OPINION" == "1" ]] || return 0
  local out="$ROUNDS/second-opinion.md" n_remarks
  log "second opinion: $SECOND_AGENT reviewing approved SPEC.md"
  "$SECOND_FN" "$(render "$PROMPTS/critic.md" ROUND="$1" MAX="$MAX_ROUNDS")" "$out"
  n_remarks="$(grep -cE '^[0-9]+\.' "$out" 2>/dev/null || true)"
  log "second opinion from $SECOND_AGENT: ${n_remarks:-0} remark(s)"
}

closing_pass() { # <rNN> <critique-file> — after APPROVE, the planner addresses
  # or consciously declines any non-blocking remarks, incl. the second
  # opinion's if one ran. The approval stands; the critic is not re-run.
  # Over-triggering is harmless (the planner declines vacuously), so the
  # remark check errs toward running.
  [[ "$VOLLEY_CLOSING_PASS" != "0" ]] || return 0
  local so="$ROUNDS/second-opinion.md" extra="" has=0
  grep -qiE "$REMARK_RE" "$2" && has=1
  if [[ -f "$so" ]]; then
    extra="A second-opinion review from another critic is in rounds/second-opinion.md. Read it too and dispose of each of its objections and remarks the same way; it is advisory and does not reopen the review."
    grep -qiE "$REMARK_RE|^[0-9]+\." "$so" && has=1
  fi
  if (( ! has )); then
    log "$1: approval carries no remarks; skipping closing pass"
    return 0
  fi
  log "$1: closing pass — planner disposing of non-blocking remarks"
  "$PLAN_FN" "$(render "$PROMPTS/closing-pass.md" ROUND="$1" "SECOND_OPINION=$extra")"
}

log "roles: planner=$VOLLEY_PLANNER critic=$CRITIC"

# --- Round 0: initial spec (skipped on rerun so an interrupted loop resumes) ---
if [[ ! -f "$SPEC" ]]; then
  log "planner: drafting initial SPEC.md"
  "$PLAN_FN" "$(render "$PROMPTS/planner-init.md")"
  [[ -f "$SPEC" ]] || die "planner produced no SPEC.md (see state/planner.log)"
fi

# Resume after the last completed critique, if any.
start=$(( $(find "$ROUNDS" -name 'r*.critique.md' 2>/dev/null | wc -l) + 1 ))

for (( n=start; n<=MAX_ROUNDS; n++ )); do
  N="$(printf 'r%02d' "$n")"
  CRIT="$ROUNDS/$N.critique.md"

  log "$N: critic reviewing SPEC.md"
  "$CRIT_FN" "$(render "$PROMPTS/critic.md" ROUND="$n" MAX="$MAX_ROUNDS")" "$CRIT"
  v="$(verdict_of "$CRIT")"

  if [[ -z "$v" ]]; then
    log "$N: no verdict line; re-asking critic once"
    "$CRIT_FN" "$(render "$PROMPTS/critic.md" ROUND="$n" MAX="$MAX_ROUNDS")

REMINDER: your previous reply omitted the required final line. It must be exactly 'VERDICT: APPROVE' or 'VERDICT: REVISE'." "$CRIT"
    v="$(verdict_of "$CRIT")"
    [[ -z "$v" ]] && v="REVISE"
  fi
  log "$N: verdict is $v"

  if [[ "$v" == "APPROVE" ]]; then
    second_opinion "$n"
    closing_pass "$N" "$CRIT"
    log "converged after $n round(s) — SPEC.md is final"
    exit 0
  fi

  log "$N: planner revising SPEC.md"
  "$PLAN_FN" "$(render "$PROMPTS/planner-revise.md" ROUND="$N")"
  cp "$SPEC" "$ROUNDS/$N.spec.md"
done

{
  echo "# Impasse"
  echo
  echo "No approval after $MAX_ROUNDS rounds. Final critique:"
  echo
  cat "$CRIT"
} >"$STATE/IMPASSE.md"
log "impasse: $MAX_ROUNDS rounds without approval — see state/IMPASSE.md"
exit 2
