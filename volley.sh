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
#                VOLLEY_CLAUDE_MODEL, VOLLEY_CODEX_MODEL,
#                VOLLEY_CLAUDE_EFFORT (low|medium|high|xhigh|max),
#                VOLLEY_CODEX_EFFORT (passed as model_reasoning_effort),
#                VOLLEY_PERSISTENT (default 0; 1 keeps one CLI session per
#                role across rounds instead of cold one-shot invocations),
#                VOLLEY_CLOSING_PASS (default 1; 0 skips the closing pass),
#                VOLLEY_SECOND_OPINION (default 0; 1 has the other agent
#                review the approved spec once, feeding the closing pass),
#                VOLLEY_CONTEXT_DIR (absolute path to an existing codebase
#                both agents may read; must lie outside the workspace),
#                VOLLEY_PROFILE (append prompts/profiles/<name>.md to every
#                critic prompt; shipped: security, data, decision-memo,
#                plan-spec),
#                VOLLEY_BACKEND (cli default; gashki runs each role in a
#                live tmux pane through the gashki CLI), GASHKI_BIN.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${1:-$SCRIPT_DIR}" && pwd)"
PROMPTS="$SCRIPT_DIR/prompts"

BRIEF="$ROOT/BRIEF.md"
CONSTRAINTS="$ROOT/CONSTRAINTS.md"
SPEC="$ROOT/SPEC.md"
ROUNDS="$ROOT/rounds"
STATE="$ROOT/state"

MAX_ROUNDS="${MAX_ROUNDS:-8}"
VOLLEY_PERSISTENT="${VOLLEY_PERSISTENT:-0}"
VOLLEY_CLOSING_PASS="${VOLLEY_CLOSING_PASS:-1}"
VOLLEY_SECOND_OPINION="${VOLLEY_SECOND_OPINION:-0}"
CALL_TIMEOUT="${CALL_TIMEOUT:-900}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
CODEX_BIN="${CODEX_BIN:-codex}"
VOLLEY_PLANNER="${VOLLEY_PLANNER:-claude}"
VOLLEY_CLAUDE_MODEL="${VOLLEY_CLAUDE_MODEL:-}"
VOLLEY_CODEX_MODEL="${VOLLEY_CODEX_MODEL:-}"
VOLLEY_CLAUDE_EFFORT="${VOLLEY_CLAUDE_EFFORT:-}"
VOLLEY_CODEX_EFFORT="${VOLLEY_CODEX_EFFORT:-}"
VOLLEY_BACKEND="${VOLLEY_BACKEND:-cli}"
GASHKI_BIN="${GASHKI_BIN:-gashki}"

die() { echo "volley: $*" >&2; ! declare -F gk_abort >/dev/null || gk_abort; exit 1; }

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

# --- Repo context: VOLLEY_CONTEXT_DIR mounts an existing codebase read-only
# for both agents, so BRIEF.md can request plans about real systems. Writes
# stay confined to the workspace; volley stays strictly on the planning side.
# codex reads outside cwd in both sandbox modes (verified empirically);
# claude needs --add-dir.
CONTEXT_BLOCK=""
CLAUDE_CTX=()
if [[ -n "${VOLLEY_CONTEXT_DIR:-}" ]]; then
  [[ "$VOLLEY_CONTEXT_DIR" == /* ]] \
    || die "VOLLEY_CONTEXT_DIR must be an absolute path (got '$VOLLEY_CONTEXT_DIR')"
  [[ -d "$VOLLEY_CONTEXT_DIR" && -r "$VOLLEY_CONTEXT_DIR" ]] \
    || die "VOLLEY_CONTEXT_DIR is not a readable directory: $VOLLEY_CONTEXT_DIR"
  CTX="$(cd "$VOLLEY_CONTEXT_DIR" && pwd)"
  case "$CTX/" in
    "$ROOT/"*) die "VOLLEY_CONTEXT_DIR must be outside the workspace ($ROOT)" ;;
  esac
  CONTEXT_BLOCK="

A read-only reference codebase is available at $CTX. Ground your work in its actual code — start from the entry points and paths BRIEF.md names. Do not modify anything under it; all writes stay in the workspace."
  CLAUDE_CTX=(--add-dir "$CTX")
fi

# --- Optional hard constraints: BRIEF.md is the human instruction; source
# files it names are context. CONSTRAINTS.md, when present, is binding and is
# injected into both roles so it cannot be missed.
CONSTRAINTS_BLOCK=""
if [[ -f "$CONSTRAINTS" ]]; then
  CONSTRAINTS_BLOCK="

--- CONSTRAINTS.md (binding) ---
$(cat "$CONSTRAINTS")
--- END CONSTRAINTS.md ---"
fi

# --- Critic rubric profile: an optional prompt fragment appended to every
# critic prompt. No profile means the critic prompt is byte-identical to the
# unprofiled render.
PROFILE_BLOCK=""
if [[ -n "${VOLLEY_PROFILE:-}" ]]; then
  PROFILE_FILE="$PROMPTS/profiles/$VOLLEY_PROFILE.md"
  [[ -f "$PROFILE_FILE" ]] \
    || die "unknown VOLLEY_PROFILE '$VOLLEY_PROFILE' — expected $PROFILE_FILE"
  PROFILE_BLOCK="

$(cat "$PROFILE_FILE")"
fi

if [[ "$VOLLEY_PERSISTENT" == "1" ]]; then
  command -v uuidgen >/dev/null 2>&1 \
    || die "VOLLEY_PERSISTENT=1 requires uuidgen for claude session ids"
fi

case "$VOLLEY_BACKEND" in
  cli) ;;
  gashki)
    command -v "$GASHKI_BIN" >/dev/null 2>&1 \
      || die "VOLLEY_BACKEND=gashki requires gashki on PATH (or GASHKI_BIN)"
    command -v jq >/dev/null 2>&1 \
      || die "VOLLEY_BACKEND=gashki requires jq"
    [[ "$VOLLEY_PERSISTENT" == "1" ]] \
      && die "VOLLEY_BACKEND=gashki keeps each role in one live pane already; unset VOLLEY_PERSISTENT"
    PLAN_FN=gk_plan; CRIT_FN=gk_critique; SECOND_FN=gk_second ;;
  *) die "VOLLEY_BACKEND must be 'cli' or 'gashki' (got '$VOLLEY_BACKEND')" ;;
esac

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

# Persistence pinning: mixing session modes across a resumed run would silently
# change the experiment a workspace is running, so pin it like the roles.
PERSIST_FILE="$STATE/persistent"
if [[ -f "$PERSIST_FILE" ]]; then
  prev_p="$(cat "$PERSIST_FILE")"
  [[ "$prev_p" == "$VOLLEY_PERSISTENT" ]] \
    || die "this workspace was started with VOLLEY_PERSISTENT=$prev_p — rerun with that, or remove rounds/ and state/ to start over"
else
  echo "$VOLLEY_PERSISTENT" >"$PERSIST_FILE"
fi

BACKEND_FILE="$STATE/backend"
if [[ -f "$BACKEND_FILE" ]]; then
  prev_b="$(cat "$BACKEND_FILE")"
  [[ "$prev_b" == "$VOLLEY_BACKEND" ]] \
    || die "this workspace was started with VOLLEY_BACKEND=$prev_b — rerun with that, or remove rounds/ and state/ to start over"
else
  echo "$VOLLEY_BACKEND" >"$BACKEND_FILE"
fi

log() { echo "[volley $(date +%H:%M:%S)] $*" | tee -a "$STATE/volley.log"; }

cmd_path() {
  command -v "$1" 2>/dev/null || printf 'not found'
}

cmd_version() { # cmd_version <bin> [args...]
  "$@" --version 2>&1 | head -1 || printf 'unavailable'
}

write_provenance() {
  cat >"$STATE/provenance.md" <<EOF
# Volley Provenance

- Workspace: $ROOT
- Started: $(date -u +%Y-%m-%dT%H:%M:%SZ)
- Planner: $VOLLEY_PLANNER
- Critic: $CRITIC
- Max rounds: $MAX_ROUNDS
- Claude binary: $(cmd_path "$CLAUDE_BIN")
- Claude version: $(cmd_version "$CLAUDE_BIN")
- Claude model: ${VOLLEY_CLAUDE_MODEL:-default/unrecorded by volley}
- Claude effort: ${VOLLEY_CLAUDE_EFFORT:-default/unrecorded by volley}
- Codex binary: $(cmd_path "$CODEX_BIN")
- Codex version: $(cmd_version "$CODEX_BIN")
- Codex model: ${VOLLEY_CODEX_MODEL:-default/unrecorded by volley}
- Codex effort: ${VOLLEY_CODEX_EFFORT:-default/unrecorded by volley}
- Context dir: ${VOLLEY_CONTEXT_DIR:-none}
- Critic profile: ${VOLLEY_PROFILE:-none}
- Persistent sessions: $VOLLEY_PERSISTENT
- Backend: $VOLLEY_BACKEND

If a model or effort is listed as default/unrecorded, Volley did not pass an
explicit flag for it; the underlying CLI chose its configured default. Agent transcripts
under state/*.log may contain more detail when the CLI prints it.
EOF
}

# macOS ships no GNU timeout; use it (or gtimeout) when available.
if command -v timeout >/dev/null 2>&1; then TIMEOUT=(timeout "$CALL_TIMEOUT")
elif command -v gtimeout >/dev/null 2>&1; then TIMEOUT=(gtimeout "$CALL_TIMEOUT")
else TIMEOUT=(); fi

CLAUDE_MODEL_ARGS=()
[[ -n "$VOLLEY_CLAUDE_MODEL" ]] && CLAUDE_MODEL_ARGS=(--model "$VOLLEY_CLAUDE_MODEL")
CODEX_MODEL_ARGS=()
[[ -n "$VOLLEY_CODEX_MODEL" ]] && CODEX_MODEL_ARGS=(--model "$VOLLEY_CODEX_MODEL")

# claude's effort levels are a closed set; catch typos before a call burns
# tokens. codex's valid set varies by model, so its value passes through and
# a bad one fails the first call (exit 1, surfacing as a missing-artifact
# die). Both survive session resume: claude keeps the session's effort,
# codex re-receives the -c override.
case "$VOLLEY_CLAUDE_EFFORT" in
  ""|low|medium|high|xhigh|max) ;;
  *) die "VOLLEY_CLAUDE_EFFORT must be low|medium|high|xhigh|max (got '$VOLLEY_CLAUDE_EFFORT')" ;;
esac
CLAUDE_EFFORT_ARGS=()
[[ -n "$VOLLEY_CLAUDE_EFFORT" ]] && CLAUDE_EFFORT_ARGS=(--effort "$VOLLEY_CLAUDE_EFFORT")
CODEX_EFFORT_ARGS=()
[[ -n "$VOLLEY_CODEX_EFFORT" ]] && CODEX_EFFORT_ARGS=(-c "model_reasoning_effort=$VOLLEY_CODEX_EFFORT")

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

# --- Persistent sessions (VOLLEY_PERSISTENT=1): each role keeps one CLI
# session across rounds, so later rounds carry working memory instead of
# cold-starting from the files. Session ids live in state/session.<role>,
# keeping the loop resumable. A session file is written only after the call
# that opens it succeeds, so a failed first call retries fresh. ---------------

session_file() { echo "$STATE/session.$1"; }

claude_session_begin() { # <role-key> — sets CLAUDE_SESSION_ARGS / CLAUDE_NEW_SESSION
  CLAUDE_SESSION_ARGS=(); CLAUDE_NEW_SESSION=""
  [[ "$VOLLEY_PERSISTENT" == "1" && -n "${1:-}" ]] || return 0
  local f; f="$(session_file "$1")"
  if [[ -f "$f" ]]; then
    CLAUDE_SESSION_ARGS=(--resume "$(cat "$f")")
  else
    CLAUDE_NEW_SESSION="$(uuidgen | tr '[:upper:]' '[:lower:]')"
    CLAUDE_SESSION_ARGS=(--session-id "$CLAUDE_NEW_SESSION")
  fi
}

claude_session_commit() { # <role-key>
  [[ -n "${1:-}" && -n "$CLAUDE_NEW_SESSION" ]] || return 0
  echo "$CLAUDE_NEW_SESSION" >"$(session_file "$1")"
}

codex_session_commit() { # <role-key> <log> — codex prints "session id: <uuid>"
  # in its run header; capture the one from the call that just completed.
  [[ "$VOLLEY_PERSISTENT" == "1" && -n "${1:-}" ]] || return 0
  local sid
  sid="$(grep -Eo 'session id: [0-9a-f-]{36}' "$2" | tail -1 | cut -d' ' -f3)"
  [[ -n "$sid" ]] || die "persistent mode: no codex session id found in $2"
  echo "$sid" >"$(session_file "$1")"
}

# --- Agent invocations: one function per agent per role. The loop below
# calls roles ($PLAN_FN/$CRIT_FN), never agents. An optional session key
# makes the call persistent; omitting it keeps the call one-shot. -------------

claude_plan() { # <prompt> [session-key]
  claude_session_begin "${2:-}"
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} ${NESTED_ENV[@]+"${NESTED_ENV[@]}"} "$CLAUDE_BIN" -p "$1" \
    ${CLAUDE_MODEL_ARGS[@]+"${CLAUDE_MODEL_ARGS[@]}"} \
    ${CLAUDE_EFFORT_ARGS[@]+"${CLAUDE_EFFORT_ARGS[@]}"} \
    ${CLAUDE_SESSION_ARGS[@]+"${CLAUDE_SESSION_ARGS[@]}"} \
    --permission-mode acceptEdits \
    --allowedTools "Read,Write,Edit,Glob,Grep" \
    ${CLAUDE_CTX[@]+"${CLAUDE_CTX[@]}"} \
    </dev/null >>"$STATE/planner.log" 2>&1)
  claude_session_commit "${2:-}"
}

claude_critique() { # <prompt> <critique-file> [session-key] — claude -p prints its final reply on stdout
  claude_session_begin "${3:-}"
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} ${NESTED_ENV[@]+"${NESTED_ENV[@]}"} "$CLAUDE_BIN" -p "$1" \
    ${CLAUDE_MODEL_ARGS[@]+"${CLAUDE_MODEL_ARGS[@]}"} \
    ${CLAUDE_EFFORT_ARGS[@]+"${CLAUDE_EFFORT_ARGS[@]}"} \
    ${CLAUDE_SESSION_ARGS[@]+"${CLAUDE_SESSION_ARGS[@]}"} \
    --allowedTools "Read,Glob,Grep" \
    ${CLAUDE_CTX[@]+"${CLAUDE_CTX[@]}"} \
    </dev/null >"$2" 2>>"$STATE/critic.log")
  claude_session_commit "${3:-}"
}

# codex exec resume has no --sandbox/--cd flags; the sandbox is re-imposed via
# -c sandbox_mode=... and the workdir comes from the session (opened with
# --cd "$ROOT") plus the subshell cd.

codex_plan() { # <prompt> [session-key] — write access limited to the workspace
  local key="${2:-}" f=""
  [[ "$VOLLEY_PERSISTENT" == "1" && -n "$key" ]] && f="$(session_file "$key")"
  if [[ -n "$f" && -f "$f" ]]; then
    (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec resume "$(cat "$f")" \
      -c sandbox_mode=workspace-write --skip-git-repo-check \
      ${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} \
      ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"} \
      "$1" </dev/null >>"$STATE/planner.log" 2>&1)
  else
    (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec \
      --sandbox workspace-write --skip-git-repo-check --cd "$ROOT" \
      ${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} \
      ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"} \
      "$1" </dev/null >>"$STATE/planner.log" 2>&1)
    codex_session_commit "$key" "$STATE/planner.log"
  fi
}

codex_critique() { # <prompt> <critique-file> [session-key]
  local key="${3:-}" f=""
  [[ "$VOLLEY_PERSISTENT" == "1" && -n "$key" ]] && f="$(session_file "$key")"
  if [[ -n "$f" && -f "$f" ]]; then
    (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec resume "$(cat "$f")" \
      -c sandbox_mode=read-only --skip-git-repo-check \
      ${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} \
      ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"} \
      --output-last-message "$2" \
      "$1" </dev/null >>"$STATE/critic.log" 2>&1)
  else
    (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec \
      --sandbox read-only --skip-git-repo-check --cd "$ROOT" \
      ${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} \
      ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"} \
      --output-last-message "$2" \
      "$1" </dev/null >>"$STATE/critic.log" 2>&1)
    codex_session_commit "$key" "$STATE/critic.log"
  fi
}

# --- gashki backend (VOLLEY_BACKEND=gashki): each role runs in a live tmux
# pane that gashki spawns, sends to and waits on. A run id in state/run names
# the panes (volley-<run>/<role>) and prefixes every idempotency key, so a
# rerun after a crash reuses the panes and replays sends instead of pasting
# a prompt twice. A normal finish kills the panes and removes state/run.
# gashki finds claude and codex on PATH; CLAUDE_BIN and CODEX_BIN are unused.

CALL_KEY=""
GK_PANES=0 # 1 once a pane may exist
GK_KEEP=0  # 1 when a failed wait may leave a turn running, so a rerun can resume

gk_run() {
  local f="$STATE/run"
  [[ -s "$f" ]] || od -An -N4 -tx1 /dev/urandom | tr -d ' \n' >"$f"
  cat "$f"
}

gk_pane() { echo "volley-$(gk_run)/$1"; }

gk_code() { jq -r '.errors[0].code // "UNKNOWN"' <<<"$1" 2>/dev/null || echo UNKNOWN; }

gk_fail() { # <what> <envelope>
  die "gashki $1 failed: $(jq -r '.errors[0] | "\(.code): \(.message)"' <<<"$2" 2>/dev/null || echo "no envelope (see state/gashki.log)")"
}

gk_agent_args() { # <agent> <role> — the --agent-args JSON array, or nothing
  local a=()
  if [[ "$1" == claude ]]; then
    a=(${CLAUDE_MODEL_ARGS[@]+"${CLAUDE_MODEL_ARGS[@]}"} ${CLAUDE_EFFORT_ARGS[@]+"${CLAUDE_EFFORT_ARGS[@]}"})
    [[ -n "${CTX:-}" ]] && a+=("--add-dir=$CTX")
    # --tools limits the tool set. A tool outside it would stop the turn on
    # a permission prompt, which gashki reports as APPROVAL_REQUIRED.
    if [[ "$2" == planner ]]; then a+=(--tools=Read,Write,Edit,Glob,Grep)
    else a+=(--tools=Read,Glob,Grep,Write); fi
  else
    a=(${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"})
  fi
  (( ${#a[@]} )) || return 0
  printf -- '--agent-args=%s' "$(jq -cn '$ARGS.positional' --args -- "${a[@]}")"
}

gk_spawn() { # <pane> <agent> <role> — returns the live pane on a rerun
  local out rc=0 aa
  aa="$(gk_agent_args "$2" "$3")"
  GK_PANES=1
  out="$("$GASHKI_BIN" spawn "$1" --agent="$2" --cwd="$ROOT" ${aa:+"$aa"} --json 2>>"$STATE/gashki.log")" || rc=$?
  (( rc == 0 )) || gk_fail "spawn $1" "$out"
}

gk_wait() { # <pane> <cursor> — one extra wait if the budget ends mid-turn
  local out rc extra=1 st
  while :; do
    rc=0
    out="$("$GASHKI_BIN" wait "$1" --until=idle --since="$2" --wait-timeout="${CALL_TIMEOUT}s" --json 2>>"$STATE/gashki.log")" || rc=$?
    (( rc == 0 )) && return 0
    if [[ "$(gk_code "$out")" == WAIT_TIMEOUT ]] && (( extra )); then
      extra=0
      st="$("$GASHKI_BIN" observe "$1" --json 2>>"$STATE/gashki.log" | jq -r '.data.state // empty' 2>/dev/null || true)"
      if [[ "$st" == working ]]; then
        log "gashki: $1 still working after ${CALL_TIMEOUT}s; waiting once more"
        continue
      fi
    fi
    GK_KEEP=1
    gk_fail "wait on $1" "$out"
  done
}

gk_call() { # <pane> <agent> <role> <prompt> — one turn under key $CALL_KEY
  local key="$(gk_run)-$CALL_KEY" pf out rc=0 cur
  gk_spawn "$1" "$2" "$3"
  mkdir -p "$STATE/prompts"
  pf="$STATE/prompts/$key.md"
  printf '%s\n' "$4" >"$pf"
  log "gashki: $1 <- $key"
  out="$(printf 'Read the file %s and do what it asks.\n' "$pf" \
    | "$GASHKI_BIN" send "$1" --from-stdin --idempotency-key="$key" --json 2>>"$STATE/gashki.log")" || rc=$?
  case "$rc" in
    0) cur="$(jq -r '.data.turn_cursor' <<<"$out")" ;;
    7) # The agent may or may not have the prompt. Never resend; wait from
       # the barrier and let the caller's file check decide.
       cur="$(jq -r '.errors[0].evidence.barrier_cursor' <<<"$out")"
       log "gashki: send $key unconfirmed ($(gk_code "$out")); waiting from the barrier" ;;
    *) gk_fail "send $key to $1" "$out" ;;
  esac
  gk_wait "$1" "$cur"
}

gk_critic_turn() { # <pane> <agent> <prompt> <critique-file>
  local rel="${4#"$ROOT"/}" before
  before="$(cksum <"$SPEC")"
  gk_call "$1" "$2" critic "$3

Write your complete reply, ending with the verdict line, to the file $rel. Do not change SPEC.md or any other file."
  [[ "$(cksum <"$SPEC")" == "$before" ]] \
    || die "critic in $1 changed SPEC.md; the critic must only review"
  [[ -f "$4" ]] || die "critic in $1 wrote no $rel (see state/gashki.log)"
}

gk_plan() { gk_call "$(gk_pane planner)" "$VOLLEY_PLANNER" planner "$1"; }

gk_critique() { gk_critic_turn "$(gk_pane critic)" "$CRITIC" "$1" "$2"; }

gk_second() {
  CALL_KEY=second
  gk_critic_turn "$(gk_pane second)" "$SECOND_AGENT" "$1" "$2"
  gk_kill "$(gk_pane second)"
}

gk_kill() { # <pane> — a pane already gone is fine
  local out rc=0
  out="$("$GASHKI_BIN" kill "$1" --yes --json 2>>"$STATE/gashki.log")" || rc=$?
  (( rc == 0 )) || [[ "$(gk_code "$out")" == NOT_FOUND ]] \
    || log "gashki: kill $1 failed: $(gk_code "$out")"
}

gk_abort() { # on die: keep the panes only if a rerun can resume them
  [[ "${VOLLEY_BACKEND:-}" == gashki ]] && (( ${GK_PANES:-0} )) || return 0
  local r="$(gk_run)"
  if (( GK_KEEP )); then
    echo "volley: panes volley-$r/* kept for a rerun; to discard, run 'gashki kill volley-$r/<role> --yes' and remove state/run" >&2
    return 0
  fi
  local role
  for role in planner critic second; do gk_kill "volley-$r/$role"; done
  rm -f "$STATE/run"
}

finish() { # <exit-code>
  if [[ "$VOLLEY_BACKEND" == gashki ]]; then
    gk_kill "$(gk_pane planner)"
    gk_kill "$(gk_pane critic)"
    rm -f "$STATE/run"
  fi
  exit "$1"
}

verdict_of() { # print APPROVE or REVISE from the file's last verdict line, if any
  grep -Eo 'VERDICT:[[:space:]]*(APPROVE|REVISE)' "$1" 2>/dev/null \
    | tail -1 | grep -Eo 'APPROVE|REVISE' || true
}

human_block_of() { # <directive-file> <round> — render the injected directive block
  printf '\n\n--- HUMAN DIRECTIVE (round %s) ---\n%s\n\n%s\n--- END HUMAN DIRECTIVE ---' \
    "$2" \
    "The human running this loop left the following instructions. They outrank the critic: comply with them, and treat any point they settle as settled — do not re-raise it in critiques or revisit it in revisions." \
    "$(cat "$1")"
}

REMARK_RE='non.?blocking|minor|remark|nitpick'

second_opinion() { # <round> — after APPROVE, the other agent reviews SPEC.md
  # once. Advisory only: it cannot flip the verdict; its remarks are input to
  # the closing pass, which keeps this lightweight and non-recursive. It stays
  # one-shot even under VOLLEY_PERSISTENT: fresh eyes are its point.
  [[ "$VOLLEY_SECOND_OPINION" == "1" ]] || return 0
  local out="$ROUNDS/second-opinion.md" n_remarks
  log "second opinion: $SECOND_AGENT reviewing approved SPEC.md"
  "$SECOND_FN" "$(render "$PROMPTS/critic.md" ROUND="$1" MAX="$MAX_ROUNDS" "HUMAN=" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")$PROFILE_BLOCK" "$out"
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
  CALL_KEY="$1-closing"
  "$PLAN_FN" "$(render "$PROMPTS/closing-pass.md" ROUND="$1" "SECOND_OPINION=$extra" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")" planner
}

log "roles: planner=$VOLLEY_PLANNER critic=$CRITIC"
write_provenance

# --- Round 0: initial spec (skipped on rerun so an interrupted loop resumes) ---
if [[ ! -f "$SPEC" ]]; then
  log "planner: drafting initial SPEC.md"
  CALL_KEY=init
  "$PLAN_FN" "$(render "$PROMPTS/planner-init.md" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")" planner
  [[ -f "$SPEC" ]] || die "planner produced no SPEC.md (see state/planner.log)"
fi

# Resume after the last completed critique, if any.
last=$(( $(find "$ROUNDS" -name 'r*.critique.md' 2>/dev/null | wc -l) ))
start=$(( last + 1 ))

# A run interrupted between critique and revision left a REVISE verdict with no
# planner response. Finish that round first instead of re-running the critic
# against the unrevised spec.
if (( last >= 1 )); then
  P="$(printf 'r%02d' "$last")"
  if [[ "$(verdict_of "$ROUNDS/$P.critique.md")" == "REVISE" && ! -f "$ROUNDS/$P.response.md" ]]; then
    HUMAN_BLOCK=""
    [[ -f "$ROUNDS/$P.human.md" ]] && HUMAN_BLOCK="$(human_block_of "$ROUNDS/$P.human.md" "$last")"
    log "$P: resuming interrupted revision"
    CALL_KEY="$P-revise"
    "$PLAN_FN" "$(render "$PROMPTS/planner-revise.md" ROUND="$P" "HUMAN=$HUMAN_BLOCK" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")" planner
    [[ -f "$ROUNDS/$P.response.md" ]] \
      || die "planner produced no rounds/$P.response.md (see state/planner.log)"
    cp "$SPEC" "$ROUNDS/$P.spec.md"
  fi
fi

for (( n=start; n<=MAX_ROUNDS; n++ )); do
  N="$(printf 'r%02d' "$n")"
  CRIT="$ROUNDS/$N.critique.md"

  # HUMAN.md steering: a directive dropped into the workspace applies to both
  # role prompts of exactly one round, then is archived. This is the only way
  # to steer a running loop without killing it.
  HUMAN_BLOCK=""
  if [[ -f "$ROOT/HUMAN.md" ]]; then
    HUMAN_BLOCK="$(human_block_of "$ROOT/HUMAN.md" "$n")"
    mv "$ROOT/HUMAN.md" "$ROUNDS/$N.human.md"
    log "$N: HUMAN.md directive applied this round (archived to rounds/$N.human.md)"
  fi

  log "$N: critic reviewing SPEC.md"
  CALL_KEY="$N-critique"
  "$CRIT_FN" "$(render "$PROMPTS/critic.md" ROUND="$n" MAX="$MAX_ROUNDS" "HUMAN=$HUMAN_BLOCK" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")$PROFILE_BLOCK" "$CRIT" critic
  v="$(verdict_of "$CRIT")"

  if [[ -z "$v" ]]; then
    log "$N: no verdict line; re-asking critic once"
    CALL_KEY="$N-critique-2"
    "$CRIT_FN" "$(render "$PROMPTS/critic.md" ROUND="$n" MAX="$MAX_ROUNDS" "HUMAN=$HUMAN_BLOCK" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")$PROFILE_BLOCK

REMINDER: your previous reply omitted the required final line. It must be exactly 'VERDICT: APPROVE' or 'VERDICT: REVISE'." "$CRIT" critic
    v="$(verdict_of "$CRIT")"
    [[ -z "$v" ]] && v="REVISE"
  fi
  log "$N: verdict is $v"

  if [[ "$v" == "APPROVE" ]]; then
    second_opinion "$n"
    closing_pass "$N" "$CRIT"
    log "converged after $n round(s) — SPEC.md is final"
    finish 0
  fi

  log "$N: planner revising SPEC.md"
  CALL_KEY="$N-revise"
  "$PLAN_FN" "$(render "$PROMPTS/planner-revise.md" ROUND="$N" "HUMAN=$HUMAN_BLOCK" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")" planner
  [[ -f "$ROUNDS/$N.response.md" ]] \
    || die "planner produced no rounds/$N.response.md (see state/planner.log)"
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
finish 2
