#!/usr/bin/env bash
set -euo pipefail

PROGRAM_NAME="sing-box-hy2-user"
SERVICE_NAME="${SERVICE_NAME:-sing-box}"
SING_BOX_BIN="${SING_BOX_BIN:-}"
CONFIG_PATH="${CONFIG_PATH:-}"
HY2_TAG="${HY2_TAG:-}"
HY2_SERVER_ADDRESS="${HY2_SERVER_ADDRESS:-}"
CREDENTIAL_DIR="${CREDENTIAL_DIR:-/root/hysteria2-credentials}"
INSTALL_PATH="${INSTALL_PATH:-/usr/local/sbin/${PROGRAM_NAME}}"
LOCK_PATH="${LOCK_PATH:-/run/lock/${PROGRAM_NAME}.lock}"

CONFIG_CANDIDATES=(
  "/etc/s-box/sb.json"
  "/etc/sing-box/config.json"
  "/usr/local/etc/sing-box/config.json"
)

usage() {
  cat <<EOF
Manage users on an existing sing-box Hysteria 2 inbound.

Usage:
  sudo ${PROGRAM_NAME} add USER
  sudo ${PROGRAM_NAME} list
  sudo ${PROGRAM_NAME} export USER
  sudo ${PROGRAM_NAME} remove USER [--yes]
  sudo bash manage-hysteria2-users.sh install

Commands:
  add      Add USER with a new strong password, or re-export an existing user.
  list     List user names only; passwords are never printed.
  export   Rebuild USER's root-only client JSON and import-link files.
  remove   Revoke USER. The last remaining user cannot be removed.
  install  Install this script as ${INSTALL_PATH}.

Set HY2_TAG only when the config contains more than one Hysteria 2 inbound.
EOF
}

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

require_root() {
  [[ "${EUID}" -eq 0 ]] || fail "Run this command with sudo."
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "Required command not found: $1"
}

acquire_lock() {
  require_cmd flock
  exec 9>"$LOCK_PATH"
  flock -n 9 || fail "Another ${PROGRAM_NAME} operation is already running."
}

validate_user_name() {
  local user_name="$1"
  [[ "$user_name" =~ ^[A-Za-z0-9._-]{1,64}$ ]] ||
    fail "USER must match [A-Za-z0-9._-] and be at most 64 characters."
}

find_config() {
  local path
  if [[ -n "$CONFIG_PATH" ]]; then
    [[ -f "$CONFIG_PATH" ]] || fail "Config file not found: $CONFIG_PATH"
    return
  fi
  for path in "${CONFIG_CANDIDATES[@]}"; do
    if [[ -f "$path" ]]; then
      CONFIG_PATH="$path"
      return
    fi
  done
  fail "No known sing-box config file found. Set CONFIG_PATH explicitly."
}

find_sing_box() {
  if [[ -n "$SING_BOX_BIN" ]]; then
    [[ -x "$SING_BOX_BIN" ]] || fail "sing-box is not executable: $SING_BOX_BIN"
  elif command -v sing-box >/dev/null 2>&1; then
    SING_BOX_BIN="$(command -v sing-box)"
  elif [[ -x /etc/s-box/sing-box ]]; then
    SING_BOX_BIN="/etc/s-box/sing-box"
  else
    fail "Cannot find sing-box. Set SING_BOX_BIN explicitly."
  fi
}

discover_inbound() {
  local -a fields=()
  mapfile -t fields < <(python3 - "$CONFIG_PATH" "$HY2_TAG" <<'PY'
import json
import sys

config_path, requested_tag = sys.argv[1:]
with open(config_path, encoding="utf-8-sig") as handle:
    data = json.load(handle)

inbounds = [item for item in data.get("inbounds", []) if item.get("type") == "hysteria2"]
if requested_tag:
    inbounds = [item for item in inbounds if item.get("tag") == requested_tag]
if not inbounds:
    raise SystemExit("No matching Hysteria 2 inbound was found")
if len(inbounds) != 1:
    tags = ", ".join(str(item.get("tag", "<untagged>")) for item in inbounds)
    raise SystemExit(f"Multiple Hysteria 2 inbounds found ({tags}); set HY2_TAG")

inbound = inbounds[0]
tls = inbound.get("tls") or {}
values = [
    inbound.get("tag", ""),
    inbound.get("listen_port", ""),
    tls.get("certificate_path", ""),
    tls.get("key_path", ""),
    tls.get("server_name", ""),
]
if any(value == "" for value in values):
    raise SystemExit("The Hysteria 2 inbound is missing tag, port, certificate, key, or TLS server_name")
for value in values:
    print(value)
PY
  )

  [[ "${#fields[@]}" -eq 5 ]] || fail "Could not read the existing Hysteria 2 inbound."
  HY2_TAG="${fields[0]}"
  HY2_PORT="${fields[1]}"
  CERT_PATH="${fields[2]}"
  KEY_PATH="${fields[3]}"
  HY2_DOMAIN="${fields[4]}"
  [[ "$HY2_PORT" =~ ^[0-9]+$ ]] || fail "Invalid Hysteria 2 port in config."
  [[ -f "$CERT_PATH" ]] || fail "Certificate file not found: $CERT_PATH"
  [[ -f "$KEY_PATH" ]] || fail "Private key file not found: $KEY_PATH"
  if [[ -z "$HY2_SERVER_ADDRESS" ]]; then
    HY2_SERVER_ADDRESS="$HY2_DOMAIN"
  fi
}

read_password() {
  local user_name="$1"
  python3 - "$CONFIG_PATH" "$HY2_TAG" "$user_name" <<'PY'
import json
import sys

config_path, tag, user_name = sys.argv[1:]
with open(config_path, encoding="utf-8-sig") as handle:
    data = json.load(handle)
for inbound in data.get("inbounds", []):
    if inbound.get("type") != "hysteria2" or inbound.get("tag") != tag:
        continue
    for user in inbound.get("users", []):
        if user.get("name") == user_name and user.get("password"):
            print(user["password"], end="")
            raise SystemExit(0)
raise SystemExit(3)
PY
}

write_client_files() {
  local user_name="$1"
  local password="$2"
  local credential_path="${CREDENTIAL_DIR}/${user_name}.json"
  local import_path="${CREDENTIAL_DIR}/${user_name}.txt"

  install -d -o root -g root -m 0700 "$CREDENTIAL_DIR"
  python3 - \
    "$HY2_SERVER_ADDRESS" "$HY2_DOMAIN" "$HY2_PORT" "$HY2_TAG" "$user_name" \
    "$credential_path" "$import_path" 3<<<"$password" <<'PY'
import json
import os
import sys
import tempfile
import urllib.parse
from pathlib import Path

server, domain, port, tag, user_name, credential_path, import_path = sys.argv[1:]
password = os.fdopen(3).read().rstrip("\n")

credential = {
    "type": "hysteria2",
    "tag": f"{tag}-{user_name}",
    "server": server,
    "server_port": int(port),
    "password": password,
    "network": "tcp",
    "tls": {
        "enabled": True,
        "server_name": domain,
        "insecure": False,
    },
}

host = f"[{server}]" if ":" in server and not server.startswith("[") else server
auth = urllib.parse.quote(password, safe="")
sni = urllib.parse.quote(domain, safe="")
fragment = urllib.parse.quote(f"{tag}-{user_name}", safe="")
share_link = f"hysteria2://{auth}@{host}:{port}/?sni={sni}&insecure=0#{fragment}\n"

def atomic_write(path_value, content):
    target = Path(path_value)
    fd, temporary = tempfile.mkstemp(prefix=target.name + ".", dir=target.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)

atomic_write(credential_path, json.dumps(credential, indent=2, ensure_ascii=False) + "\n")
atomic_write(import_path, share_link)
PY

  echo "Client JSON (root-only): $credential_path"
  echo "Import link (root-only): $import_path"
  echo "The password and link were not printed."
}

list_users() {
  python3 - "$CONFIG_PATH" "$HY2_TAG" <<'PY'
import json
import sys

config_path, tag = sys.argv[1:]
with open(config_path, encoding="utf-8-sig") as handle:
    data = json.load(handle)
for inbound in data.get("inbounds", []):
    if inbound.get("type") == "hysteria2" and inbound.get("tag") == tag:
        users = inbound.get("users") or []
        if not users:
            print("(no users)")
        for user in users:
            print(user.get("name", "<unnamed>"))
        raise SystemExit(0)
raise SystemExit("Hysteria 2 inbound disappeared during the operation")
PY
}

backup_config() {
  BACKUP_PATH="${CONFIG_PATH}.bak.$(date -u +%Y%m%d-%H%M%S).$$"
  cp --preserve=mode,ownership "$CONFIG_PATH" "$BACKUP_PATH"
}

rollback() {
  local reason="$1"
  trap - ERR
  echo "$reason" >&2
  cp --preserve=mode,ownership "$BACKUP_PATH" "$CONFIG_PATH"
  if "$SING_BOX_BIN" check -c "$CONFIG_PATH" >/dev/null 2>&1; then
    systemctl restart "$SERVICE_NAME" || true
  fi
  echo "Restored backup: $BACKUP_PATH" >&2
  exit 1
}

verify_and_restart() {
  "$SING_BOX_BIN" check -c "$CONFIG_PATH" || rollback "sing-box check failed."
  systemctl restart "$SERVICE_NAME" || rollback "Failed to restart ${SERVICE_NAME}."
  systemctl is-active --quiet "$SERVICE_NAME" || rollback "${SERVICE_NAME} is not active."

  local listener_ready=false
  local attempt
  for ((attempt = 1; attempt <= 20; attempt++)); do
    if ss -H -lunp 2>/dev/null | awk -v p=":${HY2_PORT}" '$4 ~ p"$" {found=1} END {exit !found}'; then
      listener_ready=true
      break
    fi
    sleep 0.25
  done
  [[ "$listener_ready" == "true" ]] || rollback "UDP ${HY2_PORT} is not listening."
}

add_user() {
  local user_name="$1"
  local password=""
  validate_user_name "$user_name"

  if password="$(read_password "$user_name")"; then
    echo "User already exists; credentials will be re-exported without changing the password: $user_name"
    write_client_files "$user_name" "$password"
    return
  fi

  password="$(openssl rand -hex 32)"
  backup_config
  trap 'rollback "Unexpected failure while adding the user."' ERR
  python3 - "$CONFIG_PATH" "$HY2_TAG" "$user_name" 3<<<"$password" <<'PY'
import json
import os
import stat
import sys
import tempfile
from pathlib import Path

config_path, tag, user_name = sys.argv[1:]
password = os.fdopen(3).read().rstrip("\n")
path = Path(config_path).resolve(strict=True)
data = json.loads(path.read_text(encoding="utf-8-sig"))
inbound = next(
    (item for item in data.get("inbounds", []) if item.get("type") == "hysteria2" and item.get("tag") == tag),
    None,
)
if inbound is None:
    raise SystemExit("Hysteria 2 inbound not found")
users = list(inbound.get("users") or [])
if any(user.get("name") == user_name for user in users):
    raise SystemExit("User appeared concurrently; retry the command")
users.append({"name": user_name, "password": password})
inbound["users"] = users

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
  verify_and_restart
  write_client_files "$user_name" "$password"
  trap - ERR
  echo "Added Hysteria 2 user: $user_name"
  echo "Backup: $BACKUP_PATH"
}

export_user() {
  local user_name="$1"
  local password=""
  validate_user_name "$user_name"
  password="$(read_password "$user_name")" || fail "User not found: $user_name"
  write_client_files "$user_name" "$password"
}

remove_user() {
  local user_name="$1"
  local assume_yes="$2"
  validate_user_name "$user_name"
  read_password "$user_name" >/dev/null || fail "User not found: $user_name"

  if [[ "$assume_yes" != "true" ]]; then
    read -r -p "Revoke Hysteria 2 user '${user_name}'? [y/N] " answer
    [[ "$answer" == "y" || "$answer" == "Y" ]] || {
      echo "Cancelled."
      return
    }
  fi

  backup_config
  trap 'rollback "Unexpected failure while removing the user."' ERR
  python3 - "$CONFIG_PATH" "$HY2_TAG" "$user_name" <<'PY'
import json
import os
import stat
import sys
import tempfile
from pathlib import Path

config_path, tag, user_name = sys.argv[1:]
path = Path(config_path).resolve(strict=True)
data = json.loads(path.read_text(encoding="utf-8-sig"))
inbound = next(
    (item for item in data.get("inbounds", []) if item.get("type") == "hysteria2" and item.get("tag") == tag),
    None,
)
if inbound is None:
    raise SystemExit("Hysteria 2 inbound not found")
users = list(inbound.get("users") or [])
remaining = [user for user in users if user.get("name") != user_name]
if len(remaining) == len(users):
    raise SystemExit("User not found")
if not remaining:
    raise SystemExit("Refusing to remove the last Hysteria 2 user")
inbound["users"] = remaining

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
  verify_and_restart
  trap - ERR
  rm -f -- "${CREDENTIAL_DIR}/${user_name}.json" "${CREDENTIAL_DIR}/${user_name}.txt"
  echo "Removed Hysteria 2 user: $user_name"
  echo "Backup: $BACKUP_PATH"
}

install_manager() {
  local source_path
  source_path="$(readlink -f "$0")"
  install -o root -g root -m 0750 "$source_path" "$INSTALL_PATH"
  echo "Installed: $INSTALL_PATH"
  echo "Next command: sudo ${PROGRAM_NAME} list"
}

main() {
  local command="${1:-help}"
  if [[ "$command" == "help" || "$command" == "-h" || "$command" == "--help" ]]; then
    usage
    return
  fi

  require_root
  [[ ! -e /etc/hy2-account/config.json && ! -e /var/lib/hy2-account/migrated && ! -e /usr/local/lib/hy2-account/hy2-account ]] ||
    fail "Official HY2 account backend is installed. Legacy sing-box user operations are blocked; use sudo hy2-admin."
  case "$command" in
    install)
      require_cmd install
      require_cmd readlink
      install_manager
      return
      ;;
    add|export)
      [[ -n "${2:-}" ]] || fail "${command} requires USER."
      [[ "$#" -eq 2 ]] || fail "${command} accepts USER only."
      validate_user_name "$2"
      ;;
    remove)
      [[ -n "${2:-}" ]] || fail "remove requires USER."
      [[ "$#" -le 3 ]] || fail "remove accepts only USER and optional --yes."
      [[ "$#" -lt 3 || "$3" == "--yes" ]] ||
        fail "Unknown remove option: $3"
      validate_user_name "$2"
      ;;
    list)
      [[ "$#" -eq 1 ]] || fail "list does not accept additional arguments."
      ;;
    *)
      usage >&2
      fail "Unknown command: $command"
      ;;
  esac

  require_cmd python3
  require_cmd openssl
  require_cmd systemctl
  require_cmd ss
  acquire_lock
  find_config
  find_sing_box
  discover_inbound

  case "$command" in
    add)
      systemctl is-active --quiet gcp-egress-guard.timer ||
        fail "gcp-egress-guard.timer must be active before adding users."
      add_user "$2"
      ;;
    list)
      list_users
      ;;
    export)
      export_user "$2"
      ;;
    remove)
      local assume_yes=false
      [[ "${3:-}" == "--yes" ]] && assume_yes=true
      remove_user "$2" "$assume_yes"
      ;;
  esac
}

main "$@"
