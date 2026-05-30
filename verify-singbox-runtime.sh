#!/usr/bin/env bash
set -uo pipefail

SERVICE_NAME="${SERVICE_NAME:-sing-box}"
PROXY_PORT="${PROXY_PORT:-47370}"
TLS_PORT="${TLS_PORT:-443}"
EGRESS_TIMER="${EGRESS_TIMER:-gcp-egress-guard.timer}"
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

  if ss -lntp 2>/dev/null | grep -q ":${port} "; then
    ok "${label} tcp:${port} is listening"
    ss -lntp 2>/dev/null | grep ":${port} " || true
  else
    fail "${label} tcp:${port} is not listening"
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

section "Listener"
if command -v ss >/dev/null 2>&1; then
  check_listener "$PROXY_PORT" "Reality"
  check_listener "$TLS_PORT" "VLESS TLS"
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
  grep -q "\"${TLS_PORT}\"\\|:${TLS_PORT}\\|${TLS_PORT}" "$config_path" && ok "TLS port ${TLS_PORT} is present" || warn "TLS port ${TLS_PORT} not found"
  grep -q 'certificate_path' "$config_path" && ok "TLS certificate_path field exists" || warn "TLS certificate_path field not found"
  grep -q 'key_path' "$config_path" && ok "TLS key_path field exists" || warn "TLS key_path field not found"
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
fi

section "Certificate files"
if [[ ! -f "$CERT_PATH" && -f "${CERT_DIR}/cert.crt" ]]; then
  CERT_PATH="${CERT_DIR}/cert.crt"
fi

if [[ -f "$CERT_PATH" && -f "$KEY_PATH" ]]; then
  ok "certificate files exist in ${CERT_DIR}"
  if command -v openssl >/dev/null 2>&1; then
    openssl x509 -in "$CERT_PATH" -noout -subject -issuer -dates -ext subjectAltName || true
  fi
else
  warn "certificate files not found; VLESS Reality does not require them, but VLESS TLS does"
fi

section "Reminder"
cat <<EOF
VLESS Reality does not use Let's Encrypt certificate files.
VLESS TLS uses certificate_path/key_path and should listen on tcp:${TLS_PORT}.
If v2rayN speed test shows 403 but latency is available, test real AI websites before changing node settings.
EOF
