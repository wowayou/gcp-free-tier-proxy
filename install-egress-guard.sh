#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root, for example: sudo LIMIT_GIB=180 bash install-egress-guard.sh" >&2
  exit 1
fi

LIMIT_GIB="${LIMIT_GIB:-180}"
WARN_GIB="${WARN_GIB:-150}"
SERVICE_NAME="${SERVICE_NAME:-sing-box}"
INTERFACE="${INTERFACE:-auto}"
CHECK_INTERVAL="${CHECK_INTERVAL:-5min}"
STATE_DIR="/var/lib/gcp-egress-guard"
CONFIG_FILE="/etc/default/gcp-egress-guard"
GUARD_BIN="/usr/local/sbin/gcp-egress-guard"
SERVICE_FILE="/etc/systemd/system/gcp-egress-guard.service"
TIMER_FILE="/etc/systemd/system/gcp-egress-guard.timer"

install -d -m 0755 "$STATE_DIR"

cat >"$CONFIG_FILE" <<EOF
LIMIT_GIB=${LIMIT_GIB}
WARN_GIB=${WARN_GIB}
SERVICE_NAME=${SERVICE_NAME}
INTERFACE=${INTERFACE}
MASK_ON_LIMIT=true
START_NEXT_MONTH=true
EOF

cat >"$GUARD_BIN" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

CONFIG_FILE="/etc/default/gcp-egress-guard"
STATE_DIR="/var/lib/gcp-egress-guard"
STATE_FILE="${STATE_DIR}/state"

LIMIT_GIB=180
WARN_GIB=150
SERVICE_NAME=sing-box
INTERFACE=auto
MASK_ON_LIMIT=true
START_NEXT_MONTH=true

# shellcheck disable=SC1090
source "$CONFIG_FILE"

detect_interface() {
  if [[ "$INTERFACE" != "auto" ]]; then
    echo "$INTERFACE"
    return
  fi

  ip route show default | awk '{print $5; exit}'
}

log_msg() {
  local msg="$1"
  echo "$msg"
  logger -t gcp-egress-guard "$msg" || true
}

iface="$(detect_interface)"
if [[ -z "$iface" || ! -r "/sys/class/net/${iface}/statistics/tx_bytes" ]]; then
  log_msg "Cannot read tx_bytes for interface '${iface}'."
  exit 1
fi

mkdir -p "$STATE_DIR"

month="$(date -u +%Y-%m)"
tx_bytes="$(cat "/sys/class/net/${iface}/statistics/tx_bytes")"

state_month="$month"
last_tx="$tx_bytes"
used_bytes=0

if [[ -f "$STATE_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$STATE_FILE"
fi

if [[ "${state_month}" != "$month" ]]; then
  state_month="$month"
  last_tx="$tx_bytes"
  used_bytes=0

  if [[ "${START_NEXT_MONTH}" == "true" ]]; then
    systemctl unmask "$SERVICE_NAME" >/dev/null 2>&1 || true
    systemctl enable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
    log_msg "New month detected; reset counter and attempted to start ${SERVICE_NAME}."
  fi
fi

if (( tx_bytes >= last_tx )); then
  delta=$((tx_bytes - last_tx))
else
  delta=$tx_bytes
fi

used_bytes=$((used_bytes + delta))
last_tx=$tx_bytes

cat >"$STATE_FILE" <<STATE
state_month=${state_month}
last_tx=${last_tx}
used_bytes=${used_bytes}
STATE

gib=$((1024 * 1024 * 1024))
limit_bytes=$((LIMIT_GIB * gib))
warn_bytes=$((WARN_GIB * gib))
used_gib=$((used_bytes / gib))

if (( used_bytes >= limit_bytes )); then
  log_msg "Monthly egress limit reached: ${used_gib} GiB >= ${LIMIT_GIB} GiB. Stopping ${SERVICE_NAME}."
  systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  if [[ "${MASK_ON_LIMIT}" == "true" ]]; then
    systemctl mask "$SERVICE_NAME" >/dev/null 2>&1 || true
  fi
  exit 0
fi

if (( used_bytes >= warn_bytes )); then
  log_msg "Monthly egress warning: ${used_gib} GiB >= ${WARN_GIB} GiB."
else
  log_msg "Monthly egress OK: ${used_gib} GiB / ${LIMIT_GIB} GiB."
fi
EOF

chmod 0755 "$GUARD_BIN"

cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=Google Cloud egress guard for low-cost sing-box nodes

[Service]
Type=oneshot
ExecStart=${GUARD_BIN}
EOF

cat >"$TIMER_FILE" <<EOF
[Unit]
Description=Run Google Cloud egress guard periodically

[Timer]
OnBootSec=2min
OnUnitActiveSec=${CHECK_INTERVAL}
RandomizedDelaySec=30s
Persistent=true

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
systemctl enable --now gcp-egress-guard.timer
systemctl start gcp-egress-guard.service || true

echo "Installed gcp-egress-guard."
echo "Config: $CONFIG_FILE"
echo "State: $STATE_DIR"
echo "Timer: systemctl status gcp-egress-guard.timer"
