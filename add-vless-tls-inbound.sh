#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo bash add-vless-tls-inbound.sh" >&2
  exit 1
fi

SERVICE_NAME="${SERVICE_NAME:-sing-box}"
TLS_PORT="${TLS_PORT:-443}"
FALLBACK_TLS_PORT="${FALLBACK_TLS_PORT:-8443}"
TLS_TAG="${TLS_TAG:-vless-tls}"
TLS_USER_NAME="${TLS_USER_NAME:-vless-tls}"
CERT_DIR="${CERT_DIR:-/root/ygkkkca}"
CERT_PATH="${CERT_PATH:-${CERT_DIR}/fullchain.cer}"
KEY_PATH="${KEY_PATH:-${CERT_DIR}/private.key}"

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

port_owner() {
  local port="$1"
  ss -lntp 2>/dev/null | awk -v p=":${port}" '$4 ~ p"$" {print; found=1} END {exit !found}'
}

port_is_available_or_owned_by_service() {
  local port="$1"
  local owner_file="$2"

  if port_owner "$port" >"$owner_file" 2>/dev/null; then
    if grep -q "$SERVICE_NAME" "$owner_file"; then
      return 0
    fi

    return 1
  fi

  return 0
}

pick_tls_port() {
  local owner_file="/tmp/vless-tls-port-owner"
  local fallback_owner_file="/tmp/vless-tls-fallback-port-owner"

  if port_is_available_or_owned_by_service "$TLS_PORT" "$owner_file"; then
    echo "$TLS_PORT"
    return
  fi

  echo "Port ${TLS_PORT} is already in use by:" >&2
  cat "$owner_file" >&2
  echo "Checking fallback port ${FALLBACK_TLS_PORT}." >&2

  if port_is_available_or_owned_by_service "$FALLBACK_TLS_PORT" "$fallback_owner_file"; then
    echo "$FALLBACK_TLS_PORT"
    return
  fi

  echo "Fallback port ${FALLBACK_TLS_PORT} is also in use by:" >&2
  cat "$fallback_owner_file" >&2
  echo "Set TLS_PORT or FALLBACK_TLS_PORT to an available port." >&2
  exit 1
}

detect_domain() {
  if [[ -n "${TLS_DOMAIN:-}" ]]; then
    echo "$TLS_DOMAIN"
    return
  fi

  local san
  san="$(
    openssl x509 -in "$CERT_PATH" -noout -ext subjectAltName 2>/dev/null |
      tr ',' '\n' |
      sed -n 's/.*DNS:\([^ ]*\).*/\1/p' |
      grep -v '^\*\.' |
      head -n1 || true
  )"
  if [[ -n "$san" ]]; then
    echo "$san"
    return
  fi

  san="$(
    openssl x509 -in "$CERT_PATH" -noout -ext subjectAltName 2>/dev/null |
      tr ',' '\n' |
      sed -n 's/.*DNS:\*\.\([^ ]*\).*/\1/p' |
      head -n1 || true
  )"
  if [[ -n "$san" ]]; then
    echo "$san"
    return
  fi

  openssl x509 -in "$CERT_PATH" -noout -subject |
    sed -n 's/.*CN *= *\([^,/]*\).*/\1/p'
}

generate_uuid() {
  if command -v sing-box >/dev/null 2>&1; then
    sing-box generate uuid
  elif command -v uuidgen >/dev/null 2>&1; then
    uuidgen | tr '[:upper:]' '[:lower:]'
  else
    python3 - <<'PY'
import uuid
print(uuid.uuid4())
PY
  fi
}

require_cmd openssl
require_cmd python3
require_cmd ss

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

CONFIG_PATH="$(find_config)"
TLS_DOMAIN="$(detect_domain)"
TLS_PORT="$(pick_tls_port)"
TLS_UUID="${TLS_UUID:-$(generate_uuid)}"
BACKUP_PATH="${CONFIG_PATH}.bak.$(date -u +%Y%m%d-%H%M%S)"

if [[ -z "$TLS_DOMAIN" ]]; then
  echo "Cannot detect TLS_DOMAIN from certificate. Set TLS_DOMAIN explicitly." >&2
  exit 1
fi

cp "$CONFIG_PATH" "$BACKUP_PATH"

python3 - "$CONFIG_PATH" "$TLS_TAG" "$TLS_PORT" "$TLS_USER_NAME" "$TLS_UUID" "$TLS_DOMAIN" "$CERT_PATH" "$KEY_PATH" <<'PY'
import json
import sys
from pathlib import Path

config_path, tag, port, user_name, uuid, domain, cert_path, key_path = sys.argv[1:]
path = Path(config_path)
data = json.loads(path.read_text())
inbounds = data.setdefault("inbounds", [])

existing = None
for inbound in inbounds:
    if inbound.get("tag") == tag:
        existing = inbound
        break

new_inbound = {
    "type": "vless",
    "tag": tag,
    "listen": "0.0.0.0",
    "listen_port": int(port),
    "users": [
        {
            "name": user_name,
            "uuid": uuid,
        }
    ],
    "tls": {
        "enabled": True,
        "server_name": domain,
        "certificate_path": cert_path,
        "key_path": key_path,
    },
}

if existing is None:
    inbounds.append(new_inbound)
else:
    existing.clear()
    existing.update(new_inbound)

path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
PY

if command -v sing-box >/dev/null 2>&1; then
  if ! sing-box check -c "$CONFIG_PATH"; then
    cp "$BACKUP_PATH" "$CONFIG_PATH"
    echo "sing-box check failed. Restored backup: $BACKUP_PATH" >&2
    exit 1
  fi
fi

systemctl restart "$SERVICE_NAME"

cat <<EOF
VLESS TLS inbound is ready.
Config: $CONFIG_PATH
Backup: $BACKUP_PATH
Tag: $TLS_TAG
Address: use the gray-cloud DNS name on the certificate
Port: $TLS_PORT
UUID: $TLS_UUID
TLS server name: $TLS_DOMAIN
Certificate: $CERT_PATH
Key: $KEY_PATH

v2rayN fields:
  Protocol: vless
  Address: $TLS_DOMAIN
  Port: $TLS_PORT
  UUID: $TLS_UUID
  Encryption: none
  Transport: tcp
  TLS: tls
  SNI: $TLS_DOMAIN
  Reality/PublicKey/ShortId: empty
  Mux: disabled
EOF
