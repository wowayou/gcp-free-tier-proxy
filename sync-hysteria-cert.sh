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
ACCOUNT_BINARY="${HY2_ACCOUNT_BINARY:-/usr/local/lib/hy2-account/hy2-account}"
DOMAIN="$(python3 - "$CONFIG" <<'PY'
import json,sys
with open(sys.argv[1],encoding='utf-8') as f: print(json.load(f)['sni'])
PY
)"
[[ -f "$CERT_PATH" && -f "$KEY_PATH" ]] || { echo 'Certificate/key missing.' >&2; exit 1; }
exec 9>"$LOCK_PATH"
flock -w 10 9 || exit 1
# Stage first: ACME renewal can replace source paths independently of our lock.
# Only root may write these copies; every validation below reads this pair.
install -d -o root -g hysteria -m 0750 "$CERT_ROOT"
version="$(date -u +%Y%m%d-%H%M%S)-$$"
target="$CERT_ROOT/$version"
link="$CERT_ROOT/.current-$$"
published=false
cleanup() {
  local status=$?
  trap - EXIT
  if [[ "$published" != true ]]; then
    rm -f -- "$link" || status=1
    # mv may have committed before it was interrupted/reported an error.
    # Never remove a version actually referenced by current.
    if [[ ! "$target" -ef "$CERT_ROOT/current" ]]; then
      rm -rf -- "$target" || status=1
    fi
  fi
  exit "$status"
}
# mkdir refuses an existing version rather than reusing/overwriting it.
mkdir -m 0750 -- "$target"
trap cleanup EXIT
install -d -o root -g hysteria -m 0750 "$target"
install -o root -g hysteria -m 0640 "$CERT_PATH" "$target/fullchain.cer"
install -o root -g hysteria -m 0640 "$KEY_PATH" "$target/private.key"
# Go/HY2 SAN-only hostname and validity rules, plus the existing >24h margin
# and TLS key-pair parsing. Missing/old validators fail closed; no fallback.
"$ACCOUNT_BINARY" validate-cert "$target/fullchain.cer" "$target/private.key" "$DOMAIN" || {
  echo 'Staged certificate/key validation failed; current version untouched.' >&2
  exit 1
}
ln -s "$version" "$link"
mv -Tf "$link" "$CERT_ROOT/current"
published=true
# HY2 reads certificates per handshake; no HY2 restart. Keep at most 3 copies.
python3 - "$CERT_ROOT" <<'PY'
import pathlib,re,shutil,sys
r=pathlib.Path(sys.argv[1]); current=(r/'current').resolve()
versions=sorted(p for p in r.iterdir() if p.is_dir() and not p.is_symlink() and re.fullmatch(r'\d{8}-\d{6}-\d+',p.name))
for p in versions[:-3]:
    if p.resolve()!=current: shutil.rmtree(p)
PY
echo "Validated HY2 certificate published: $target (no HY2 restart)"
