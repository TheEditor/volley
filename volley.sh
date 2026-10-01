#!/usr/bin/env bash
# volley — automated plan/critique loop between Claude Code and Codex.
#
# Usage:  ./volley.sh [workspace-dir]
#
# The workspace (default: this script's directory) must contain BRIEF.md or
# SPEC.md. From a brief the loop drafts SPEC.md; a SPEC.md already present is
# the first draft. It then alternates critic review and planner revision
# until the critic emits VERDICT: APPROVE or MAX_ROUNDS is reached.
#
# A planner turn that leaves questions for the user in QUESTIONS.md stops the
# loop (exit 3). The user answers in HUMAN.md and reruns; the next round gives
# both agents the questions and the answers.
#
# Roles: VOLLEY_PLANNER=claude (default) or codex chooses which agent drafts
# and revises the spec; the other agent critiques. The cc-volley and
# codex-volley wrappers preset this.
#
# Env overrides: VOLLEY_PLANNER (claude|codex), MAX_ROUNDS (default 8),
#                CALL_TIMEOUT seconds (default unset: no limit), CLAUDE_BIN,
#                CODEX_BIN,
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
#                live tmux pane through the gashki CLI), GASHKI_BIN,
#                VOLLEY_TRUST_FOLDER (set to 1 to allow a verified folder
#                trust response during gashki spawn).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${1:-$SCRIPT_DIR}" && pwd)"
PROMPTS="$SCRIPT_DIR/prompts"

BRIEF="$ROOT/BRIEF.md"
CONSTRAINTS="$ROOT/CONSTRAINTS.md"
SPEC="$ROOT/SPEC.md"
HUMAN="$ROOT/HUMAN.md"
QUESTIONS="$ROOT/QUESTIONS.md"
ROUNDS="$ROOT/rounds"
STATE="$ROOT/state"

MAX_ROUNDS="${MAX_ROUNDS:-8}"
VOLLEY_PERSISTENT="${VOLLEY_PERSISTENT:-0}"
VOLLEY_CLOSING_PASS="${VOLLEY_CLOSING_PASS:-1}"
VOLLEY_SECOND_OPINION="${VOLLEY_SECOND_OPINION:-0}"
CALL_TIMEOUT="${CALL_TIMEOUT:-}"
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

[[ -f "$BRIEF" || -f "$SPEC" ]] \
  || die "no BRIEF.md or SPEC.md in $ROOT — write the brief or the first spec first"

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

A read-only reference codebase is available at $CTX. Ground your work in its actual code — start from the entry points and paths the workspace files name. Do not modify anything under it; all writes stay in the workspace."
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

# No time limit unless CALL_TIMEOUT is set. macOS ships no GNU timeout; use
# it (or gtimeout) when available.
TIMEOUT=()
if [[ -n "$CALL_TIMEOUT" ]]; then
  if command -v timeout >/dev/null 2>&1; then TIMEOUT=(timeout "$CALL_TIMEOUT")
  elif command -v gtimeout >/dev/null 2>&1; then TIMEOUT=(gtimeout "$CALL_TIMEOUT"); fi
fi

CLAUDE_MODEL_ARGS=()
[[ -n "$VOLLEY_CLAUDE_MODEL" ]] && CLAUDE_MODEL_ARGS=(--model "$VOLLEY_CLAUDE_MODEL")

json_str() { local s="${1//\\/\\\\}"; printf '"%s"' "${s//\"/\\\"}"; }

# Skills: claude loads a skill (SKILL.md) with its Skill tool, but each read
# of the skill's other files, such as make-cli's references/, is outside the
# workspace and needs permission. Read-only allow rules for the skills dir,
# and for the target of each linked skill, let claude read skills as it does
# in an interactive session; writes there are still not allowed. Left alone,
# claude searches a parent folder such as ~/.claude for a skill's files, which
# the rules do not cover, so a system prompt line names the skills dir.
# codex reads $CODEX_HOME/skills and the whole disk, so it needs neither.
CLAUDE_ALLOW=()
CLAUDE_SKILL_ARGS=()
SKILLS_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills"
if [[ -d "$SKILLS_DIR" ]]; then
  skill_roots=("$SKILLS_DIR")
  for s in "$SKILLS_DIR" "$SKILLS_DIR"/*/; do
    [[ "$s" == "$SKILLS_DIR" || -L "${s%/}" ]] || continue
    t="$(cd "$s" 2>/dev/null && pwd -P)" || continue
    [[ "$t" == "$SKILLS_DIR" ]] || skill_roots+=("$t")
  done
  for t in "${skill_roots[@]}"; do CLAUDE_ALLOW+=("$(json_str "Read(/$t/**)")"); done
  CLAUDE_SKILL_ARGS=(--append-system-prompt "Skills are folders under $SKILLS_DIR, one per skill name. Load a skill with the Skill tool. To read or search a skill's other files, use the path $SKILLS_DIR/<name>/. Do not search a parent folder.")
fi

# claude keeps only the last --settings flag, so volley's settings are one
# object. Extra allow rules (JSON strings) join the skill rules.
claude_settings() { # [allow-rule ...] — the settings object, or nothing
  local m=() a=(${CLAUDE_ALLOW[@]+"${CLAUDE_ALLOW[@]}"} "$@")
  # A [1m] model asks for the 1M window. CLAUDE_CODE_DISABLE_1M_CONTEXT=1 in
  # the user's settings or env would still cap it at 200k; a --settings env
  # value outranks both, so it lifts the cap for volley's calls only.
  [[ "$VOLLEY_CLAUDE_MODEL" == *"[1m]" ]] && m+=('"env":{"CLAUDE_CODE_DISABLE_1M_CONTEXT":"0"}')
  (( ${#a[@]} )) && m+=("\"permissions\":{\"allow\":[$(IFS=,; echo "${a[*]}")]}")
  (( ${#m[@]} )) || return 0
  echo "{$(IFS=,; echo "${m[*]}")}"
}
CLAUDE_CLI_ARGS=()
s="$(claude_settings)"
[[ -n "$s" ]] && CLAUDE_CLI_ARGS=(--settings "$s")
CLAUDE_CLI_ARGS+=(${CLAUDE_SKILL_ARGS[@]+"${CLAUDE_SKILL_ARGS[@]}"})
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

# Planner prompts only. The planner's reply goes to a round file that no one
# reads while the loop runs, so a question there is lost; QUESTIONS.md stops
# the loop until the user answers.
ASK_BLOCK="

If a point needs a decision that only the user can make, do not ask it in your reply. No one reads your reply while the loop runs. Write the questions to QUESTIONS.md in the workspace instead: number each one, and give its options and the one you recommend. In SPEC.md, use your recommended option for now. The loop stops after your turn so the user can answer. Do not use QUESTIONS.md for points that the workspace files, the reference code, or the critique can settle."

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

# The planner's final reply to each call is saved as a round file. volley
# saves it; the prompts never ask the planner to write it.
reply_file() {
  case "$CALL_KEY" in
    init) echo "$ROUNDS/r00.response.md" ;;
    *-closing) echo "$ROUNDS/${CALL_KEY%-closing}.closing-response.md" ;;
    *) echo "$ROUNDS/${CALL_KEY%-revise}.response.md" ;;
  esac
}

claude_plan() { # <prompt> [session-key] — claude -p prints its final reply on stdout
  local r; r="$(reply_file)"
  claude_session_begin "${2:-}"
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} ${NESTED_ENV[@]+"${NESTED_ENV[@]}"} "$CLAUDE_BIN" -p "$1" \
    ${CLAUDE_MODEL_ARGS[@]+"${CLAUDE_MODEL_ARGS[@]}"} \
    ${CLAUDE_EFFORT_ARGS[@]+"${CLAUDE_EFFORT_ARGS[@]}"} \
    ${CLAUDE_SESSION_ARGS[@]+"${CLAUDE_SESSION_ARGS[@]}"} \
    ${CLAUDE_CLI_ARGS[@]+"${CLAUDE_CLI_ARGS[@]}"} \
    --permission-mode acceptEdits \
    --allowedTools "Read,Write,Edit,Glob,Grep" \
    ${CLAUDE_CTX[@]+"${CLAUDE_CTX[@]}"} \
    </dev/null >"$r" 2>>"$STATE/planner.log")
  cat "$r" >>"$STATE/planner.log"
  claude_session_commit "${2:-}"
}

claude_critique() { # <prompt> <critique-file> [session-key] — claude -p prints its final reply on stdout
  claude_session_begin "${3:-}"
  (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} ${NESTED_ENV[@]+"${NESTED_ENV[@]}"} "$CLAUDE_BIN" -p "$1" \
    ${CLAUDE_MODEL_ARGS[@]+"${CLAUDE_MODEL_ARGS[@]}"} \
    ${CLAUDE_EFFORT_ARGS[@]+"${CLAUDE_EFFORT_ARGS[@]}"} \
    ${CLAUDE_SESSION_ARGS[@]+"${CLAUDE_SESSION_ARGS[@]}"} \
    ${CLAUDE_CLI_ARGS[@]+"${CLAUDE_CLI_ARGS[@]}"} \
    --allowedTools "Read,Glob,Grep" \
    ${CLAUDE_CTX[@]+"${CLAUDE_CTX[@]}"} \
    </dev/null >"$2" 2>>"$STATE/critic.log")
  claude_session_commit "${3:-}"
}

# codex exec resume has no --sandbox/--cd flags; the sandbox is re-imposed via
# -c sandbox_mode=... and the workdir comes from the session (opened with
# --cd "$ROOT") plus the subshell cd.

codex_plan() { # <prompt> [session-key] — write access limited to the workspace
  local key="${2:-}" f="" r; r="$(reply_file)"
  [[ "$VOLLEY_PERSISTENT" == "1" && -n "$key" ]] && f="$(session_file "$key")"
  if [[ -n "$f" && -f "$f" ]]; then
    (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec resume "$(cat "$f")" \
      -c sandbox_mode=workspace-write --skip-git-repo-check \
      ${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} \
      ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"} \
      --output-last-message "$r" \
      "$1" </dev/null >>"$STATE/planner.log" 2>&1)
  else
    (cd "$ROOT" && ${TIMEOUT[@]+"${TIMEOUT[@]}"} "$CODEX_BIN" exec \
      --sandbox workspace-write --skip-git-repo-check --cd "$ROOT" \
      ${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} \
      ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"} \
      --output-last-message "$r" \
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
    # A permission prompt stops an unattended turn; gashki reports it as
    # APPROVAL_REQUIRED and the run dies. dontAsk denies what the rules do
    # not allow, and the agent goes on. It replaces gashki's acceptEdits (a
    # later --permission-mode wins), so the workspace needs an Edit rule,
    # which also covers Write. Reads in cwd and --add-dir need no rule.
    a+=(--settings "$(claude_settings "$(json_str "Edit(/$ROOT/**)")")" --permission-mode dontAsk)
    a+=(${CLAUDE_SKILL_ARGS[@]+"${CLAUDE_SKILL_ARGS[@]}"})
    # --tools limits the tool set. Skill loads a skill such as make-cli.
    if [[ "$2" == planner ]]; then a+=(--tools=Read,Write,Edit,Glob,Grep,Skill)
    else a+=(--tools=Read,Glob,Grep,Write,Skill); fi
  else
    a=(${CODEX_MODEL_ARGS[@]+"${CODEX_MODEL_ARGS[@]}"} ${CODEX_EFFORT_ARGS[@]+"${CODEX_EFFORT_ARGS[@]}"})
  fi
  (( ${#a[@]} )) || return 0
  printf -- '--agent-args=%s' "$(jq -cn '$ARGS.positional' --args -- "${a[@]}")"
}

gk_spawn() { # <pane> <agent> <role> — returns the live pane on a rerun
  local out rc=0 aa here=() trust=()
  aa="$(gk_agent_args "$2" "$3")"
  [[ -n "${TMUX_PANE:-}" ]] && here=(--here)
  [[ "${VOLLEY_TRUST_FOLDER:-}" == 1 ]] && trust=(--trust-folder)
  GK_PANES=1
  out="$("$GASHKI_BIN" spawn "$1" --agent="$2" --cwd="$ROOT" ${aa:+"$aa"} ${here[@]+"${here[@]}"} ${trust[@]+"${trust[@]}"} --json 2>>"$STATE/gashki.log")" || rc=$?
  if (( rc != 0 )); then
    GK_KEEP=1 # A failed spawn may have found an earlier pane in another window.
    gk_fail "spawn $1" "$out"
  fi
}

gk_wait() { # <pane> <cursor> — with CALL_TIMEOUT set, one wait of that
  # budget. Without it there is no limit: gashki caps one wait at 24h, so
  # wait again while the pane still works.
  local out rc st budget="${CALL_TIMEOUT:+${CALL_TIMEOUT}s}"
  while :; do
    rc=0
    out="$("$GASHKI_BIN" wait "$1" --until=idle --since="$2" --wait-timeout="${budget:-24h}" --json 2>>"$STATE/gashki.log")" || rc=$?
    (( rc == 0 )) && return 0
    if [[ -z "$CALL_TIMEOUT" && "$(gk_code "$out")" == WAIT_TIMEOUT ]]; then
      st="$("$GASHKI_BIN" observe "$1" --json 2>>"$STATE/gashki.log" | jq -r '.data.state // empty' 2>/dev/null || true)"
      if [[ "$st" == working ]]; then
        log "gashki: $1 still working; waiting again"
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

gk_reply() { # <agent> <prompt-file> — the reply that ended the turn, from the agent's transcript
  local dir f t=""
  if [[ "$1" == claude ]]; then
    # Claude names the folder after the cwd; a symlinked path may use either form.
    dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects/"
    f="$dir$(printf '%s' "$ROOT" | sed 's/[^A-Za-z0-9]/-/g')"
    [[ -d "$f" ]] || f="$dir$(cd "$ROOT" && pwd -P | sed 's/[^A-Za-z0-9]/-/g')"
    dir="$f"; f=""
  else
    dir="${CODEX_HOME:-$HOME/.codex}/sessions"
  fi
  [[ -d "$dir" ]] || return 0
  # Transcripts this run touched, newest first; the prompt path marks the turn.
  while IFS= read -r f; do
    [[ -z "$t" || "$f" -nt "$t" ]] && t="$f"
  done < <(find "$dir" -name '*.jsonl' -newer "$STATE/run" -exec grep -lF "$2" {} + 2>/dev/null)
  [[ -n "$t" ]] || return 0
  if [[ "$1" == claude ]]; then
    # A turn runs from its prompt to the next typed prompt.
    jq -rs --arg pf "$2" '
      map(select(.type == "user" or .type == "assistant"))
      | (map(.type == "user" and (.message.content | type) == "string")) as $typed
      | (map(.type == "user" and (.message.content | type) == "string"
             and (.message.content | contains($pf))) | rindex(true)) as $i
      | if $i == null then empty else
          (([$typed | to_entries[] | select(.key > $i and .value) | .key] | first) // length) as $j
          | [.[$i+1:$j][] | select(.type == "assistant") | .message.content[]?
             | select(.type == "text") | .text] | last // empty end' "$t" 2>/dev/null
  else
    jq -rs --arg pf "$2" '
      # The first task_complete after the prompt ends that turn.
      (map(.type == "response_item" and .payload.role == "user"
           and (.payload.content | tostring | contains($pf))) | rindex(true)) as $i
      | if $i == null then empty else
          [.[$i+1:][] | select(.type == "event_msg" and .payload.type == "task_complete")
           | .payload.last_agent_message // empty] | first // empty end' "$t" 2>/dev/null
  fi
}

gk_plan() {
  local pf="$STATE/prompts/$(gk_run)-$CALL_KEY.md" r out try
  r="$(reply_file)"
  gk_call "$(gk_pane planner)" "$VOLLEY_PLANNER" planner "$1"
  # The transcript may trail the Stop hook by a moment.
  for try in 1 2 3; do
    out="$(gk_reply "$VOLLEY_PLANNER" "$pf" || true)"
    [[ -n "$out" ]] && break
    sleep 1
  done
  if [[ -n "$out" ]]; then
    printf '%s\n' "$out" >"$r"
  else
    log "gashki: no planner reply found in the $VOLLEY_PLANNER transcript for $CALL_KEY; ${r#"$ROOT"/} not written"
  fi
}

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

human_block_of() { # <rNN> <round> — render the injected directive block from
  # rounds/rNN.human.md, with the planner's questions it answers, if any
  local q="$ROUNDS/$1.questions.md" asked=""
  grep -qs '[^[:space:]]' "$q" \
    && printf -v asked 'The planner asked the user these questions:\n%s\n\nThe user answered:\n' "$(cat "$q")"
  printf '\n\n--- HUMAN DIRECTIVE (round %s) ---\n%s\n\n%s%s\n--- END HUMAN DIRECTIVE ---' \
    "$2" \
    "The human running this loop left the following instructions. They outrank the critic: comply with them, and treat any point they settle as settled — do not re-raise it in critiques or revisit it in revisions." \
    "$asked" "$(cat "$ROUNDS/$1.human.md")"
}

# QUESTIONS.md is answered once a HUMAN.md newer than it exists. An empty
# QUESTIONS.md asks nothing; the user may delete it to go on without answers.
questions_open() {
  grep -qs '[^[:space:]]' "$QUESTIONS" || return 1
  ! [[ -f "$HUMAN" && "$HUMAN" -nt "$QUESTIONS" ]]
}

ask_user() { # <log-line> — stop until the user answers QUESTIONS.md
  log "$1"
  {
    echo "volley: the planner needs a decision from you. QUESTIONS.md:"
    echo
    cat "$QUESTIONS"
    echo
    echo "volley: answer in HUMAN.md, then rerun volley. The next round gives your answers to both agents."
    echo "volley: to go on without answers, delete QUESTIONS.md and rerun."
    if [[ "$VOLLEY_BACKEND" == gashki && -s "$STATE/run" ]]; then
      echo "volley: panes volley-$(cat "$STATE/run")/* stay up for the rerun"
    fi
  } >&2
  exit 3
}

check_questions() { # <rNN> — after a planner turn
  questions_open && ask_user "$1: planner left questions for the user in QUESTIONS.md; stopping (exit 3)"
  return 0
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
  "$PLAN_FN" "$(render "$PROMPTS/closing-pass.md" ROUND="$1" "SECOND_OPINION=$extra" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")$ASK_BLOCK" planner
  check_questions "$1"
}

log "roles: planner=$VOLLEY_PLANNER critic=$CRITIC"
write_provenance

# A rerun after a stop for questions goes on only once they are answered.
questions_open && ask_user "QUESTIONS.md has no answer yet (no HUMAN.md newer than it); stopping (exit 3)"

# --- Round 0: initial spec (skipped on rerun so an interrupted loop resumes) ---
if [[ ! -f "$SPEC" ]]; then
  log "planner: drafting initial SPEC.md"
  CALL_KEY=init
  "$PLAN_FN" "$(render "$PROMPTS/planner-init.md" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")$ASK_BLOCK" planner
  [[ -f "$SPEC" ]] || die "planner produced no SPEC.md (see state/planner.log)"
  check_questions r00
fi

# Resume after the last completed critique, if any.
last=$(( $(find "$ROUNDS" -name 'r*.critique.md' 2>/dev/null | wc -l) ))
start=$(( last + 1 ))
# A rerun past MAX_ROUNDS (say, after questions in the last round) runs no
# round; the impasse report then shows the last critique.
CRIT="$ROUNDS/$(printf 'r%02d' "$last").critique.md"

# A run interrupted between critique and revision left a REVISE verdict with no
# spec snapshot. Finish that round first instead of re-running the critic
# against the unrevised spec.
if (( last >= 1 )); then
  P="$(printf 'r%02d' "$last")"
  if [[ "$(verdict_of "$ROUNDS/$P.critique.md")" == "REVISE" && ! -f "$ROUNDS/$P.spec.md" ]]; then
    HUMAN_BLOCK=""
    [[ -f "$ROUNDS/$P.human.md" ]] && HUMAN_BLOCK="$(human_block_of "$P" "$last")"
    log "$P: resuming interrupted revision"
    CALL_KEY="$P-revise"
    "$PLAN_FN" "$(render "$PROMPTS/planner-revise.md" ROUND="$P" "HUMAN=$HUMAN_BLOCK" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")$ASK_BLOCK" planner
    cp "$SPEC" "$ROUNDS/$P.spec.md"
    check_questions "$P"
  fi
fi

for (( n=start; n<=MAX_ROUNDS; n++ )); do
  N="$(printf 'r%02d' "$n")"
  CRIT="$ROUNDS/$N.critique.md"

  # HUMAN.md steering: a directive dropped into the workspace applies to both
  # role prompts of exactly one round, then is archived. This is the only way
  # to steer a running loop without killing it. It also answers QUESTIONS.md,
  # which is archived with it.
  HUMAN_BLOCK=""
  if [[ -f "$HUMAN" ]]; then
    mv "$HUMAN" "$ROUNDS/$N.human.md"
    [[ -f "$QUESTIONS" ]] && mv "$QUESTIONS" "$ROUNDS/$N.questions.md"
    HUMAN_BLOCK="$(human_block_of "$N" "$n")"
    log "$N: HUMAN.md directive applied this round (archived to rounds/$N.human.md)"
    [[ -f "$ROUNDS/$N.questions.md" ]] && log "$N: it answers the planner's questions (archived to rounds/$N.questions.md)"
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
  "$PLAN_FN" "$(render "$PROMPTS/planner-revise.md" ROUND="$N" "HUMAN=$HUMAN_BLOCK" "CONSTRAINTS=$CONSTRAINTS_BLOCK" "CONTEXT=$CONTEXT_BLOCK")$ASK_BLOCK" planner
  cp "$SPEC" "$ROUNDS/$N.spec.md"
  check_questions "$N"
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
