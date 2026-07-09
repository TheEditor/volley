#!/usr/bin/env bash
# volley — automated plan/critique loop between Claude Code (planner) and Codex (critic).
#
# Usage:  ./volley.sh [workspace-dir]
#
# The workspace (default: this script's directory) must contain BRIEF.md.
# The loop drafts SPEC.md, then alternates critic review and planner revision
# until the critic emits VERDICT: APPROVE or MAX_ROUNDS is reached.
#
# Env overrides: MAX_ROUNDS (default 8), CALL_TIMEOUT seconds (default 900),
#                CLAUDE_BIN, CODEX_BIN.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${1:-$SCRIPT_DIR}" && pwd)"
PROMPTS="$SCRIPT_DIR/prompts"

BRIEF="$ROOT/BRIEF.md"
SPEC="$ROOT/SPEC.md"
ROUNDS="$ROOT/rounds"
STATE="$ROOT/state"

MAX_ROUNDS="${MAX_ROUNDS:-8}"
CALL_TIMEOUT="${CALL_TIMEOUT:-900}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
CODEX_BIN="${CODEX_BIN:-codex}"

die() { echo "volley: $*" >&2; exit 1; }

[[ -f "$BRIEF" ]] || die "no BRIEF.md in $ROOT — write the brief first"

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

run_planner() { # run_planner <prompt>
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} ${NESTED_ENV[@]+"${NESTED_ENV[@]}"} "$CLAUDE_BIN" -p "$1" \
    --permission-mode acceptEdits \
    --allowedTools "Read,Write,Edit,Glob,Grep" \
    </dev/null >>"$STATE/planner.log" 2>&1)
}

run_critic() { # run_critic <prompt> <critique-file>
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec \
    --sandbox read-only --skip-git-repo-check --cd "$ROOT" \
    --output-last-message "$2" \
    "$1" </dev/null >>"$STATE/critic.log" 2>&1)
}

verdict_of() { # print APPROVE or REVISE from the file's last verdict line, if any
  grep -Eo 'VERDICT:[[:space:]]*(APPROVE|REVISE)' "$1" 2>/dev/null \
    | tail -1 | grep -Eo 'APPROVE|REVISE' || true
}

# --- Round 0: initial spec (skipped on rerun so an interrupted loop resumes) ---
if [[ ! -f "$SPEC" ]]; then
  log "planner: drafting initial SPEC.md"
  run_planner "$(render "$PROMPTS/planner-init.md")"
  [[ -f "$SPEC" ]] || die "planner produced no SPEC.md (see state/planner.log)"
fi

# Resume after the last completed critique, if any.
start=$(( $(find "$ROUNDS" -name 'r*.critique.md' 2>/dev/null | wc -l) + 1 ))

for (( n=start; n<=MAX_ROUNDS; n++ )); do
  N="$(printf 'r%02d' "$n")"
  CRIT="$ROUNDS/$N.critique.md"

  log "$N: critic reviewing SPEC.md"
  run_critic "$(render "$PROMPTS/critic.md" ROUND="$n" MAX="$MAX_ROUNDS")" "$CRIT"
  v="$(verdict_of "$CRIT")"

  if [[ -z "$v" ]]; then
    log "$N: no verdict line; re-asking critic once"
    run_critic "$(render "$PROMPTS/critic.md" ROUND="$n" MAX="$MAX_ROUNDS")

REMINDER: your previous reply omitted the required final line. It must be exactly 'VERDICT: APPROVE' or 'VERDICT: REVISE'." "$CRIT"
    v="$(verdict_of "$CRIT")"
    [[ -z "$v" ]] && v="REVISE"
  fi
  log "$N: verdict is $v"

  if [[ "$v" == "APPROVE" ]]; then
    log "converged after $n round(s) — SPEC.md is final"
    exit 0
  fi

  log "$N: planner revising SPEC.md"
  run_planner "$(render "$PROMPTS/planner-revise.md" ROUND="$N")"
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
