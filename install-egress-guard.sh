#!/usr/bin/env bash
set -euo pipefail

[[ "${EUID}" -eq 0 ]] || { echo 'Run on the VM as root.' >&2; exit 1; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Routine reinstall preserves existing policy and the protected-service list.
# Never silently shrink a two-proxy guard back to the legacy single service.
CONFIG_FILE=/etc/default/gcp-egress-guard
old_services="" old_legacy="" old_limit="" old_warn="" old_interface="" old_timezone="" old_auto=""
if [[ -r "$CONFIG_FILE" ]]; then
  while IFS='=' read -r key value; do
    value="${value#\"}"; value="${value%\"}"
    case "$key" in
      SERVICE_NAMES) old_services="$value" ;; SERVICE_NAME) old_legacy="$value" ;;
      LIMIT_GIB) old_limit="$value" ;; WARN_GIB) old_warn="$value" ;;
      INTERFACE) old_interface="$value" ;; BILLING_TIMEZONE) old_timezone="$value" ;;
      START_NEXT_MONTH) old_auto="$value" ;;
    esac
  done <"$CONFIG_FILE"
fi
LIMIT_GIB="${LIMIT_GIB:-${old_limit:-180}}"
WARN_GIB="${WARN_GIB:-${old_warn:-150}}"
# If the independent HY2 account backend already exists, never silently
# install a legacy single-service guard that would leave it unprotected.
default_services="${old_services:-${old_legacy:-sing-box}}"
if [[ -z "${SERVICE_NAMES:-}" && -z "${SERVICE_NAME:-}" && -z "$old_services" && -z "$old_legacy" && -f /etc/hy2-account/config.json ]]; then
  default_services="sing-box hysteria-server"
fi
SERVICE_NAMES="${SERVICE_NAMES:-${SERVICE_NAME:-$default_services}}"
INTERFACE="${INTERFACE:-${old_interface:-auto}}"
CHECK_INTERVAL="${CHECK_INTERVAL:-1min}"
BILLING_TIMEZONE="${BILLING_TIMEZONE:-${old_timezone:-America/Los_Angeles}}"
START_NEXT_MONTH="${START_NEXT_MONTH:-${old_auto:-false}}"
INITIAL_USED_GIB="${INITIAL_USED_GIB:-}"
STATE_DIR=/var/lib/gcp-egress-guard
STATE_FILE="$STATE_DIR/state"
GUARD_BIN=/usr/local/sbin/gcp-egress-guard

integer() { [[ "$2" =~ ^[0-9]{1,6}$ ]] || { echo "$1 must be a nonnegative integer (max 6 digits)." >&2; exit 1; }; }
integer LIMIT_GIB "$LIMIT_GIB"; integer WARN_GIB "$WARN_GIB"
((10#$LIMIT_GIB>0 && 10#$LIMIT_GIB<=180 && 10#$WARN_GIB<10#$LIMIT_GIB)) || { echo 'Require 0 <= WARN_GIB < LIMIT_GIB <= 180; do not expand the egress budget.' >&2; exit 1; }
[[ "$START_NEXT_MONTH" == true || "$START_NEXT_MONTH" == false ]] || exit 1
case "$CHECK_INTERVAL" in 30s|60s|1min) ;; *) echo 'Use CHECK_INTERVAL=30s, 60s or 1min; do not weaken the bounded local guard cadence.' >&2; exit 1 ;; esac
[[ "$BILLING_TIMEZONE" =~ ^[A-Za-z0-9_+.-]+(/[A-Za-z0-9_+.-]+)*$ && "$BILLING_TIMEZONE" != *..* && -e "/usr/share/zoneinfo/$BILLING_TIMEZONE" ]] || exit 1
[[ "$INTERFACE" == auto || "$INTERFACE" =~ ^[A-Za-z0-9_.:-]+$ ]] || exit 1
read -r -a services <<<"$SERVICE_NAMES"
((${#services[@]}>0 && ${#services[@]}<=8)) || exit 1
for i in "${!services[@]}"; do
  name="${services[$i]%.service}"
  [[ "$name" =~ ^[A-Za-z0-9@_.-]+$ && "$name" != -* && "$name" != *..* && "$name" != gcp-egress-guard* && "$name" != hy2-account* && "$name" != hy2-health* ]] || exit 1
  [[ "$name" != hysteria-server || "$START_NEXT_MONTH" == false ]] || { echo 'HY2 requires manual accounting review after a stop/reboot; START_NEXT_MONTH must be false.' >&2; exit 1; }
  services[$i]="$name"
done
read -r -a previously_protected <<<"${old_services:-$old_legacy}"
for prior in "${previously_protected[@]}"; do
  prior="${prior%.service}"
  [[ " ${services[*]} " == *" $prior "* ]] || { echo "Refusing to remove existing protected service $prior during reinstall." >&2; exit 1; }
done
SERVICE_NAMES="${services[*]}"
for c in flock ip awk date mktemp systemctl python3 timeout sync; do command -v "$c" >/dev/null || { echo "Missing $c" >&2; exit 1; }; done
[[ -f "$ROOT/egress-guard/runtime.sh" && -f "$ROOT/egress-guard/generator.sh" ]] || { echo 'Upload install-egress-guard.sh together with egress-guard/.' >&2; exit 1; }
if [[ ! -f "$STATE_FILE" && -z "$INITIAL_USED_GIB" ]]; then
  echo 'No prior state. Audit month-to-date VM sent bytes first, then set rounded-up INITIAL_USED_GIB. Never assume zero.' >&2
  exit 1
fi
[[ -z "$INITIAL_USED_GIB" ]] || integer INITIAL_USED_GIB "$INITIAL_USED_GIB"
install -d -m 0700 "$STATE_DIR"

if [[ -n "$INITIAL_USED_GIB" ]]; then
  exec 8>"$STATE_DIR/lock"; flock -w 10 8 || exit 1
  blocked=false
  [[ ! -f "$STATE_DIR/blocked" ]] || blocked=true
  if [[ -f "$STATE_FILE" ]]; then
    grep -qx 'blocked_by_guard=false' "$STATE_FILE" || blocked=true
    backup="${STATE_FILE}.bak.$(date -u +%Y%m%d-%H%M%S).$$"
    cp --preserve=mode,ownership "$STATE_FILE" "$backup"
    echo "Prior state backup: $backup (a prior global block is retained)"
  fi
  if [[ "$INTERFACE" == auto ]]; then iface="$(ip route show default | awk '{print $5; exit}')"; else iface="$INTERFACE"; fi
  [[ -n "$iface" && -r "/sys/class/net/$iface/statistics/tx_bytes" ]] || exit 1
  tx="$(<"/sys/class/net/$iface/statistics/tx_bytes")"
  [[ "$tx" =~ ^[0-9]{1,18}$ ]] || exit 1
  tmp="$(mktemp "$STATE_FILE.tmp.XXXXXX")"; chmod 0600 "$tmp"
  {
    printf 'state_month=%s\n' "$(TZ="$BILLING_TIMEZONE" date +%Y-%m)"
    printf 'last_tx=%s\n' "$tx"
    printf 'used_bytes=%s\n' "$((10#$INITIAL_USED_GIB * 1024 * 1024 * 1024))"
    printf 'blocked_by_guard=%s\n' "$blocked"
    printf 'warning_logged=false\n'
    printf 'last_boot_id=%s\n' "$(</proc/sys/kernel/random/boot_id)"
    printf 'last_interface=%s\n' "$iface"
    printf 'updated_epoch=%s\n' "$(date +%s)"
  } >"$tmp"
  sync -f "$tmp"; mv -f "$tmp" "$STATE_FILE"; sync -f "$STATE_DIR"
  flock -u 8
fi

umask 077
[[ ! -f "$CONFIG_FILE" ]] || cp --preserve=mode,ownership "$CONFIG_FILE" "${CONFIG_FILE}.bak.$(date -u +%Y%m%d-%H%M%S).$$"
tmp="$(mktemp "$CONFIG_FILE.tmp.XXXXXX")"
printf 'LIMIT_GIB=%s\nWARN_GIB=%s\nSERVICE_NAMES="%s"\nINTERFACE=%s\nBILLING_TIMEZONE=%s\nMASK_ON_LIMIT=true\nSTART_NEXT_MONTH=%s\n' \
 "$LIMIT_GIB" "$WARN_GIB" "$SERVICE_NAMES" "$INTERFACE" "$BILLING_TIMEZONE" "$START_NEXT_MONTH" >"$tmp"
chmod 0600 "$tmp"; sync -f "$tmp"; mv -f "$tmp" "$CONFIG_FILE"; sync -f "$(dirname "$CONFIG_FILE")"
install -m 0755 "$ROOT/egress-guard/runtime.sh" "$GUARD_BIN.new.$$"
mv -f "$GUARD_BIN.new.$$" "$GUARD_BIN"
install -d -m 0755 /etc/systemd/system-generators
install -m 0755 "$ROOT/egress-guard/generator.sh" /etc/systemd/system-generators/gcp-egress-guard

before=""
for name in "${services[@]}"; do
  before+=" $name.service"
  dropin="/etc/systemd/system/$name.service.d"
  install -d -m 0755 "$dropin"
  # '+' runs only this known permission check as root, even for unprivileged
  # HY2. Gate takes no privileged action other than its own guard accounting.
  printf '[Unit]\nWants=gcp-egress-guard.service\nAfter=gcp-egress-guard.service\n\n[Service]\nExecStartPre=+%s --permit %s\nReadWritePaths=-/var/lib/gcp-egress-guard -/run/gcp-egress-guard\n' "$GUARD_BIN" "$name" >"$dropin/10-egress-guard.conf"
  chmod 0644 "$dropin/10-egress-guard.conf"
done
cat >/etc/systemd/system/gcp-egress-guard.service <<EOF
[Unit]
Description=VM monthly egress guard for explicit proxy service list
After=local-fs.target
Before=$before

[Service]
Type=oneshot
ExecStart=$GUARD_BIN
TimeoutStartSec=150s

[Install]
WantedBy=multi-user.target
EOF
cat >/etc/systemd/system/gcp-egress-guard.timer <<EOF
[Unit]
Description=Periodic VM egress guard (local counters only)

[Timer]
OnBootSec=30s
OnUnitActiveSec=$CHECK_INTERVAL
RandomizedDelaySec=0
Persistent=true

[Install]
WantedBy=timers.target
EOF
chmod 0644 /etc/systemd/system/gcp-egress-guard.{service,timer}
systemctl daemon-reload
systemctl enable gcp-egress-guard.service gcp-egress-guard.timer
systemctl start gcp-egress-guard.service
systemctl start gcp-egress-guard.timer
printf 'Installed fail-closed guard. Services: %s\nState preserved: %s\nWarning/limit: %s/%s GiB\nManual resume: sudo %s --resume (independent HY2: add --no-start, then arm/start/activate separately)\n' "$SERVICE_NAMES" "$STATE_FILE" "$WARN_GIB" "$LIMIT_GIB" "$GUARD_BIN"
