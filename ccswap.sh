#!/usr/bin/env bash
set -euo pipefail

# 5h usage threshold (percent). Account is considered "exhausted" for the
# session window when 5h usage exceeds this.
USAGE_THRESHOLD="${USAGE_THRESHOLD:-90}"

# 7d usage threshold (percent). Account is considered "exhausted" for the
# weekly window when 7d usage exceeds this.
WEEKLY_THRESHOLD="${WEEKLY_THRESHOLD:-90}"

# Priority tiers. Pipe-separated tiers; comma-separated account numbers within
# a tier. Earlier tiers preferred. Within a tier, the account with the lowest
# max(5h%, 7d%) wins — this balances both session and weekly usage.
# Default: balance accounts 1 & 2, fall back to account 3 only when both
# tier-1 accounts are over either threshold.
PRIORITY_TIERS="${PRIORITY_TIERS:-1,2|3}"

# Proactive weekly balance: when active is still under its threshold but a
# same-tier account has 7d usage at least this many percentage points lower,
# switch to balance the weekly burn. Set to 100 to disable proactive balancing
# (script only switches when active crosses a threshold).
BALANCE_GAP="${BALANCE_GAP:-25}"

# Persistent trace log for failures and switch decisions.
CCSWAP_LOG_FILE="${CCSWAP_LOG_FILE:-${HOME:-.}/.claude-swap/ccswap.log}"

# Claude settings file used by Claude Code. Codex Proxy fallback only touches
# the top-level "env" object in this JSON file.
CLAUDE_SETTINGS_FILE="${CLAUDE_SETTINGS_FILE:-${HOME:-.}/.claude/settings.json}"

# Codex Proxy env values written to ~/.claude/settings.json when every Claude
# account path is exhausted.
CODEX_PROXY_BASE_URL="http://127.0.0.1:18765"
CODEX_PROXY_AUTH_TOKEN="unused"
CODEX_PROXY_MODEL="gpt-5.5[1m]"
CODEX_PROXY_SMALL_FAST_MODEL="gpt-5.4-mini[1m]"
CODEX_PROXY_AUTO_COMPACT_WINDOW="272000"
CODEX_PROXY_EFFORT_LEVEL="high"
CODEX_PROXY_DISABLE_NONESSENTIAL_TRAFFIC="1"
CODEX_PROXY_DISABLE_NONSTREAMING_FALLBACK="1"

write_log_line() {
  local stream="$1" line log_dir
  shift

  line="$(printf '[%s] [ccswap] %s' "$(date '+%Y-%m-%d %H:%M:%S %z')" "$*")"
  if [[ "$stream" == "stderr" ]]; then
    printf '%s\n' "$line" >&2
  else
    printf '%s\n' "$line"
  fi

  if [[ -n "$CCSWAP_LOG_FILE" ]]; then
    log_dir="$(dirname "$CCSWAP_LOG_FILE")"
    mkdir -p "$log_dir" 2>/dev/null || true
    printf '%s\n' "$line" >> "$CCSWAP_LOG_FILE" 2>/dev/null || true
  fi
}

log() { write_log_line stdout "$*"; }
err() { write_log_line stderr "ERROR: $*"; }

codex_proxy_status="skipped"
settings_edit_error=""

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "missing command: $1"
    exit 127
  fi
}

is_number() {
  [[ "$1" =~ ^[0-9]+([.][0-9]+)?$ ]]
}

compact_text() {
  printf '%s\n' "$1" | awk '
    {
      gsub(/[[:space:]]+/, " ")
      if ($0 != "") out = (out == "" ? $0 : out " " $0)
    }
    END { print out }
  '
}

log_multiline() {
  local title="$1" body="$2" line

  log "$title"
  if [[ -z "$body" ]]; then
    log "  (empty)"
    return
  fi

  while IFS= read -r line; do
    log "  $line"
  done <<< "$body"
}

append_probe_trace() {
  local candidate="$1" result="$2" status="$3" output="$4" line

  line="#${candidate} ${result} exit=${status} output=${output:-"(empty)"}"
  if [[ -n "${probe_trace:-}" ]]; then
    probe_trace+=$'\n'
  fi
  probe_trace+="$line"
}

edit_codex_proxy_env() {
  local action="$1" reason="${2:-manual}" output status changed

  set +e
  output="$(python3 - "$action" "$CLAUDE_SETTINGS_FILE" \
    "$CODEX_PROXY_BASE_URL" \
    "$CODEX_PROXY_AUTH_TOKEN" \
    "$CODEX_PROXY_MODEL" \
    "$CODEX_PROXY_SMALL_FAST_MODEL" \
    "$CODEX_PROXY_AUTO_COMPACT_WINDOW" \
    "$CODEX_PROXY_EFFORT_LEVEL" \
    "$CODEX_PROXY_DISABLE_NONESSENTIAL_TRAFFIC" \
    "$CODEX_PROXY_DISABLE_NONSTREAMING_FALLBACK" <<'PY' 2>&1
import json
import os
import stat
import sys
import tempfile

action, path = sys.argv[1], os.path.expanduser(sys.argv[2])
proxy_env = {
    "ANTHROPIC_BASE_URL": sys.argv[3],
    "ANTHROPIC_AUTH_TOKEN": sys.argv[4],
    "ANTHROPIC_MODEL": sys.argv[5],
    "ANTHROPIC_SMALL_FAST_MODEL": sys.argv[6],
    "CLAUDE_CODE_AUTO_COMPACT_WINDOW": int(sys.argv[7]),
    "CLAUDE_CODE_EFFORT_LEVEL": sys.argv[8],
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": int(sys.argv[9]),
    "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": int(sys.argv[10]),
}

if action not in {"enable", "disable"}:
    raise SystemExit(f"unknown action: {action}")

exists = os.path.exists(path)
mode = 0o600
data = {}

if exists:
    mode = stat.S_IMODE(os.stat(path).st_mode)
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()
    data = json.loads(content) if content.strip() else {}
    if not isinstance(data, dict):
        raise ValueError("settings JSON top-level value must be an object")
elif action == "disable":
    print("changed=0")
    raise SystemExit(0)

env = data.get("env")
if env is None:
    if action == "disable":
        print("changed=0")
        raise SystemExit(0)
    env = {}
    data["env"] = env
elif not isinstance(env, dict):
    raise ValueError("settings JSON env value must be an object")

before = dict(env)
if action == "enable":
    env.update(proxy_env)
else:
    for key in proxy_env:
        env.pop(key, None)

changed = int(env != before)
if changed:
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, mode=0o700, exist_ok=True)
    fd, tmp_path = tempfile.mkstemp(prefix=".settings.", suffix=".tmp", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump(data, fh, ensure_ascii=False, indent=2)
            fh.write("\n")
        os.chmod(tmp_path, mode)
        os.replace(tmp_path, path)
    except Exception:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise

print(f"changed={changed}")
PY
  )"
  status=$?
  set -e

  changed="$(printf '%s\n' "$output" | awk -F= '$1 == "changed" { print $2; exit }')"
  if [[ "$status" -ne 0 ]]; then
    settings_edit_error="$(compact_text "$output")"
    codex_proxy_status="${action}-failed"
    err "failed to ${action} Codex Proxy env in ${CLAUDE_SETTINGS_FILE}: ${settings_edit_error}"
    return "$status"
  fi

  settings_edit_error=""
  if [[ "$action" == "enable" ]]; then
    codex_proxy_status="enabled"
    if [[ "$changed" == "1" ]]; then
      log "Codex Proxy env enabled in ${CLAUDE_SETTINGS_FILE} (${reason})"
    else
      log "Codex Proxy env already enabled in ${CLAUDE_SETTINGS_FILE} (${reason})"
    fi
  else
    if [[ "$changed" == "1" ]]; then
      codex_proxy_status="disabled"
      log "Codex Proxy env removed from ${CLAUDE_SETTINGS_FILE} (${reason})"
    else
      codex_proxy_status="disabled-noop"
    fi
  fi
}

enable_codex_proxy_env() {
  edit_codex_proxy_env enable "$1"
}

disable_codex_proxy_env() {
  edit_codex_proxy_env disable "$1"
}

log_failure_context() {
  local failure_reason="$1" probe_details="${2:-not run}" final_active="${3:-unknown}"

  log "failure trace start"
  log "reason: $failure_reason"
  log "log_file: ${CCSWAP_LOG_FILE:-disabled}"
  log "claude_settings_file: ${CLAUDE_SETTINGS_FILE}"
  log "codex_proxy_status: ${codex_proxy_status}"
  log "settings_edit_error: ${settings_edit_error:-none}"
  log "thresholds: 5h>${USAGE_THRESHOLD}% 7d>${WEEKLY_THRESHOLD}% balance_gap=${BALANCE_GAP}% tiers=${PRIORITY_TIERS}"
  log "active_at_start: ${original_active:-$active_account}; active_at_failure: ${final_active:-unknown}"
  log "chosen_known_account: ${chosen:-none} 5h=${chosen_5h:-unknown}% 7d=${chosen_7d:-unknown}% tier=${chosen_tier:-unknown}"
  log "probe_candidates: ${probe_candidates:-none}"
  log_multiline "probe_results:" "$probe_details"
  log_multiline "parsed account status:" "${account_summary:-}"
  log_multiline "cswap --list --token-status output:" "${clean_output:-}"
  log "failure trace end"
}

LAST_PROBE_OUTPUT=""
LAST_PROBE_STATUS=""

claude_probe_failure_kind() {
  local output="$1"

  if printf '%s\n' "$output" | grep -Fq "You've hit your session limit"; then
    printf 'session-limit\n'
    return
  fi

  if printf '%s\n' "$output" | grep -Fq "Your organization has disabled Claude subscription access for Claude Code"; then
    printf 'subscription-disabled\n'
    return
  fi

  printf 'probe-error\n'
}

run_claude_probe() {
  local probe_output probe_status failure_kind

  if ! disable_codex_proxy_env "before Claude quota probe"; then
    LAST_PROBE_OUTPUT="failed to disable Codex Proxy env: ${settings_edit_error:-unknown settings edit error}"
    LAST_PROBE_STATUS="settings-edit"
    return 3
  fi

  set +e
  probe_output="$(claude -p hello 2>&1)"
  probe_status=$?
  set -e

  LAST_PROBE_OUTPUT="$probe_output"
  LAST_PROBE_STATUS="$probe_status"
  failure_kind="$(claude_probe_failure_kind "$probe_output")"

  if [[ "$failure_kind" == "session-limit" ]]; then
    printf '%s\n' "$probe_output" >&2
    return 2
  fi

  if [[ "$failure_kind" == "subscription-disabled" ]]; then
    printf '%s\n' "$probe_output" >&2
    return 4
  fi

  if [[ "$probe_status" -ne 0 ]]; then
    printf '%s\n' "$probe_output" >&2
    return 1
  fi

  if [[ -n "$probe_output" ]]; then
    printf '%s\n' "$probe_output"
  fi
  return 0
}

activate_codex_proxy_fallback() {
  local fallback_reason="$1" probe_details="${2:-not run}" final_active="${3:-unknown}"

  if enable_codex_proxy_env "$fallback_reason"; then
    log_failure_context "${fallback_reason}; Codex Proxy enabled" "$probe_details" "$final_active"
    log "Codex Proxy fallback enabled; Claude will use ${CODEX_PROXY_BASE_URL}"
    exit 0
  fi

  log_failure_context "failed to enable Codex Proxy fallback: ${fallback_reason}" "$probe_details" "$final_active"
  err "failed to enable Codex Proxy fallback"
  exit 1
}

require_cmd cswap
require_cmd claude
require_cmd python3

for var in USAGE_THRESHOLD WEEKLY_THRESHOLD BALANCE_GAP; do
  val="${!var}"
  if ! is_number "$val"; then
    err "$var must be a number, got: $val"
    exit 1
  fi
  if ! awk -v v="$val" 'BEGIN { exit !(v >= 0 && v <= 100) }'; then
    err "$var must be between 0 and 100, got: $val"
    exit 1
  fi
done

list_output="$(cswap --list --token-status 2>&1)" || {
  err "failed to run: cswap --list --token-status"
  printf '%s\n' "$list_output" >&2
  exit 1
}

# Strip ANSI escapes for stable parsing.
clean_output="$(printf '%s\n' "$list_output" | sed -E $'s/\x1B\\[[0-9;]*[[:alpha:]]//g')"

# Emit one row per account:
# account_number\tusage_5h\tusage_7d\tis_active\tusage_known\toauth_status
# Each usage is a numeric percent or the literal "unknown".
parsed_rows="$(printf '%s\n' "$clean_output" | awk '
  function emit() {
    if (account != "") {
      usage_known = (have_5h && have_7d) ? 1 : 0
      printf "%s\t%s\t%s\t%d\t%d\t%s\n",
        account,
        (have_5h ? u5h : "unknown"),
        (have_7d ? u7d : "unknown"),
        is_active,
        usage_known,
        oauth_status
    }
  }

  function capture_percent(line,    pct) {
    if (match(line, /[0-9]+([.][0-9]+)?%/)) {
      pct = substr(line, RSTART, RLENGTH)
      sub(/%$/, "", pct)
      return pct
    }
    return ""
  }

  BEGIN {
    account = ""; is_active = 0
    have_5h = 0; u5h = ""
    have_7d = 0; u7d = ""
    oauth_status = "unknown"
  }

  /^[[:space:]]*[0-9]+:/ {
    emit()
    line = $0
    sub(/^[[:space:]]*/, "", line)
    split(line, parts, ":")
    account = parts[1]
    is_active = (index($0, "(active)") > 0) ? 1 : 0
    have_5h = 0; u5h = ""
    have_7d = 0; u7d = ""
    oauth_status = "unknown"
    next
  }

  account != "" && /5h:/ {
    line = $0
    sub(/^.*5h:[[:space:]]*/, "", line)
    pct = capture_percent(line)
    if (pct != "") { u5h = pct; have_5h = 1 }
  }

  account != "" && /7d:/ {
    line = $0
    sub(/^.*7d:[[:space:]]*/, "", line)
    pct = capture_percent(line)
    if (pct != "") { u7d = pct; have_7d = 1 }
  }

  account != "" && /oauth:/ {
    line = $0
    sub(/^.*oauth:[[:space:]]*/, "", line)
    split(line, parts, /,[[:space:]]*/)
    oauth_status = parts[1]
  }

  END { emit() }
')"

if [[ -z "$parsed_rows" ]]; then
  err "failed to parse any accounts from cswap output"
  printf '%s\n' "$list_output" >&2
  exit 1
fi

account_summary="$(printf '%s\n' "$parsed_rows" | awk -F '\t' '
  function usage(u) { return (u == "unknown") ? "unknown" : u "%" }
  {
    state = ($5 + 0 == 1) ? "known" : "usage-unavailable"
    active = ($4 + 0 == 1) ? " active=yes" : " active=no"
    printf "#%s %s%s 5h=%s 7d=%s oauth=%s\n",
      $1, state, active, usage($2), usage($3), $6
  }
')"

decision="$(printf '%s\n' "$parsed_rows" | awk -F '\t' \
  -v tiers="$PRIORITY_TIERS" \
  -v thr5h="$USAGE_THRESHOLD" \
  -v thr7d="$WEEKLY_THRESHOLD" \
  -v balance_gap="$BALANCE_GAP" '
  function eff(u) { return u + 0 }
  function emax(a, b) { return (a > b) ? a : b }
  function usage_known(a) { return known[a] + 0 == 1 }
  function over_thresh(a) {
    return usage_known(a) && ((eff(u5h[a]) > thr5h + 0) || (eff(u7d[a]) > thr7d + 0))
  }
  function healthy(a) {
    return usage_known(a) && !over_thresh(a)
  }

  function tier_of(a,    t, i, n, parts) {
    for (t = 1; t <= n_tiers; t++) {
      n = split(tier_list[t], parts, ",")
      for (i = 1; i <= n; i++) if (parts[i] == a) return t
    }
    return 999
  }

  function append_unique(list, a,    needle) {
    needle = " " a " "
    return (index(" " list " ", needle) > 0) ? list : (list == "" ? a : list " " a)
  }

  BEGIN {
    n_tiers = split(tiers, tier_list, "|")
    active_account = ""; active_5h = ""; active_7d = ""
  }

  {
    accounts[$1] = 1
    account_order[++account_count] = $1
    u5h[$1] = $2
    u7d[$1] = $3
    known[$1] = $5
    oauth_status[$1] = $6
    if ($4 + 0 == 1) {
      active_account = $1
      active_5h = $2
      active_7d = $3
    }
  }

  END {
    chosen = ""
    chosen_5h = ""; chosen_7d = ""
    chosen_tier = 999

    # Walk tiers in order. Within a tier, sort by max(5h_eff, 7d_eff) — this
    # gives the account with the most headroom across both windows.
    # First tier with a known-usage candidate under BOTH thresholds wins.
    for (t = 1; t <= n_tiers; t++) {
      n = split(tier_list[t], tier_acc, ",")
      best = ""; best_score = 101
      for (i = 1; i <= n; i++) {
        a = tier_acc[i]
        if (!(a in accounts)) continue
        if (!healthy(a)) continue
        score = emax(eff(u5h[a]), eff(u7d[a]))
        if (best == "" || score < best_score) {
          best = a; best_score = score
        }
      }
      if (best != "") {
        chosen = best
        chosen_5h = u5h[best]
        chosen_7d = u7d[best]
        chosen_tier = t
        break
      }
    }

    if (chosen == "") {
      best = ""; best_score = 101
      for (i = 1; i <= account_count; i++) {
        a = account_order[i]
        if (!healthy(a)) continue
        score = emax(eff(u5h[a]), eff(u7d[a]))
        if (best == "" || score < best_score) {
          best = a; best_score = score
        }
      }
      if (best != "") {
        chosen = best
        chosen_5h = u5h[best]
        chosen_7d = u7d[best]
        chosen_tier = tier_of(best)
      }
    }

    probe_candidates = ""
    for (t = 1; t <= n_tiers; t++) {
      n = split(tier_list[t], tier_acc, ",")
      for (i = 1; i <= n; i++) {
        a = tier_acc[i]
        if ((a in accounts) && !usage_known(a)) {
          probe_candidates = append_unique(probe_candidates, a)
        }
      }
    }
    for (i = 1; i <= account_count; i++) {
      a = account_order[i]
      if (!usage_known(a)) {
        probe_candidates = append_unique(probe_candidates, a)
      }
    }

    active_tier = (active_account == "") ? 999 : tier_of(active_account)

    # Decide whether to switch.
    if (chosen == "" && probe_candidates != "") {
      action = "probe"
      reason = "no known-usage account has enough balance; probing usage-unavailable accounts"
    } else if (chosen == "") {
      action = "fail"
      reason = "no known-usage account has enough balance and no usage-unavailable account to probe"
    } else if (active_account == "") {
      action = "switch"
      reason = "no active account detected"
    } else if (active_account == chosen) {
      action = "stay"
      reason = sprintf("active %s already best (5h=%s%% 7d=%s%% tier=%d)",
        active_account, active_5h, active_7d, active_tier)
    } else if (!usage_known(active_account)) {
      action = "switch"
      reason = sprintf("active %s usage unavailable; known-usage account %s has enough balance",
        active_account, chosen)
    } else if (over_thresh(active_account)) {
      action = "switch"
      which = ""
      if (eff(u5h[active_account]) > thr5h + 0) which = "5h"
      if (eff(u7d[active_account]) > thr7d + 0) which = (which == "") ? "7d" : which "+7d"
      reason = sprintf("active %s over %s threshold (5h=%s%% 7d=%s%%)",
        active_account, which, active_5h, active_7d)
    } else if (chosen_tier < active_tier) {
      action = "switch"
      reason = sprintf("higher-priority tier available (active tier=%d, chosen tier=%d)",
        active_tier, chosen_tier)
    } else if (chosen_tier == active_tier && \
               eff(active_7d) - eff(u7d[chosen]) >= balance_gap + 0) {
      action = "switch"
      reason = sprintf("weekly imbalance (active 7d=%s%% vs chosen 7d=%s%%, gap >= %s%%)",
        active_7d, chosen_7d, balance_gap)
    } else {
      action = "stay"
      reason = sprintf("active %s healthy (5h=%s%% 7d=%s%% tier=%d)",
        active_account, active_5h, active_7d, active_tier)
    }

    printf "action=%s\n", action
    printf "reason=%s\n", reason
    printf "chosen=%s\n", chosen
    printf "chosen_5h=%s\n", chosen_5h
    printf "chosen_7d=%s\n", chosen_7d
    printf "chosen_tier=%s\n", chosen_tier
    printf "active_account=%s\n", active_account
    printf "active_5h=%s\n", active_5h
    printf "active_7d=%s\n", active_7d
    printf "active_tier=%s\n", active_tier
    printf "probe_candidates=%s\n", probe_candidates
  }
')"

action=""; reason=""; chosen=""; chosen_5h=""; chosen_7d=""; chosen_tier=""
active_account=""; active_5h=""; active_7d=""; active_tier=""
probe_candidates=""
while IFS='=' read -r key value; do
  case "$key" in
    action) action="$value" ;;
    reason) reason="$value" ;;
    chosen) chosen="$value" ;;
    chosen_5h) chosen_5h="$value" ;;
    chosen_7d) chosen_7d="$value" ;;
    chosen_tier) chosen_tier="$value" ;;
    active_account) active_account="$value" ;;
    active_5h) active_5h="$value" ;;
    active_7d) active_7d="$value" ;;
    active_tier) active_tier="$value" ;;
    probe_candidates) probe_candidates="$value" ;;
  esac
done <<< "$decision"

if [[ "$action" == "fail" ]]; then
  activate_codex_proxy_fallback "$reason" "probe not run" "${active_account:-unknown}"
fi

if [[ "$action" == "switch" ]]; then
  log "$reason -> switch to ${chosen} (5h=${chosen_5h}% 7d=${chosen_7d}% tier=${chosen_tier})"
  cswap --switch-to "$chosen"
  log "switch complete, warming up with: claude -p hello"
  probe_trace=""
  if run_claude_probe; then
    log "done"
  else
    probe_status=$?
    if [[ "$probe_status" -eq 2 ]]; then
      append_probe_trace "$chosen" "session-limit-after-switch" "$LAST_PROBE_STATUS" "$(compact_text "$LAST_PROBE_OUTPUT")"
      err "account ${chosen} hit session limit after switch"
      activate_codex_proxy_fallback "account ${chosen} hit session limit after switch" "$probe_trace" "$chosen"
    elif [[ "$probe_status" -eq 4 ]]; then
      append_probe_trace "$chosen" "subscription-disabled-after-switch" "$LAST_PROBE_STATUS" "$(compact_text "$LAST_PROBE_OUTPUT")"
      err "account ${chosen} has Claude subscription access disabled after switch"
      activate_codex_proxy_fallback "account ${chosen} has Claude subscription access disabled after switch" "$probe_trace" "$chosen"
    else
      append_probe_trace "$chosen" "probe-error-after-switch" "$LAST_PROBE_STATUS" "$(compact_text "$LAST_PROBE_OUTPUT")"
      err "claude warmup failed after switching to account ${chosen}"
    fi
    log_failure_context "claude warmup failed after switching to account ${chosen}" "$probe_trace" "$chosen"
    exit 1
  fi
elif [[ "$action" == "probe" ]]; then
  log "$reason: ${probe_candidates}"
  original_active="$active_account"
  probe_trace=""
  probe_had_non_fallback_failure=0

  for candidate in $probe_candidates; do
    if [[ -n "$active_account" && "$candidate" == "$active_account" ]]; then
      log "probing active usage-unavailable account ${candidate} with: claude -p hello"
    else
      log "switching to usage-unavailable account ${candidate} for probe"
      cswap --switch-to "$candidate"
      active_account="$candidate"
    fi

    if run_claude_probe; then
      log "probe succeeded; using account ${candidate}"
      log "done"
      exit 0
    else
      probe_status=$?
      if [[ "$probe_status" -eq 2 ]]; then
        append_probe_trace "$candidate" "session-limit" "$LAST_PROBE_STATUS" "$(compact_text "$LAST_PROBE_OUTPUT")"
        log "account ${candidate} hit session limit; trying next usage-unavailable account"
      elif [[ "$probe_status" -eq 4 ]]; then
        append_probe_trace "$candidate" "subscription-disabled" "$LAST_PROBE_STATUS" "$(compact_text "$LAST_PROBE_OUTPUT")"
        log "account ${candidate} has Claude subscription access disabled; trying next usage-unavailable account"
      else
        append_probe_trace "$candidate" "probe-error" "$LAST_PROBE_STATUS" "$(compact_text "$LAST_PROBE_OUTPUT")"
        probe_had_non_fallback_failure=1
        err "claude probe failed for account ${candidate}; trying next usage-unavailable account"
      fi
    fi
  done

  if [[ -n "$original_active" && "$active_account" != "$original_active" ]]; then
    log "no usage-unavailable account passed probe; restoring original active account ${original_active}"
    if cswap --switch-to "$original_active"; then
      active_account="$original_active"
    else
      err "failed to restore original active account ${original_active}"
    fi
  fi

  if [[ "$probe_had_non_fallback_failure" -eq 0 ]]; then
    activate_codex_proxy_fallback "no Claude account available; all probes hit session limit or subscription access is disabled" "$probe_trace" "${active_account:-unknown}"
  fi

  log_failure_context "no account with available balance found" "$probe_trace" "${active_account:-unknown}"
  err "no account with available balance found"
  exit 1
else
  if ! disable_codex_proxy_env "Claude account available; ${reason}"; then
    log_failure_context "failed to remove Codex Proxy env while Claude account is available" "probe not run" "${active_account:-unknown}"
    exit 1
  fi
  log "$reason; no switch"
fi
