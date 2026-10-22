#!/usr/bin/env bash
set -uo pipefail

SERVICE_NAME="${SERVICE_NAME:-sing-box}"
SING_BOX_BIN="${SING_BOX_BIN:-}"
PROXY_PORT="${PROXY_PORT:-47370}"
TLS_PORT="${TLS_PORT:-443}"
HY2_PORT="${HY2_PORT:-443}"
EGRESS_TIMER="${EGRESS_TIMER:-gcp-egress-guard.timer}"
EGRESS_STATUS="${EGRESS_STATUS:-/run/gcp-egress-guard/status.json}"
HY2_ACCOUNT_CONFIG="${HY2_ACCOUNT_CONFIG:-/etc/hy2-account/config.json}"
HY2_ACCOUNT_UNIT="${HY2_ACCOUNT_UNIT:-hy2-accountd.service}"
HY2_SERVER_UNIT="${HY2_SERVER_UNIT:-hysteria-server.service}"
HY2_HEALTH_TIMER="${HY2_HEALTH_TIMER:-hy2-health.timer}"
HY2_CERT_PATH="${HY2_CERT_PATH:-/etc/hysteria/certs/current/fullchain.cer}"
HY2_ADMIN_BIN="${HY2_ADMIN_BIN:-hy2-admin}"
CERT_DIR="${CERT_DIR:-/root/ygkkkca}"
CERT_PATH="${CERT_PATH:-${CERT_DIR}/fullchain.cer}"
KEY_PATH="${KEY_PATH:-${CERT_DIR}/private.key}"

CONFIG_CANDIDATES=(
  "/etc/s-box/sb.json"
  "/etc/sing-box/config.json"
  "/usr/local/etc/sing-box/config.json"
)

section() {
  printf '\n==== %s ====\n' "$1"
}

ok() {
  echo "[OK] $1"
}

warn() {
  echo "[WARN] $1"
}

fail() {
  echo "[FAIL] $1"
}

check_listener() {
  local port="$1"
  local label="$2"
  local required="${3:-true}"

  if ss -lntp 2>/dev/null | grep -q ":${port} "; then
    ok "${label} tcp:${port} is listening"
    ss -lntp 2>/dev/null | grep ":${port} " || true
  else
    if [[ "$required" == "true" ]]; then
      fail "${label} tcp:${port} is not listening"
    else
      warn "${label} tcp:${port} is not listening"
    fi
  fi
}

check_udp_listener() {
  local port="$1"
  local label="$2"

  if ss -lunp 2>/dev/null | grep -q ":${port} "; then
    ok "${label} udp:${port} is listening"
    ss -lunp 2>/dev/null | grep ":${port} " || true
  else
    warn "${label} udp:${port} is not listening"
  fi
}

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

section "Service"
if command -v systemctl >/dev/null 2>&1; then
  if systemctl is-active --quiet "$SERVICE_NAME"; then
    ok "${SERVICE_NAME} is active"
  else
    fail "${SERVICE_NAME} is not active"
  fi
else
  warn "systemctl is not available"
fi

if [[ -z "$SING_BOX_BIN" ]]; then
  if command -v sing-box >/dev/null 2>&1; then
    SING_BOX_BIN="$(command -v sing-box)"
  elif [[ -x /etc/s-box/sing-box ]]; then
    SING_BOX_BIN="/etc/s-box/sing-box"
  fi
fi

if [[ -n "$SING_BOX_BIN" ]]; then
  "$SING_BOX_BIN" version | sed -n '1,2p'
else
  warn "sing-box executable not found"
fi

section "Listener"
if command -v ss >/dev/null 2>&1; then
  check_listener "$PROXY_PORT" "Reality"
  check_listener "$TLS_PORT" "VLESS TLS" false
  check_udp_listener "$HY2_PORT" "Hysteria 2"
else
  warn "ss is not available"
fi

section "Runtime config"
config_path="$(find_config || true)"
if [[ -n "${config_path:-}" ]]; then
  ok "found config: ${config_path}"

  grep -q '"reality"' "$config_path" && ok "reality config is present" || fail "reality config not found"
  grep -q 'xtls-rprx-vision' "$config_path" && ok "vision flow is present" || warn "vision flow not found"
  grep -q 'apple.com' "$config_path" && ok "apple.com SNI/handshake is present" || warn "apple.com not found"
  grep -q "\"${PROXY_PORT}\"\\|:${PROXY_PORT}\\|${PROXY_PORT}" "$config_path" && ok "port ${PROXY_PORT} is present" || warn "port ${PROXY_PORT} not found"
  grep -q 'private_key' "$config_path" && ok "reality private_key field exists" || warn "reality private_key field not found"
  grep -q 'short_id' "$config_path" && ok "reality short_id field exists" || warn "reality short_id field not found"
  grep -q 'vless-tls' "$config_path" && ok "vless-tls inbound tag is present" || warn "vless-tls inbound tag not found"
  grep -q '"type"[[:space:]]*:[[:space:]]*"hysteria2"' "$config_path" && ok "hysteria2 inbound is present" || warn "hysteria2 inbound not found"
  grep -q "\"${TLS_PORT}\"\\|:${TLS_PORT}\\|${TLS_PORT}" "$config_path" && ok "TLS port ${TLS_PORT} is present" || warn "TLS port ${TLS_PORT} not found"
  grep -q 'certificate_path' "$config_path" && ok "TLS certificate_path field exists" || warn "TLS certificate_path field not found"
  grep -q 'key_path' "$config_path" && ok "TLS key_path field exists" || warn "TLS key_path field not found"
  if [[ -n "$SING_BOX_BIN" ]] && "$SING_BOX_BIN" check -c "$config_path" >/dev/null; then
    ok "sing-box check passed"
  else
    fail "sing-box check failed or executable is unavailable"
  fi
else
  fail "no known sing-box config file found"
fi

section "Egress guard"
if command -v systemctl >/dev/null 2>&1; then
  if systemctl is-active --quiet "$EGRESS_TIMER"; then
    ok "${EGRESS_TIMER} is active"
  else
    warn "${EGRESS_TIMER} is not active"
  fi

  if [[ -f /var/lib/gcp-egress-guard/state ]]; then
    ok "egress guard state exists"
    sed -n '1,20p' /var/lib/gcp-egress-guard/state
  else
    warn "egress guard state file not found"
  fi
  if [[ -f "$EGRESS_STATUS" ]] && command -v python3 >/dev/null 2>&1; then
    python3 - "$EGRESS_STATUS" <<'PY' || warn "egress guard status is unreadable/invalid"
import json, sys, time
with open(sys.argv[1], encoding="utf-8") as handle:
    status = json.load(handle)
services = {str(item).removesuffix(".service") for item in status.get("services", [])}
missing = {"sing-box", "hysteria-server"} - services
print("[OK] guard covers both proxies" if not missing else "[WARN] guard is missing: " + ", ".join(sorted(missing)))
print("[OK] guard is not blocked" if not status.get("blocked") else "[FAIL] guard is blocked; do not bypass with user-level resume")
print("[OK] guard status is fresh" if int(time.time()) - int(status.get("updated", 0)) <= 180 else "[FAIL] guard status is stale")
print(f"guard used/limit: {int(status.get('used_bytes', 0)) / 2**30:.3f} / {int(status.get('limit_bytes', 0)) / 2**30:.3f} GiB")
PY
  else
    warn "egress guard status missing; account authentication should stay closed"
  fi
fi

section "HY2 account backend"
if [[ -f "$HY2_ACCOUNT_CONFIG" ]]; then
  ok "HY2 account configuration is installed"
  if command -v systemctl >/dev/null 2>&1; then
    for unit in "$HY2_ACCOUNT_UNIT" "$HY2_SERVER_UNIT" "$HY2_HEALTH_TIMER"; do
      if systemctl is-active --quiet "$unit"; then ok "${unit} is active"; else warn "${unit} is not active"; fi
    done
    systemctl show "$HY2_SERVER_UNIT" --property=ActiveState --property=LoadState --no-pager 2>/dev/null || true
  fi
  if [[ -S /run/hy2-account/admin.sock ]] && command -v "$HY2_ADMIN_BIN" >/dev/null 2>&1; then
    # Read-only local status/list output. Never print credential material.
    "$HY2_ADMIN_BIN" status 2>/dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("[OK] accountd status read" if v.get("authentication_allowed") else "[WARN] authentication is closed"); print("healthy=%s core_live=%s shared_health_blocked=%s" % (v.get("healthy"),v.get("core_live"),v.get("shared_health_blocked"))); print("vm_error=%s stats_error=%s" % (v.get("vm_error") or "-",v.get("stats_error") or "-"))' 2>/dev/null || warn "accountd status is unreadable"
    "$HY2_ADMIN_BIN" user list 2>/dev/null || warn "account list is unavailable"
  else
    warn "accountd admin socket/binary is unavailable"
  fi
  if command -v ss >/dev/null 2>&1; then
    if ss -lunp 2>/dev/null | grep -q ":${HY2_PORT} "; then
      if ss -lunp 2>/dev/null | grep ":${HY2_PORT} " | grep -q 'hysteria'; then
        ok "official HY2 owns udp:${HY2_PORT}"
      else
        warn "udp:${HY2_PORT} is listening but not clearly owned by official hysteria"
      fi
    else
      warn "official HY2 udp:${HY2_PORT} is not listening"
    fi
  fi
  if [[ -n "${config_path:-}" ]] && grep -q '"type"[[:space:]]*:[[:space:]]*"hysteria2"' "$config_path"; then
    warn "legacy sing-box hysteria2 inbound still exists; keep it stopped and finish migration"
  else
    ok "no legacy sing-box hysteria2 inbound detected"
  fi
  if [[ -f "$HY2_CERT_PATH" ]] && command -v openssl >/dev/null 2>&1; then
    openssl x509 -in "$HY2_CERT_PATH" -noout -subject -issuer -dates -ext subjectAltName || warn "HY2 certificate is invalid"
    if openssl x509 -in "$HY2_CERT_PATH" -noout -checkend 86400 >/dev/null; then ok "HY2 certificate valid for at least 24h"; else warn "HY2 certificate expires within 24h"; fi
  else
    warn "HY2 published certificate not found"
  fi
else
  warn "HY2 account backend is not installed; legacy scripts remain the current baseline"
fi

section "Certificate files"
if [[ ! -f "$CERT_PATH" && -f "${CERT_DIR}/cert.crt" ]]; then
  CERT_PATH="${CERT_DIR}/cert.crt"
fi

if [[ (! -f "$CERT_PATH" || ! -f "$KEY_PATH") && -n "${config_path:-}" ]] && command -v python3 >/dev/null 2>&1; then
  mapfile -t configured_tls_paths < <(python3 - "$config_path" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8-sig") as handle:
    data = json.load(handle)

inbounds = data.get("inbounds") or []
ordered = sorted(inbounds, key=lambda item: item.get("type") != "hysteria2")
for inbound in ordered:
    tls = inbound.get("tls") or {}
    certificate_path = tls.get("certificate_path")
    key_path = tls.get("key_path")
    if certificate_path and key_path:
        print(certificate_path)
        print(key_path)
        break
PY
  )
  if [[ "${#configured_tls_paths[@]}" -eq 2 ]]; then
    CERT_PATH="${configured_tls_paths[0]}"
    KEY_PATH="${configured_tls_paths[1]}"
    CERT_DIR="$(dirname "$CERT_PATH")"
  fi
fi

if [[ -f "$CERT_PATH" && -f "$KEY_PATH" ]]; then
  ok "certificate files exist in ${CERT_DIR}"
  if command -v openssl >/dev/null 2>&1; then
    openssl x509 -in "$CERT_PATH" -noout -subject -issuer -dates -ext subjectAltName || true
  fi
else
  warn "certificate files not found; Reality does not require them, but VLESS TLS and Hysteria 2 do"
fi

section "Reminder"
cat <<EOF
VLESS Reality does not use Let's Encrypt certificate files.
VLESS TLS uses certificate_path/key_path and should listen on tcp:${TLS_PORT}.
Legacy sing-box HY2 also used these files; migrated official HY2 uses /etc/hysteria/certs/current and should listen on udp:${HY2_PORT}.
If v2rayN speed test shows 403 but latency is available, test real AI websites before changing node settings.
EOF
