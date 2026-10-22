#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo bash issue-acme-cloudflare-token.sh" >&2
  exit 1
fi

ACME_HOME="${ACME_HOME:-/root/.acme.sh}"
CERT_DIR="${CERT_DIR:-/root/ygkkkca}"
CERT_PATH="${CERT_PATH:-${CERT_DIR}/cert.crt}"
KEY_PATH="${KEY_PATH:-${CERT_DIR}/private.key}"
CA_PATH="${CA_PATH:-${CERT_DIR}/ca.cer}"
FULLCHAIN_PATH="${FULLCHAIN_PATH:-${CERT_DIR}/fullchain.cer}"
KEY_TYPE="${KEY_TYPE:-ec-256}"
SERVER="${SERVER:-letsencrypt}"
DNS_SLEEP="${DNS_SLEEP:-120}"
RELOAD_CMD="${RELOAD_CMD:-}"
if [[ -z "$RELOAD_CMD" ]]; then
  if [[ -f /etc/hy2-account/config.json ]]; then
    [[ -x /usr/local/sbin/sync-hysteria-cert ]] || { echo 'HY2 certificate synchronization hook is missing.' >&2; exit 1; }
    # ACME installs original files first; publish validated private copies for
    # HY2, then update only the retained sing-box TLS service.
    RELOAD_CMD="CERT_PATH='$FULLCHAIN_PATH' KEY_PATH='$KEY_PATH' /usr/local/sbin/sync-hysteria-cert && systemctl try-restart sing-box"
  else
    RELOAD_CMD="systemctl try-restart sing-box"
  fi
fi

prompt_if_empty() {
  local var_name="$1"
  local prompt="$2"
  local secret="${3:-false}"
  local current="${!var_name:-}"

  if [[ -n "$current" ]]; then
    return
  fi

  if [[ "$secret" == "true" ]]; then
    read -r -s -p "$prompt: " "$var_name"
    echo
  else
    read -r -p "$prompt: " "$var_name"
  fi
}

install_acme() {
  if [[ -x "${ACME_HOME}/acme.sh" ]]; then
    return
  fi

  if ! command -v curl >/dev/null 2>&1 || ! command -v crontab >/dev/null 2>&1; then
    apt-get update
    apt-get install -y curl ca-certificates cron
  fi

  curl https://get.acme.sh | sh -s email="${ACME_EMAIL}"
}

cleanup_old_cloudflare_key() {
  local account_conf="${ACME_HOME}/account.conf"

  if [[ ! -f "$account_conf" ]]; then
    return
  fi

  cp "$account_conf" "${account_conf}.bak.$(date -u +%Y%m%d-%H%M%S)"
  sed -i \
    -e '/^SAVED_CF_Key=/d' \
    -e '/^SAVED_CF_Email=/d' \
    -e '/^CF_Key=/d' \
    -e '/^CF_Email=/d' \
    "$account_conf"
}

prompt_if_empty DOMAIN "Certificate domain, for example rr.example.com or example.com"
prompt_if_empty ACME_EMAIL "ACME account email"
prompt_if_empty CF_Token "Cloudflare API Token" true

if [[ -z "${CF_Zone_ID:-}" && -z "${CF_Account_ID:-}" ]]; then
  read -r -p "Cloudflare Zone ID for this domain: " CF_Zone_ID
fi

read -r -p "Issue wildcard certificate too? Type yes to include *.${DOMAIN}: " WILDCARD_REPLY

install -d -m 0700 "$CERT_DIR"
install_acme
cleanup_old_cloudflare_key

export CF_Token
export CF_Zone_ID="${CF_Zone_ID:-}"
export CF_Account_ID="${CF_Account_ID:-}"

"${ACME_HOME}/acme.sh" --set-default-ca --server "$SERVER"
"${ACME_HOME}/acme.sh" --register-account -m "$ACME_EMAIL" --server "$SERVER" || true

issue_args=(
  --issue
  --dns dns_cf
  --dnssleep "$DNS_SLEEP"
  --keylength "$KEY_TYPE"
  -d "$DOMAIN"
)

if [[ "$WILDCARD_REPLY" == "yes" ]]; then
  issue_args+=(-d "*.${DOMAIN}")
fi

"${ACME_HOME}/acme.sh" "${issue_args[@]}"

install_args=(
  --install-cert
  --ecc
  -d "$DOMAIN"
  --cert-file "$CERT_PATH"
  --key-file "$KEY_PATH"
  --ca-file "$CA_PATH"
  --fullchain-file "$FULLCHAIN_PATH"
  --reloadcmd "$RELOAD_CMD"
)

"${ACME_HOME}/acme.sh" "${install_args[@]}"

chmod 0600 "$KEY_PATH"
chmod 0644 "$CERT_PATH" "$CA_PATH" "$FULLCHAIN_PATH"

if command -v systemctl >/dev/null 2>&1; then
  systemctl enable cron >/dev/null 2>&1 || true
  systemctl restart cron >/dev/null 2>&1 || true
fi

cat <<EOF

Certificate issued and installed.
Domain: ${DOMAIN}
Cert: ${CERT_PATH}
Key: ${KEY_PATH}
CA: ${CA_PATH}
Full chain: ${FULLCHAIN_PATH}

Cloudflare credentials were saved by acme.sh in:
${ACME_HOME}/account.conf

Check renewal jobs:
crontab -l

Test renewal later:
${ACME_HOME}/acme.sh --cron --home ${ACME_HOME} --force
EOF
