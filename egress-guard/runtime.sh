#!/usr/bin/env bash
# Runtime helper installed by install-egress-guard.sh; no cloud API calls.
set -euo pipefail

CONFIG_FILE="${GUARD_CONFIG_FILE:-/etc/default/gcp-egress-guard}"
STATE_DIR="${GUARD_STATE_DIR:-/var/lib/gcp-egress-guard}"
STATE_FILE="${STATE_DIR}/state"
STATUS_FILE="${GUARD_STATUS_FILE:-/run/gcp-egress-guard/status.json}"
SYS_NET="${GUARD_SYS_NET:-/sys/class/net}"
BOOT_ID_FILE="${GUARD_BOOT_ID_FILE:-/proc/sys/kernel/random/boot_id}"
MODE="${1:-run}"
REQUESTED_SERVICE="${2:-}"
NO_START=false

LIMIT_GIB=180 WARN_GIB=150 SERVICE_NAME=sing-box SERVICE_NAMES=""
INTERFACE=auto BILLING_TIMEZONE=America/Los_Angeles
MASK_ON_LIMIT=true START_NEXT_MONTH=false

log_msg() {
  echo "$*" >&2
  logger -t gcp-egress-guard "$*" || true
}

# Read key/value data, never execute config or state content as shell code.
load_config() {
  local key value
  local -A seen=()
  [[ -r "$CONFIG_FILE" ]] || return 1
  while IFS='=' read -r key value; do
    [[ -z "$key" || "$key" == \#* ]] && continue
    [[ -z "${seen[$key]:-}" ]] || return 1
    seen[$key]=1
    case "$key" in
      LIMIT_GIB|WARN_GIB|SERVICE_NAME|SERVICE_NAMES|INTERFACE|BILLING_TIMEZONE|MASK_ON_LIMIT|START_NEXT_MONTH)
        value="${value#\"}"; value="${value%\"}"
        printf -v "$key" '%s' "$value"
        ;;
      *) return 1 ;;
    esac
  done <"$CONFIG_FILE"
}

services=(sing-box hysteria-server)
normalize_services() {
  local name
  local -A seen=()
  services=()
  local configured="${SERVICE_NAMES:-$SERVICE_NAME}"
  read -r -a candidates <<<"$configured"
  ((${#candidates[@]} > 0 && ${#candidates[@]} <= 8)) || return 1
  for name in "${candidates[@]}"; do
    name="${name%.service}"
    [[ "$name" =~ ^[A-Za-z0-9@_.-]+$ && "$name" != -* && "$name" != *..* ]] || return 1
    [[ "$name" != gcp-egress-guard* && "$name" != hy2-account* && "$name" != hy2-health* ]] || return 1
    if [[ -z "${seen[$name]:-}" ]]; then services+=("$name"); seen[$name]=1; fi
  done
}

load_config || { log_msg 'Invalid/missing guard configuration.'; config_bad=true; }
config_bad="${config_bad:-false}"
if [[ "$config_bad" == true ]] || ! normalize_services; then services=(sing-box hysteria-server); config_bad=true; fi

usage() { echo 'Usage: gcp-egress-guard [--resume [--no-start] | --permit SERVICE | --status]' >&2; exit 2; }
case "$MODE" in
  run) (($#<=1)) || usage ;;
  --resume) if (($#==2)) && [[ "$2" == --no-start ]]; then NO_START=true; elif (($#!=1)); then usage; fi ;;
  --permit) (($#==2)) || usage ;;
  --status) (($#==1)) || usage ;;
  *) usage ;;
esac

state_month="" last_tx="" used_bytes="" blocked_by_guard=false warning_logged=false
last_boot_id="" last_interface="" updated_epoch=0
month="$(TZ=America/Los_Angeles date +%Y-%m)" # Safe fallback for invalid config/status.
now="$(date +%s)"
new_month=false

write_status() {
  local healthy="$1" reason="$2" tmp
  install -d -m 0755 "$(dirname "$STATUS_FILE")"
  tmp="$(mktemp "${STATUS_FILE}.tmp.XXXXXX")"
  python3 - "$month" "${used_bytes:-0}" "$LIMIT_GIB" "$WARN_GIB" "$blocked_by_guard" "$healthy" "$reason" "$now" "${services[@]}" >"$tmp" <<'PY'
import json, sys
month, used, limit, warn, blocked, healthy, reason, updated, *services = sys.argv[1:]
print(json.dumps({"month":month,"used_bytes":int(used),"limit_bytes":int(limit)*2**30,
 "warn_bytes":int(warn)*2**30,"blocked":blocked=="true","healthy":healthy=="true",
 "reason":reason,"updated":int(updated),"services":services}))
PY
  chmod 0644 "$tmp"
  mv -f "$tmp" "$STATUS_FILE"
}

# Callers use `write_state || fail_closed`, where bash disables `set -e` inside
# the function. Every step is therefore checked explicitly; a failed publish
# must never fall through to a startup permit with unpersisted usage.
write_state() {
  local tmp
  tmp="$(mktemp "${STATE_FILE}.tmp.XXXXXX")" || return 1
  if chmod 0600 "$tmp" &&
     printf 'state_month=%s\nlast_tx=%s\nused_bytes=%s\nblocked_by_guard=%s\nwarning_logged=%s\nlast_boot_id=%s\nlast_interface=%s\nupdated_epoch=%s\n' \
       "$state_month" "$last_tx" "$used_bytes" "$blocked_by_guard" "$warning_logged" \
       "$last_boot_id" "$last_interface" "$now" >"$tmp" &&
     sync -f "$tmp" &&
     mv -f "$tmp" "$STATE_FILE" &&
     sync -f "$STATE_DIR"; then
    return 0
  fi
  rm -f "$tmp"
  return 1
}

mask_all() {
  local name state failed=0
  # Every stop is bounded; failure on one unit never skips the other.
  for name in "${services[@]}"; do
    if ! timeout 8s systemctl stop "$name.service" >/dev/null 2>&1; then
      log_msg "Failed to stop ${name}.service."; failed=1
    fi
    state="$(timeout 2s systemctl show "$name.service" --property=ActiveState --value 2>/dev/null)" || state=unknown
    if [[ "$state" != inactive && "$state" != failed ]]; then
      log_msg "${name}.service is not verified stopped; shutdown incomplete."; failed=1
    fi
  done
  # Generator-owned masks work even when units are installed in /etc, and do
  # not overwrite any unrelated operator mask. The persisted latch/state is
  # read on each reload and boot before either unit is loaded.
  if ! timeout 8s systemctl daemon-reload >/dev/null 2>&1; then failed=1; fi
  for name in "${services[@]}"; do
    state="$(timeout 2s systemctl show "$name.service" --property=LoadState --value 2>/dev/null)" || state=unknown
    if [[ "$state" != masked ]]; then log_msg "${name}.service is not verified masked."; failed=1; fi
  done
  return "$failed"
}

persist_block() {
  python3 - "${STATE_DIR}/blocked" "$1" <<'PY'
import os,sys,tempfile
path,reason=sys.argv[1:]; directory=os.path.dirname(path)
fd,tmp=tempfile.mkstemp(prefix='.blocked-',dir=directory)
try:
    with os.fdopen(fd,'w') as f:
        f.write(reason+'\n'); f.flush(); os.fsync(f.fileno())
    os.replace(tmp,path)
    fd=os.open(directory,os.O_RDONLY)
    try: os.fsync(fd)
    finally: os.close(fd)
finally:
    if os.path.exists(tmp): os.unlink(tmp)
PY
}

fail_closed() {
  local reason="$1"
  blocked_by_guard=true
  # A separate latch survives malformed/missing state and VM reboots. Explicit
  # reseeding never erases a previous global block; --resume is required.
  persist_block "$reason" || log_msg 'Could not persist global block; still attempting every shutdown.'
  write_status false "$reason" || true
  log_msg "FAIL CLOSED: $reason Stopping/masking every protected service."
  # A startup gate must not synchronously stop its own activating unit, nor
  # wait for the running oneshot guard (systemd ordering deadlock).
  if [[ "$MODE" != --permit ]]; then mask_all || true; fi
  exit 1
}

invalid_config() {
  services=(sing-box hysteria-server)
  LIMIT_GIB=180 WARN_GIB=150
  fail_closed "$1"
}

if [[ "$config_bad" == true ]] ||
   ! [[ "$LIMIT_GIB" =~ ^[0-9]{1,6}$ && "$WARN_GIB" =~ ^[0-9]{1,6}$ ]] ||
   ((10#$LIMIT_GIB < 1 || 10#$LIMIT_GIB > 180 || 10#$WARN_GIB >= 10#$LIMIT_GIB)) ||
   [[ "$MASK_ON_LIMIT" != true ]] ||
   [[ "$START_NEXT_MONTH" != true && "$START_NEXT_MONTH" != false ]]; then
  invalid_config 'Invalid guard configuration; protection cannot be disabled.'
fi
if ! [[ "$BILLING_TIMEZONE" =~ ^[A-Za-z0-9_+.-]+(/[A-Za-z0-9_+.-]+)*$ ]] ||
   [[ "$BILLING_TIMEZONE" == *..* || ! -e "/usr/share/zoneinfo/$BILLING_TIMEZONE" ]]; then
  invalid_config 'Invalid installed billing timezone.'
fi
if [[ "$INTERFACE" != auto ]] && ! [[ "$INTERFACE" =~ ^[A-Za-z0-9_.:-]+$ ]]; then
  invalid_config 'Invalid interface.'
fi

month="$(TZ="$BILLING_TIMEZONE" date +%Y-%m)"
for name in "${services[@]}"; do
  if [[ "$name" == hysteria-server && "$START_NEXT_MONTH" == true ]]; then
    invalid_config 'HY2 requires explicit review; automatic global resume is not supported for this service list.'
  fi
  if [[ "$name" == hysteria-server && "$MODE" == --resume && "$NO_START" != true ]]; then
    echo 'For independent HY2 use --resume --no-start, then separately arm/start/activate HY2. Global resume must not bypass its accounting review.' >&2
    exit 2
  fi
done

load_state() {
  local key value
  local -A seen=()
  [[ -r "$STATE_FILE" ]] || return 1
  while IFS='=' read -r key value; do
    [[ -n "$key" && -z "${seen[$key]:-}" ]] || return 1
    seen[$key]=1
    case "$key" in
      state_month|last_tx|used_bytes|blocked_by_guard|warning_logged|last_boot_id|last_interface|updated_epoch)
        printf -v "$key" '%s' "$value" ;;
      *) return 1 ;;
    esac
  done <"$STATE_FILE"
  for key in state_month last_tx used_bytes blocked_by_guard warning_logged; do
    [[ -n "${seen[$key]:-}" ]] || return 1
  done
  [[ "$state_month" =~ ^[0-9]{4}-(0[1-9]|1[0-2])$ ]] || return 1
  [[ "$last_tx" =~ ^[0-9]{1,18}$ && "$used_bytes" =~ ^[0-9]{1,18}$ && "$updated_epoch" =~ ^[0-9]{1,12}$ ]] || return 1
  [[ "$blocked_by_guard" == true || "$blocked_by_guard" == false ]] || return 1
  [[ "$warning_logged" == true || "$warning_logged" == false ]] || return 1
  # Legacy five-field state has no process/interface witness. Keep its total
  # and conservative baseline; first successful read adds the witness.
  [[ -z "$last_boot_id" || "$last_boot_id" =~ ^[A-Za-z0-9-]+$ ]] || return 1
  [[ -z "$last_interface" || "$last_interface" =~ ^[A-Za-z0-9_.:-]+$ ]] || return 1
}
install -d -m 0700 "$STATE_DIR" || fail_closed 'Guard state directory unavailable.'
if ! { exec 9>"${STATE_DIR}/lock"; }; then fail_closed 'Guard lock file unavailable.'; fi
flock -w 10 9 || fail_closed 'Could not acquire guard lock; total accounting is not trusted.'
load_state || fail_closed 'State is missing/invalid; re-audit before explicitly reseeding.'
[[ ! -e "${STATE_DIR}/blocked" && ! -L "${STATE_DIR}/blocked" ]] || blocked_by_guard=true

if [[ "$INTERFACE" == auto ]]; then iface="$(ip route show default | awk '{print $5; exit}')"; else iface="$INTERFACE"; fi
[[ -n "$iface" && -r "$SYS_NET/$iface/statistics/tx_bytes" ]] || fail_closed 'Default interface counter unavailable.'
tx_bytes="$(<"$SYS_NET/$iface/statistics/tx_bytes")"
boot_id="$(<"$BOOT_ID_FILE")"
[[ "$tx_bytes" =~ ^[0-9]{1,18}$ && "$boot_id" =~ ^[A-Za-z0-9-]+$ ]] || fail_closed 'Invalid interface/boot counter.'
last_tx=$((10#$last_tx)); used_bytes=$((10#$used_bytes)); tx_bytes=$((10#$tx_bytes)); updated_epoch=$((10#$updated_epoch))
if [[ -n "$last_interface" && "$last_interface" != "$iface" ]]; then fail_closed 'Default interface changed; explicit baseline verification required.'; fi
if ((now < updated_epoch)); then fail_closed 'Clock moved backwards; manual verification required.'; fi

# Always count the current boot's bytes when boot identity changed, even if
# the new absolute counter is numerically greater than the old counter.
if [[ -z "$last_boot_id" || "$last_boot_id" != "$boot_id" ]]; then
  delta=$tx_bytes
  if [[ -z "$last_boot_id" ]]; then log_msg 'Legacy state has no boot witness; conservatively adding this boot total once (possible overcount), never assuming a matching boot.'; fi
elif ((tx_bytes >= last_tx)); then
  delta=$((tx_bytes - last_tx))
else
  fail_closed 'Interface counter decreased within the same boot.'
fi
if [[ "$state_month" != "$month" ]]; then
  [[ "$state_month" < "$month" ]] || fail_closed 'Billing month moved backwards.'
  state_month="$month"; used_bytes=0; warning_logged=false; new_month=true
  # End-of-sample attribution: entire boundary delta goes to the new month.
  log_msg 'New billing month; boundary delta attributed to sampling-end month. Global block is retained by default.'
fi
used_bytes=$((used_bytes + delta)); last_tx="$tx_bytes"; last_boot_id="$boot_id"; last_interface="$iface"
limit_bytes=$((10#$LIMIT_GIB * 1024 * 1024 * 1024))
warn_bytes=$((10#$WARN_GIB * 1024 * 1024 * 1024))
if ((used_bytes >= limit_bytes)); then
  blocked_by_guard=true
  write_state || fail_closed 'Could not persist limit state.'
  fail_closed 'Monthly VM egress limit reached.'
fi
if ((used_bytes >= warn_bytes)) && [[ "$warning_logged" == false ]]; then warning_logged=true; log_msg "Monthly egress warning: $used_bytes / $limit_bytes bytes."; fi

# All state writes precede the permission decision. This never restarts any
# proxy except the explicitly requested global --resume below.
write_state || fail_closed 'Could not persist cumulative state.'
if [[ "$MODE" == --status ]]; then
  write_status true ''
  python3 -m json.tool "$STATUS_FILE"
  exit 0
fi

if [[ "$MODE" == --permit ]]; then
  REQUESTED_SERVICE="${REQUESTED_SERVICE%.service}"
  found=false
  for name in "${services[@]}"; do [[ "$name" == "$REQUESTED_SERVICE" ]] && found=true; done
  [[ "$found" == true ]] || fail_closed 'Startup unit is not covered by the protected-service list.'
  if [[ "$blocked_by_guard" == true ]]; then write_status true 'Global block retained pending --resume'; exit 1; fi
  write_status true ''
  exit 0
fi

resume=false
if [[ "$MODE" == --resume ]]; then resume=true; fi
# Legacy auto-resume remains opt-in only. A failure latch is never auto-cleared.
if [[ "$START_NEXT_MONTH" == true && "$new_month" == true && "$blocked_by_guard" == true && ! -e "${STATE_DIR}/blocked" && ! -L "${STATE_DIR}/blocked" ]]; then resume=true; fi
if [[ "$resume" == true ]]; then
  if [[ "$NO_START" == true ]]; then
    for name in "${services[@]}"; do
      state="$(timeout 2s systemctl show "$name.service" --property=ActiveState --value 2>/dev/null)" || state=unknown
      [[ "$state" == inactive || "$state" == failed ]] || { echo "Stop $name before global --resume --no-start; block retained." >&2; exit 1; }
    done
  fi
  blocked_by_guard=false
  write_state || fail_closed 'Could not persist explicit resume.'
  rm -f "${STATE_DIR}/blocked"
  write_status true ''
  # Release before any start: ExecStartPre acquires this same lock. Reload
  # removes only masks emitted by our generator after clearing its latch.
  flock -u 9
  failed=0
  if ! timeout 8s systemctl daemon-reload; then failed=1; fi
  if [[ "$NO_START" != true && "$failed" -eq 0 ]]; then
    for name in "${services[@]}"; do
      if ! timeout 15s systemctl start "$name.service"; then failed=1; fi
    done
  fi
  if ((failed)); then
    flock -w 10 9 || exit 1
    fail_closed 'Global resume incomplete; both proxies blocked again.'
  fi
  if [[ "$NO_START" == true ]]; then
    log_msg 'Global block explicitly cleared; no proxy started. HY2 still needs its own arm/start/activate sequence.'
  else
    log_msg 'Manual global resume completed for the legacy protected service list.'
  fi
  exit 0
fi
if [[ "$blocked_by_guard" == true ]]; then
  write_status true 'Global block retained pending --resume'
  mask_all
  exit 0
fi
write_status true ''
