#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root, for example: sudo INITIAL_USED_GIB=12 bash install-egress-guard.sh" >&2
  exit 1
fi

LIMIT_GIB="${LIMIT_GIB:-180}"
WARN_GIB="${WARN_GIB:-150}"
SERVICE_NAME="${SERVICE_NAME:-sing-box}"
INTERFACE="${INTERFACE:-auto}"
CHECK_INTERVAL="${CHECK_INTERVAL:-1min}"
BILLING_TIMEZONE="${BILLING_TIMEZONE:-America/Los_Angeles}"
START_NEXT_MONTH="${START_NEXT_MONTH:-false}"
INITIAL_USED_GIB="${INITIAL_USED_GIB:-}"
STATE_DIR="/var/lib/gcp-egress-guard"
STATE_FILE="${STATE_DIR}/state"
CONFIG_FILE="/etc/default/gcp-egress-guard"
GUARD_BIN="/usr/local/sbin/gcp-egress-guard"
SERVICE_FILE="/etc/systemd/system/gcp-egress-guard.service"
TIMER_FILE="/etc/systemd/system/gcp-egress-guard.timer"

require_integer() {
  local name="$1"
  local value="$2"
  if ! [[ "$value" =~ ^[0-9]+$ ]]; then
    echo "$name must be a non-negative integer." >&2
    exit 1
  fi
}

require_integer LIMIT_GIB "$LIMIT_GIB"
require_integer WARN_GIB "$WARN_GIB"
if ((WARN_GIB >= LIMIT_GIB)); then
  echo "WARN_GIB must be lower than LIMIT_GIB." >&2
  exit 1
fi

if ! [[ "$SERVICE_NAME" =~ ^[A-Za-z0-9@_.-]+$ ]]; then
  echo "SERVICE_NAME contains unsupported characters." >&2
  exit 1
fi

if [[ "$INTERFACE" != "auto" ]] && ! [[ "$INTERFACE" =~ ^[A-Za-z0-9_.:-]+$ ]]; then
  echo "INTERFACE contains unsupported characters." >&2
  exit 1
fi

if ! [[ "$CHECK_INTERVAL" =~ ^[1-9][0-9]*(s|min|h)$ ]]; then
  echo "CHECK_INTERVAL must look like 30s, 1min, or 1h." >&2
  exit 1
fi

if ! [[ "$BILLING_TIMEZONE" =~ ^[A-Za-z0-9_+.-]+(/[A-Za-z0-9_+.-]+)*$ ]] ||
   [[ "$BILLING_TIMEZONE" == *".."* ]] ||
   [[ ! -e "/usr/share/zoneinfo/${BILLING_TIMEZONE}" ]]; then
  echo "BILLING_TIMEZONE is not an installed IANA time zone." >&2
  exit 1
fi

if [[ "$START_NEXT_MONTH" != "true" && "$START_NEXT_MONTH" != "false" ]]; then
  echo "START_NEXT_MONTH must be true or false." >&2
  exit 1
fi

for required_command in flock ip awk date mktemp systemctl; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    echo "Required command not found: $required_command" >&2
    exit 1
  fi
done

if [[ ! -f "$STATE_FILE" && -z "$INITIAL_USED_GIB" ]]; then
  cat >&2 <<'EOF'
No prior egress state exists. Set INITIAL_USED_GIB to the rounded-up month-to-date
sent GiB from audit-cost-and-usage.sh. Use INITIAL_USED_GIB=0 only when you have
verified that this VM has sent no traffic in the current billing month.
EOF
  exit 1
fi

if [[ -n "$INITIAL_USED_GIB" ]]; then
  require_integer INITIAL_USED_GIB "$INITIAL_USED_GIB"
fi

initial_used_bytes=0
if [[ -n "$INITIAL_USED_GIB" ]]; then
  initial_used_bytes="$((INITIAL_USED_GIB * 1024 * 1024 * 1024))"
fi

install -d -m 0700 "$STATE_DIR"

if [[ -n "$INITIAL_USED_GIB" ]]; then
  exec 8>"${STATE_DIR}/lock"
  if ! flock -w 10 8; then
    echo "Cannot reseed state while the egress guard lock is busy." >&2
    exit 1
  fi

  if [[ "$INTERFACE" == "auto" ]]; then
    seed_interface="$(ip route show default | awk '{print $5; exit}')"
  else
    seed_interface="$INTERFACE"
  fi

  if [[ -z "$seed_interface" || ! -r "/sys/class/net/${seed_interface}/statistics/tx_bytes" ]]; then
    echo "Cannot seed state: tx_bytes is unavailable for interface '${seed_interface}'." >&2
    exit 1
  fi

  seed_tx_bytes="$(<"/sys/class/net/${seed_interface}/statistics/tx_bytes")"
  if ! [[ "$seed_tx_bytes" =~ ^[0-9]+$ ]]; then
    echo "Cannot seed state: tx_bytes is invalid." >&2
    exit 1
  fi

  state_tmp="$(mktemp "${STATE_FILE}.tmp.XXXXXX")"
  chmod 0600 "$state_tmp"
  {
    printf 'state_month=%s\n' "$(TZ="$BILLING_TIMEZONE" date +%Y-%m)"
    printf 'last_tx=%s\n' "$seed_tx_bytes"
    printf 'used_bytes=%s\n' "$initial_used_bytes"
    printf 'blocked_by_guard=false\n'
    printf 'warning_logged=false\n'
  } >"$state_tmp"

  if [[ -f "$STATE_FILE" ]]; then
    state_backup="${STATE_FILE}.bak.$(date -u +%Y%m%d-%H%M%S).$$"
    mv "$STATE_FILE" "$state_backup"
    echo "Backed up the previous egress state to $state_backup"
  fi

  mv -f "$state_tmp" "$STATE_FILE"
  flock -u 8
fi

umask 077
cat >"$CONFIG_FILE" <<EOF
LIMIT_GIB=${LIMIT_GIB}
WARN_GIB=${WARN_GIB}
SERVICE_NAME=${SERVICE_NAME}
INTERFACE=${INTERFACE}
BILLING_TIMEZONE=${BILLING_TIMEZONE}
MASK_ON_LIMIT=true
START_NEXT_MONTH=${START_NEXT_MONTH}
EOF
chmod 0600 "$CONFIG_FILE"

cat >"$GUARD_BIN" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

CONFIG_FILE="/etc/default/gcp-egress-guard"
STATE_DIR="/var/lib/gcp-egress-guard"
STATE_FILE="${STATE_DIR}/state"
LOCK_FILE="${STATE_DIR}/lock"
MODE="${1:-run}"

LIMIT_GIB=180
WARN_GIB=150
SERVICE_NAME=sing-box
INTERFACE=auto
BILLING_TIMEZONE=America/Los_Angeles
MASK_ON_LIMIT=true
START_NEXT_MONTH=false

if [[ "$MODE" != "run" && "$MODE" != "--resume" ]]; then
  echo "Usage: gcp-egress-guard [--resume]" >&2
  exit 2
fi

# shellcheck disable=SC1090
source "$CONFIG_FILE"

install -d -m 0700 "$STATE_DIR"
exec 9>"$LOCK_FILE"
if ! flock -w 10 9; then
  logger -t gcp-egress-guard "Could not acquire the egress guard lock." || true
  exit 1
fi

log_msg() {
  local msg="$1"
  echo "$msg"
  logger -t gcp-egress-guard "$msg" || true
}

mask_service() {
  systemctl stop --no-block "$SERVICE_NAME" >/dev/null 2>&1 || true
  if [[ "$MASK_ON_LIMIT" == "true" ]]; then
    # A runtime mask does not overwrite a unit file installed under /etc. The
    # enabled guard service recreates the mask before sing-box on every boot.
    systemctl mask --runtime "$SERVICE_NAME" >/dev/null 2>&1 || {
      log_msg "Failed to mask ${SERVICE_NAME}; manual intervention is required."
      return 1
    }
  fi
}

fail_closed() {
  log_msg "FAIL CLOSED: $1 Stopping and masking ${SERVICE_NAME}."
  mask_service || true
  exit 1
}

detect_interface() {
  if [[ "$INTERFACE" != "auto" ]]; then
    echo "$INTERFACE"
    return
  fi
  ip route show default | awk '{print $5; exit}'
}

write_state() {
  local tmp
  tmp="$(mktemp "${STATE_FILE}.tmp.XXXXXX")"
  chmod 0600 "$tmp"
  {
    printf 'state_month=%s\n' "$state_month"
    printf 'last_tx=%s\n' "$last_tx"
    printf 'used_bytes=%s\n' "$used_bytes"
    printf 'blocked_by_guard=%s\n' "$blocked_by_guard"
    printf 'warning_logged=%s\n' "$warning_logged"
  } >"$tmp"
  mv -f "$tmp" "$STATE_FILE"
}

load_state() {
  local key value
  while IFS='=' read -r key value; do
    case "$key" in
      state_month) state_month="$value" ;;
      last_tx) last_tx="$value" ;;
      used_bytes) used_bytes="$value" ;;
      blocked_by_guard) blocked_by_guard="$value" ;;
      warning_logged) warning_logged="$value" ;;
      "") ;;
      *) return 1 ;;
    esac
  done <"$STATE_FILE"

  [[ "$state_month" =~ ^[0-9]{4}-[0-9]{2}$ ]] || return 1
  [[ "$last_tx" =~ ^[0-9]+$ ]] || return 1
  [[ "$used_bytes" =~ ^[0-9]+$ ]] || return 1
  [[ "$blocked_by_guard" == "true" || "$blocked_by_guard" == "false" ]] || return 1
  [[ "$warning_logged" == "true" || "$warning_logged" == "false" ]] || return 1
}

iface="$(detect_interface)"
if [[ -z "$iface" || ! -r "/sys/class/net/${iface}/statistics/tx_bytes" ]]; then
  fail_closed "Cannot read tx_bytes for interface '${iface}'."
fi

month="$(TZ="$BILLING_TIMEZONE" date +%Y-%m)"
tx_bytes="$(<"/sys/class/net/${iface}/statistics/tx_bytes")"
if ! [[ "$tx_bytes" =~ ^[0-9]+$ ]]; then
  fail_closed "The tx_bytes counter is invalid."
fi

state_month=""
last_tx=""
used_bytes=""
blocked_by_guard=false
warning_logged=false

if [[ -f "$STATE_FILE" ]]; then
  if ! load_state; then
    fail_closed "The state file is missing required fields or contains invalid data."
  fi
else
  fail_closed "The state file is missing; re-audit month-to-date usage before reseeding it."
fi

if [[ "$state_month" != "$month" ]]; then
  previously_blocked="$blocked_by_guard"
  state_month="$month"
  last_tx="$tx_bytes"
  used_bytes=0
  warning_logged=false
  blocked_by_guard="$previously_blocked"
  write_state

  if [[ "$blocked_by_guard" == "true" && "$START_NEXT_MONTH" == "true" ]]; then
    blocked_by_guard=false
    write_state
    systemctl unmask "$SERVICE_NAME" >/dev/null 2>&1 || fail_closed "Failed to unmask ${SERVICE_NAME}."
    systemctl enable --now "$SERVICE_NAME" >/dev/null 2>&1 || fail_closed "Failed to start ${SERVICE_NAME}."
    log_msg "New billing month detected; reset the counter and resumed ${SERVICE_NAME}."
  elif [[ "$blocked_by_guard" == "true" ]]; then
    log_msg "New billing month detected; ${SERVICE_NAME} remains blocked pending manual --resume."
  else
    log_msg "New billing month detected; reset the counter."
  fi
fi

if ((tx_bytes >= last_tx)); then
  delta=$((tx_bytes - last_tx))
else
  delta=$tx_bytes
fi

used_bytes=$((used_bytes + delta))
last_tx="$tx_bytes"

gib=$((1024 * 1024 * 1024))
limit_bytes=$((LIMIT_GIB * gib))
warn_bytes=$((WARN_GIB * gib))
used_gib=$((used_bytes / gib))

if [[ "$MODE" == "--resume" ]]; then
  if ((used_bytes >= limit_bytes)); then
    blocked_by_guard=true
    write_state
    fail_closed "Cannot resume: ${used_gib} GiB >= ${LIMIT_GIB} GiB."
  fi
  blocked_by_guard=false
  write_state
  systemctl unmask "$SERVICE_NAME" >/dev/null 2>&1 || fail_closed "Failed to unmask ${SERVICE_NAME}."
  systemctl enable --now "$SERVICE_NAME" >/dev/null 2>&1 || fail_closed "Failed to start ${SERVICE_NAME}."
  log_msg "Manual resume completed at ${used_gib} GiB / ${LIMIT_GIB} GiB."
  exit 0
fi

if ((used_bytes >= limit_bytes)); then
  blocked_by_guard=true
  write_state
  log_msg "Monthly egress limit reached: ${used_gib} GiB >= ${LIMIT_GIB} GiB."
  mask_service
  exit 0
fi

if [[ "$blocked_by_guard" == "true" ]]; then
  write_state
  mask_service
  exit 0
fi

if ((used_bytes >= warn_bytes)) && [[ "$warning_logged" == "false" ]]; then
  warning_logged=true
  log_msg "Monthly egress warning: ${used_gib} GiB >= ${WARN_GIB} GiB."
fi

write_state
EOF

chmod 0755 "$GUARD_BIN"

cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=Google Cloud egress guard for low-cost sing-box nodes
After=local-fs.target
Before=${SERVICE_NAME}.service

[Service]
Type=oneshot
ExecStart=${GUARD_BIN}

[Install]
WantedBy=multi-user.target
EOF

cat >"$TIMER_FILE" <<EOF
[Unit]
Description=Run Google Cloud egress guard periodically

[Timer]
OnBootSec=30s
OnUnitActiveSec=${CHECK_INTERVAL}
RandomizedDelaySec=10s
Persistent=true

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
systemctl enable gcp-egress-guard.service gcp-egress-guard.timer
systemctl start gcp-egress-guard.service
systemctl start gcp-egress-guard.timer

cat <<EOF
Installed the fail-closed gcp-egress-guard.
Config: $CONFIG_FILE
State: $STATE_FILE
Billing timezone: $BILLING_TIMEZONE
Warning: $WARN_GIB GiB
Limit: $LIMIT_GIB GiB
Automatic next-month resume: $START_NEXT_MONTH

Manual resume after checking Billing:
  sudo $GUARD_BIN --resume
EOF
