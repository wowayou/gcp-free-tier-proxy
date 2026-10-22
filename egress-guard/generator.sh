#!/usr/bin/env bash
# Generator.early overrides /etc units too. Masks are owned by durable guard
# state, not static symlinks that --resume could confuse with operator masks.
set -euo pipefail
out="${2:?systemd generator early-output directory required}"
config="${GUARD_CONFIG_FILE:-/etc/default/gcp-egress-guard}"
state_dir="${GUARD_STATE_DIR:-/var/lib/gcp-egress-guard}"
SERVICE_NAMES="" SERVICE_NAME=sing-box
LIMIT_GIB=180 WARN_GIB=150 MASK_ON_LIMIT=true START_NEXT_MONTH=false
valid=true
declare -A seen=()
if [[ -r "$config" ]]; then
  while IFS='=' read -r key value; do
    [[ -z "$key" || "$key" == \#* ]] && continue
    if [[ -n "${seen[$key]:-}" ]]; then valid=false; fi
    seen[$key]=1
    value="${value#\"}"; value="${value%\"}"
    case "$key" in
      SERVICE_NAMES|SERVICE_NAME|LIMIT_GIB|WARN_GIB|MASK_ON_LIMIT|START_NEXT_MONTH) printf -v "$key" '%s' "$value" ;;
      INTERFACE|BILLING_TIMEZONE) ;;
      *) valid=false ;;
    esac
  done <"$config"
else
  valid=false
fi
if ! [[ "$LIMIT_GIB" =~ ^[0-9]{1,6}$ && "$WARN_GIB" =~ ^[0-9]{1,6}$ ]] ||
   ((10#$LIMIT_GIB<1 || 10#$LIMIT_GIB>180 || 10#$WARN_GIB>=10#$LIMIT_GIB)) ||
   [[ "$MASK_ON_LIMIT" != true ]]; then valid=false; fi
read -r -a names <<<"${SERVICE_NAMES:-$SERVICE_NAME}"
((${#names[@]}>0 && ${#names[@]}<=8)) || valid=false
for i in "${!names[@]}"; do
  name="${names[$i]%.service}"
  names[$i]="$name"
  [[ "$name" =~ ^[A-Za-z0-9@_.-]+$ && "$name" != -* && "$name" != *..* ]] || valid=false
  [[ "$name" != gcp-egress-guard* && "$name" != hy2-account* && "$name" != hy2-health* ]] || valid=false
  [[ "$name" != hysteria-server || "$START_NEXT_MONTH" == false ]] || valid=false
done
if [[ "$valid" != true ]]; then names=(sing-box hysteria-server); fi

# Validate the legacy/current state before trusting its one permit flag.
state_valid=true
state_month="" last_tx="" used_bytes="" blocked_by_guard=true warning_logged=false
seen=()
if [[ -r "$state_dir/state" ]]; then
  while IFS='=' read -r key value; do
    if [[ -z "$key" || -n "${seen[$key]:-}" ]]; then state_valid=false; continue; fi
    seen[$key]=1
    case "$key" in
      state_month|last_tx|used_bytes|blocked_by_guard|warning_logged) printf -v "$key" '%s' "$value" ;;
      last_boot_id|last_interface|updated_epoch) ;;
      *) state_valid=false ;;
    esac
  done <"$state_dir/state"
else
  state_valid=false
fi
for key in state_month last_tx used_bytes blocked_by_guard warning_logged; do
  [[ -n "${seen[$key]:-}" ]] || state_valid=false
done
[[ "$state_month" =~ ^[0-9]{4}-(0[1-9]|1[0-2])$ && "$last_tx" =~ ^[0-9]{1,18}$ && "$used_bytes" =~ ^[0-9]{1,18}$ ]] || state_valid=false
[[ "$blocked_by_guard" == true || "$blocked_by_guard" == false ]] || state_valid=false
[[ "$warning_logged" == true || "$warning_logged" == false ]] || state_valid=false
if [[ "$valid" != true || "$state_valid" != true || "$blocked_by_guard" == true || -e "$state_dir/blocked" || -L "$state_dir/blocked" ]]; then
  mkdir -p "$out"
  for name in "${names[@]}"; do ln -sf /dev/null "$out/$name.service"; done
fi
