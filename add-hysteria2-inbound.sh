#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo bash add-hysteria2-inbound.sh" >&2
  exit 1
fi

SERVICE_NAME="${SERVICE_NAME:-sing-box}"
SING_BOX_BIN="${SING_BOX_BIN:-}"
HY2_PORT="${HY2_PORT:-443}"
HY2_TAG="${HY2_TAG:-hysteria2-in}"
HY2_USER_NAME="${HY2_USER_NAME:-friend-1}"
HY2_SERVER_ADDRESS="${HY2_SERVER_ADDRESS:-}"
HY2_REPLACE_USERS="${HY2_REPLACE_USERS:-false}"
HY2_REDUCE_LOGS="${HY2_REDUCE_LOGS:-true}"
CERT_DIR="${CERT_DIR:-/root/ygkkkca}"
CERT_PATH="${CERT_PATH:-${CERT_DIR}/fullchain.cer}"
KEY_PATH="${KEY_PATH:-${CERT_DIR}/private.key}"
CREDENTIAL_DIR="${CREDENTIAL_DIR:-/root/hysteria2-credentials}"

CONFIG_CANDIDATES=(
  "/etc/s-box/sb.json"
  "/etc/sing-box/config.json"
  "/usr/local/etc/sing-box/config.json"
)

find_config() {
  local path
  for path in "${CONFIG_CANDIDATES[@]}"; do
    if [[ -f "$path" ]]; then
      echo "$path"
      return 0
    fi
  done
  return 1
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

udp_port_owner() {
  ss -H -lunp 2>/dev/null | awk -v p=":${HY2_PORT}" '$4 ~ p"$" {print}'
}

detect_domain() {
  if [[ -n "${HY2_DOMAIN:-}" ]]; then
    if ! openssl x509 -in "$CERT_PATH" -noout -checkhost "$HY2_DOMAIN" >/dev/null 2>&1; then
      echo "HY2_DOMAIN is not covered by the certificate SAN: $HY2_DOMAIN" >&2
      exit 1
    fi
    echo "$HY2_DOMAIN"
    return
  fi

  local domain
  domain="$({
    openssl x509 -in "$CERT_PATH" -noout -ext subjectAltName 2>/dev/null |
      tr ',' '\n' |
      sed -n 's/.*DNS:\([^ ]*\).*/\1/p' |
      grep -v '^\*\.' |
      head -n1 || true
  })"

  if [[ -z "$domain" ]]; then
    echo "Cannot infer a concrete hostname from a wildcard-only certificate." >&2
    echo "Set HY2_DOMAIN to a hostname covered by the certificate." >&2
    exit 1
  fi

  echo "$domain"
}

version_at_least_1_11() {
  local version="$1"
  local major minor
  IFS=. read -r major minor _ <<<"$version"
  [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ ]] || return 1
  ((major > 1 || (major == 1 && minor >= 11)))
}

rollback() {
  local reason="$1"
  trap - ERR
  echo "$reason" >&2
  cp "$BACKUP_PATH" "$CONFIG_PATH"
  if "$SING_BOX_BIN" check -c "$CONFIG_PATH" >/dev/null 2>&1; then
    systemctl restart "$SERVICE_NAME" || true
  fi
  echo "Restored backup: $BACKUP_PATH" >&2
  exit 1
}

require_cmd openssl
require_cmd python3
require_cmd ss
require_cmd systemctl
require_cmd install
require_cmd readlink

if [[ -z "$SING_BOX_BIN" ]]; then
  if command -v sing-box >/dev/null 2>&1; then
    SING_BOX_BIN="$(command -v sing-box)"
  elif [[ -x /etc/s-box/sing-box ]]; then
    SING_BOX_BIN="/etc/s-box/sing-box"
  else
    echo "Cannot find the sing-box executable. Set SING_BOX_BIN explicitly." >&2
    exit 1
  fi
fi

if [[ ! -x "$SING_BOX_BIN" ]]; then
  echo "sing-box executable is not runnable: $SING_BOX_BIN" >&2
  exit 1
fi

if ! systemctl is-active --quiet gcp-egress-guard.timer; then
  echo "gcp-egress-guard.timer must be active before HY2 is added." >&2
  exit 1
fi

if [[ ! -r /var/lib/gcp-egress-guard/state ]] ||
   ! grep -q '^blocked_by_guard=' /var/lib/gcp-egress-guard/state; then
  echo "Install the current fail-closed egress guard and seed its month-to-date baseline first." >&2
  exit 1
fi

if ! [[ "$HY2_PORT" =~ ^[0-9]+$ ]] || ((HY2_PORT < 1 || HY2_PORT > 65535)); then
  echo "HY2_PORT must be an integer from 1 to 65535." >&2
  exit 1
fi

if ! [[ "$HY2_USER_NAME" =~ ^[A-Za-z0-9._-]{1,64}$ ]]; then
  echo "HY2_USER_NAME must match [A-Za-z0-9._-] and be at most 64 characters." >&2
  exit 1
fi

if [[ "$HY2_REPLACE_USERS" != "true" && "$HY2_REPLACE_USERS" != "false" ]]; then
  echo "HY2_REPLACE_USERS must be true or false." >&2
  exit 1
fi

if [[ "$HY2_REDUCE_LOGS" != "true" && "$HY2_REDUCE_LOGS" != "false" ]]; then
  echo "HY2_REDUCE_LOGS must be true or false." >&2
  exit 1
fi

if [[ ! -f "$CERT_PATH" && -f "${CERT_DIR}/cert.crt" ]]; then
  CERT_PATH="${CERT_DIR}/cert.crt"
fi

if [[ ! -f "$CERT_PATH" ]]; then
  echo "Certificate file not found: $CERT_PATH" >&2
  exit 1
fi

if [[ ! -f "$KEY_PATH" ]]; then
  echo "Private key file not found: $KEY_PATH" >&2
  exit 1
fi

CONFIG_PATH="$(find_config)" || {
  echo "No known sing-box config file found." >&2
  exit 1
}

SING_BOX_VERSION="$("$SING_BOX_BIN" version | awk 'NR == 1 {print $3}')"
if ! version_at_least_1_11 "$SING_BOX_VERSION"; then
  echo "sing-box 1.11 or newer is required by this managed HY2 configuration." >&2
  echo "Detected version: ${SING_BOX_VERSION:-unknown}" >&2
  exit 1
fi

HY2_DOMAIN="$(detect_domain)"
if [[ -z "$HY2_SERVER_ADDRESS" ]]; then
  HY2_SERVER_ADDRESS="$HY2_DOMAIN"
fi
if ! [[ "$HY2_SERVER_ADDRESS" =~ ^[A-Za-z0-9._:-]{1,253}$ ]]; then
  echo "HY2_SERVER_ADDRESS contains unsupported characters." >&2
  exit 1
fi

port_owner="$(udp_port_owner || true)"
if [[ -n "$port_owner" ]] && ! grep -q "$SERVICE_NAME" <<<"$port_owner"; then
  echo "UDP port ${HY2_PORT} is already used by another process:" >&2
  echo "$port_owner" >&2
  exit 1
fi

existing_password="$({
  python3 - "$CONFIG_PATH" "$HY2_TAG" "$HY2_USER_NAME" <<'PY'
import json
import sys

config_path, tag, user_name = sys.argv[1:]
with open(config_path, encoding="utf-8") as handle:
    data = json.load(handle)

for inbound in data.get("inbounds", []):
    if inbound.get("tag") != tag:
        continue
    if inbound.get("type") != "hysteria2":
        raise SystemExit(f"Inbound tag {tag!r} already exists with a different type")
    for user in inbound.get("users", []):
        if user.get("name") == user_name and user.get("password"):
            print(user["password"], end="")
            raise SystemExit(0)
raise SystemExit(0)
PY
})"

if [[ -n "${HY2_PASSWORD:-}" ]]; then
  password="$HY2_PASSWORD"
elif [[ -n "$existing_password" ]]; then
  password="$existing_password"
else
  password="$(openssl rand -hex 32)"
fi

if (( ${#password} < 24 )); then
  echo "HY2_PASSWORD must contain at least 24 characters." >&2
  exit 1
fi

install -d -m 0700 "$CREDENTIAL_DIR"
CREDENTIAL_PATH="${CREDENTIAL_DIR}/${HY2_USER_NAME}.json"
BACKUP_PATH="${CONFIG_PATH}.bak.$(date -u +%Y%m%d-%H%M%S).$$"
cp "$CONFIG_PATH" "$BACKUP_PATH"

rollback_on_error() {
  local status="$?"
  rollback "Unexpected failure while applying HY2 (exit ${status})."
}
trap rollback_on_error ERR

python3 - "$CONFIG_PATH" "$HY2_TAG" "$HY2_PORT" "$HY2_USER_NAME" "$HY2_DOMAIN" "$CERT_PATH" "$KEY_PATH" "$HY2_REPLACE_USERS" "$HY2_REDUCE_LOGS" 3<<<"$password" <<'PY'
import json
import os
import stat
import sys
import tempfile
from pathlib import Path

(
    config_path,
    tag,
    port,
    user_name,
    domain,
    cert_path,
    key_path,
    replace_users,
    reduce_logs,
) = sys.argv[1:]
password = os.fdopen(3).read().rstrip("\n")

path = Path(config_path).resolve(strict=True)
data = json.loads(path.read_text(encoding="utf-8"))
if reduce_logs == "true":
    log = data.setdefault("log", {})
    if not isinstance(log, dict):
        raise SystemExit("log must be a JSON object")
    log["level"] = "warn"
inbounds = data.setdefault("inbounds", [])

existing = next((item for item in inbounds if item.get("tag") == tag), None)
if existing is not None and existing.get("type") != "hysteria2":
    raise SystemExit(f"Inbound tag {tag!r} already exists with a different type")

users = [] if existing is None or replace_users == "true" else list(existing.get("users", []))
users = [item for item in users if item.get("name") != user_name]
users.append({"name": user_name, "password": password})

managed = {} if existing is None else dict(existing)
managed.update(
    {
        "type": "hysteria2",
        "tag": tag,
        "listen": "0.0.0.0",
        "listen_port": int(port),
        "users": users,
        "ignore_client_bandwidth": True,
        "tls": {
            "enabled": True,
            "server_name": domain,
            "certificate_path": cert_path,
            "key_path": key_path,
        },
    }
)
managed.pop("up_mbps", None)
managed.pop("down_mbps", None)

if existing is None:
    inbounds.append(managed)
else:
    existing.clear()
    existing.update(managed)

route = data.setdefault("route", {})
rules = route.setdefault("rules", [])
if not isinstance(rules, list):
    raise SystemExit("route.rules must be a JSON array")

private_reject = {
    "inbound": [tag],
    "ip_is_private": True,
    "action": "reject",
}
metadata_reject = {
    "inbound": [tag],
    "domain": ["metadata.google.internal"],
    "action": "reject",
}

def same_managed_rule(rule, kind):
    if rule.get("inbound") != [tag] or rule.get("action") != "reject":
        return False
    if kind == "private":
        return rule.get("ip_is_private") is True
    return rule.get("domain") == ["metadata.google.internal"]

rules[:] = [
    rule
    for rule in rules
    if not same_managed_rule(rule, "private")
    and not same_managed_rule(rule, "metadata")
]
rules[0:0] = [metadata_reject, private_reject]

metadata = path.stat()
fd, temporary = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(data, handle, indent=2, ensure_ascii=False)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, stat.S_IMODE(metadata.st_mode))
    os.chown(temporary, metadata.st_uid, metadata.st_gid)
    os.replace(temporary, path)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY

if ! "$SING_BOX_BIN" check -c "$CONFIG_PATH"; then
  rollback "sing-box check failed."
fi

if ! systemctl restart "$SERVICE_NAME"; then
  rollback "Failed to restart ${SERVICE_NAME}."
fi

if ! systemctl is-active --quiet "$SERVICE_NAME"; then
  rollback "${SERVICE_NAME} did not become active."
fi

listener_ready=false
for ((attempt = 1; attempt <= 20; attempt++)); do
  if ss -H -lunp 2>/dev/null | awk -v p=":${HY2_PORT}" '$4 ~ p"$" {found=1} END {exit !found}'; then
    listener_ready=true
    break
  fi
  sleep 0.25
done

if [[ "$listener_ready" != "true" ]]; then
  rollback "${SERVICE_NAME} is active but UDP ${HY2_PORT} is not listening."
fi

python3 - "$HY2_SERVER_ADDRESS" "$HY2_DOMAIN" "$HY2_PORT" "$CREDENTIAL_PATH" 3<<<"$password" <<'PY'
import json
import os
import sys
import tempfile
from pathlib import Path

server_address, domain, port, credential_path = sys.argv[1:]
password = os.fdopen(3).read().rstrip("\n")
credential = {
    "type": "hysteria2",
    "server": server_address,
    "server_port": int(port),
    "password": password,
    "network": "tcp",
    "tls": {
        "enabled": True,
        "server_name": domain,
        "insecure": False,
    },
}
target = Path(credential_path)
fd, temporary = tempfile.mkstemp(prefix=target.name + ".", dir=target.parent)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(credential, handle, indent=2, ensure_ascii=False)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, 0o600)
    os.replace(temporary, target)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY

trap - ERR

manager_source="$(dirname "$(readlink -f "$0")")/manage-hysteria2-users.sh"
manager_install_path="/usr/local/sbin/sing-box-hy2-user"
manager_note="User manager was not installed; upload manage-hysteria2-users.sh next to this script and rerun."
if [[ -f "$manager_source" ]]; then
  install -o root -g root -m 0750 "$manager_source" "$manager_install_path"
  manager_note="User manager installed: $manager_install_path"
fi

cat <<EOF
Hysteria 2 inbound is ready in sing-box.
Config: $CONFIG_PATH
Backup: $BACKUP_PATH
Tag: $HY2_TAG
UDP port: $HY2_PORT
TLS server name: $HY2_DOMAIN
Client server address: $HY2_SERVER_ADDRESS
User: $HY2_USER_NAME
Client credential file (mode 0600): $CREDENTIAL_PATH
$manager_note

The password was not printed. Read the credential locally when needed:
  sudo cat $CREDENTIAL_PATH

Future users can be managed with:
  sudo sing-box-hy2-user add friend-2
  sudo sing-box-hy2-user list

Rollback:
  sudo cp $BACKUP_PATH $CONFIG_PATH
  sudo $SING_BOX_BIN check -c $CONFIG_PATH
  sudo systemctl restart $SERVICE_NAME
EOF
