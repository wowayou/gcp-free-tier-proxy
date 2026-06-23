#!/usr/bin/env bash
set -euo pipefail

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

find_python() {
  local candidate
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c 'import json' >/dev/null 2>&1; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

PYTHON_BIN="$(find_python || true)"

if [[ -z "$PYTHON_BIN" ]]; then
  echo "python3 or python is required." >&2
  exit 1
fi

CONFIG_PATH="${CONFIG_PATH:-$(find_config || true)}"

if [[ -z "$CONFIG_PATH" || ! -f "$CONFIG_PATH" ]]; then
  echo "No known sing-box config file found. Set CONFIG_PATH explicitly." >&2
  exit 1
fi

cat >&2 <<'EOF'
This command prints client credentials as vless:// share links.
Run it only in a private terminal. Do not paste the output into chat, issues, or commits.

For Reality, sing-box server config usually stores the private key, not the client public key.
If the Reality link is skipped, rerun with REALITY_PUBLIC_KEY set from the original deploy output.
EOF

"$PYTHON_BIN" - "$CONFIG_PATH" <<'PY'
import base64
import json
import os
import sys
import urllib.parse
import urllib.request
from pathlib import Path


def metadata_external_ip():
    if os.environ.get("DISABLE_GCP_METADATA") == "1":
        return ""
    request = urllib.request.Request(
        "http://metadata.google.internal/computeMetadata/v1/instance/"
        "network-interfaces/0/access-configs/0/external-ip",
        headers={"Metadata-Flavor": "Google"},
    )
    try:
        with urllib.request.urlopen(request, timeout=1) as response:
            return response.read().decode("utf-8").strip()
    except Exception:
        return ""


def host_for_url(host):
    if ":" in host and not (host.startswith("[") and host.endswith("]")):
        return f"[{host}]"
    return host


def first_uuid(inbound):
    users = inbound.get("users") or []
    if not users:
        return ""
    return users[0].get("uuid", "")


def first_flow(inbound, default=""):
    users = inbound.get("users") or []
    if users and users[0].get("flow"):
        return users[0]["flow"]
    return inbound.get("flow", default)


def first_short_id(reality):
    short_id = reality.get("short_id") or reality.get("short_id_hex") or ""
    if isinstance(short_id, list):
        for item in short_id:
            if item:
                return str(item)
        return ""
    return str(short_id)


def vless_url(uuid, address, port, params, name):
    query = urllib.parse.urlencode(params)
    fragment = urllib.parse.quote(name, safe="")
    return f"vless://{uuid}@{host_for_url(address)}:{port}?{query}#{fragment}"


def tls_server_name(inbound):
    tls = inbound.get("tls") or {}
    return tls.get("server_name", "")


config_path = Path(sys.argv[1])
data = json.loads(config_path.read_text(encoding="utf-8-sig"))
inbounds = data.get("inbounds") or []

metadata_ip = metadata_external_ip()
server_address = os.environ.get("SERVER_ADDRESS", "").strip() or metadata_ip
reality_address = os.environ.get("REALITY_ADDRESS", "").strip() or server_address
tls_address_env = os.environ.get("TLS_ADDRESS", "").strip()
reality_public_key = os.environ.get("REALITY_PUBLIC_KEY", "").strip()

links = []
notes = []

for inbound in inbounds:
    if inbound.get("type") != "vless":
        continue

    uuid = first_uuid(inbound)
    port = inbound.get("listen_port")
    if not uuid or not port:
        continue

    tag = inbound.get("tag") or "vless"
    tls = inbound.get("tls") or {}
    reality = tls.get("reality") or {}

    if reality.get("enabled"):
        public_key = reality_public_key or reality.get("public_key", "")
        if not public_key:
            notes.append(
                f"Skipped Reality inbound '{tag}': set REALITY_PUBLIC_KEY to export its client link."
            )
            continue
        if not reality_address:
            notes.append(
                f"Skipped Reality inbound '{tag}': set REALITY_ADDRESS or SERVER_ADDRESS."
            )
            continue

        handshake = reality.get("handshake") or {}
        sni = (
            os.environ.get("REALITY_SNI", "").strip()
            or tls.get("server_name", "")
            or handshake.get("server", "")
            or "apple.com"
        )
        params = {
            "encryption": "none",
            "flow": first_flow(inbound, "xtls-rprx-vision"),
            "security": "reality",
            "sni": sni,
            "fp": os.environ.get("REALITY_FINGERPRINT", "chrome"),
            "pbk": public_key,
            "sid": first_short_id(reality),
            "type": "tcp",
        }
        links.append((tag, vless_url(uuid, reality_address, port, params, tag)))
        continue

    if tls.get("enabled"):
        sni = tls_server_name(inbound)
        address = tls_address_env or sni or server_address
        if not address:
            notes.append(f"Skipped TLS inbound '{tag}': set TLS_ADDRESS or SERVER_ADDRESS.")
            continue
        params = {
            "encryption": "none",
            "security": "tls",
            "sni": sni or address,
            "fp": os.environ.get("TLS_FINGERPRINT", "chrome"),
            "type": "tcp",
        }
        links.append((tag, vless_url(uuid, address, port, params, tag)))

if notes:
    print("\nNotes:", file=sys.stderr)
    for note in notes:
        print(f"- {note}", file=sys.stderr)

if not links:
    print("No exportable VLESS links found.", file=sys.stderr)
    sys.exit(1)

print("\nVLESS share links:")
for tag, link in links:
    print(f"{tag}:")
    print(link)

subscription_text = "\n".join(link for _, link in links)
subscription_base64 = base64.b64encode(subscription_text.encode("utf-8")).decode("ascii")

print("\nBase64 subscription content:")
print(subscription_base64)
print("\nThis is subscription content, not a hosted subscription URL.")
PY
