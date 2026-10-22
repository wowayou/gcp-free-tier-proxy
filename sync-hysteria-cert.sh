#!/usr/bin/env bash
# Root-only ACME install hook. Copy validated versions atomically; do not grant
# hysteria access to /root or relax the original key's permissions.
set -euo pipefail
[[ "$EUID" -eq 0 ]] || { echo 'Run on VM as root.' >&2; exit 1; }
CONFIG="${HY2_ACCOUNT_CONFIG:-/etc/hy2-account/config.json}"
CERT_PATH="${CERT_PATH:-/root/ygkkkca/fullchain.cer}"
KEY_PATH="${KEY_PATH:-/root/ygkkkca/private.key}"
CERT_ROOT="${CERT_ROOT:-/etc/hysteria/certs}"
LOCK_PATH="${SYNC_LOCK_PATH:-/run/lock/sync-hysteria-cert.lock}"
DOMAIN="$(python3 - "$CONFIG" <<'PY'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: print(json.load(f)['sni'])
PY
)"
[[ -f "$CERT_PATH" && -f "$KEY_PATH" ]] || { echo 'Certificate/key missing.' >&2; exit 1; }
exec 9>"$LOCK_PATH"
flock -w 10 9 || exit 1
# `openssl x509 -checkhost` exits 0 even on mismatch (observed: OpenSSL 3.0); only
# its exact positive verdict is accepted. SNI is a validated hostname.
check="$(openssl x509 -in "$CERT_PATH" -noout -checkhost "$DOMAIN")" || exit 1
[[ "$check" == "Hostname $DOMAIN does match certificate" ]] || { echo "Certificate SAN does not cover $DOMAIN; not published." >&2; exit 1; }
openssl x509 -in "$CERT_PATH" -noout -checkend 86400 >/dev/null || { echo 'Certificate expires within 24h; not published.' >&2; exit 1; }
cert_public="$(openssl x509 -in "$CERT_PATH" -pubkey -noout | openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
key_public="$(openssl pkey -in "$KEY_PATH" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
[[ "$cert_public" == "$key_public" ]] || { echo 'Certificate/key mismatch; not published.' >&2; exit 1; }
install -d -o root -g hysteria -m 0750 "$CERT_ROOT"
version="$(date -u +%Y%m%d-%H%M%S)-$$"
target="$CERT_ROOT/$version"
install -d -o root -g hysteria -m 0750 "$target"
install -o root -g hysteria -m 0640 "$CERT_PATH" "$target/fullchain.cer"
install -o root -g hysteria -m 0640 "$KEY_PATH" "$target/private.key"
# Validate staged copy too, not just source paths (renewal can replace them).
staged_cert="$(openssl x509 -in "$target/fullchain.cer" -pubkey -noout | openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
staged_key="$(openssl pkey -in "$target/private.key" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
[[ "$staged_cert" == "$staged_key" ]] || { echo 'Staged cert/key mismatch; current version untouched.' >&2; exit 1; }
ln -s "$version" "$CERT_ROOT/.current-$$"
mv -Tf "$CERT_ROOT/.current-$$" "$CERT_ROOT/current"
# HY2 reads certificates per handshake; no HY2 restart. Keep at most 3 copies.
python3 - "$CERT_ROOT" <<'PY'
import pathlib,re,shutil,sys
r=pathlib.Path(sys.argv[1]); current=(r/'current').resolve()
versions=sorted(p for p in r.iterdir() if p.is_dir() and not p.is_symlink() and re.fullmatch(r'\d{8}-\d{6}-\d+',p.name))
for p in versions[:-3]:
    if p.resolve()!=current: shutil.rmtree(p)
PY
echo "Validated HY2 certificate published: $target (no HY2 restart)"
