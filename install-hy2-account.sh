#!/usr/bin/env bash
# Prepare only; does not touch sing-box config, UDP 443, firewall or live core.
set -euo pipefail
[[ "$EUID" -eq 0 && "${CONFIRM_LOCAL_INSTALL:-}" == yes ]] || { echo 'VM only: authorize local installation with CONFIRM_LOCAL_INSTALL=yes; no migration performed.' >&2; exit 1; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Never prepare a public HY2 service before the global guard covers both
# proxies. The guard installer intentionally preserves an existing service list.
GUARD_CONFIG="${GUARD_CONFIG:-/etc/default/gcp-egress-guard}"
[[ -r "$GUARD_CONFIG" ]] || { echo "Install/upgrade gcp-egress-guard first with SERVICE_NAMES='sing-box hysteria-server'." >&2; exit 1; }
guard_services="$(grep -E '^SERVICE_NAMES=' "$GUARD_CONFIG" | tail -n 1 || true)"
[[ "$guard_services" == *sing-box* && "$guard_services" == *hysteria-server* ]] || {
  echo 'gcp-egress-guard does not cover both sing-box and hysteria-server; refusing partial protection.' >&2
  exit 1
}
systemctl is-active --quiet gcp-egress-guard.timer || { echo 'gcp-egress-guard.timer is not active; finish the guard installation first.' >&2; exit 1; }
ACCOUNT_BINARY="${ACCOUNT_BINARY:?Provide locally built static hy2-account binary}"
HYSTERIA_BINARY="${HYSTERIA_BINARY:?Provide checksum-verified official binary; installer never downloads}"
HY2_DOMAIN="${HY2_DOMAIN:?Set existing certificate domain}"
HY2_SERVER_ADDRESS="${HY2_SERVER_ADDRESS:-$HY2_DOMAIN}"
QUOTA_POOL_GIB="${QUOTA_POOL_GIB:?Set explicit <=140 GiB pool after billing/shared-allowance audit}"
AUTH_PORT="${AUTH_PORT:-18081}" PAGE_PORT="${PAGE_PORT:-18082}" STATS_PORT="${STATS_PORT:-18083}"
[[ "$QUOTA_POOL_GIB" =~ ^[0-9]+$ ]] && ((QUOTA_POOL_GIB>0 && QUOTA_POOL_GIB<=140)) || exit 1
for c in python3 sha256sum systemctl install getent useradd groupadd ss runuser openssl flock; do command -v "$c" >/dev/null || { echo "Missing $c" >&2; exit 1; }; done
[[ -x "$ACCOUNT_BINARY" && -x "$HYSTERIA_BINARY" ]] || exit 1
for unit in hysteria-server hy2-accountd; do
  if systemctl is-active --quiet "$unit.service"; then echo "$unit is active; upgrades require an authorized maintenance window." >&2; exit 1; fi
done
[[ ! -e /etc/hy2-account/config.json && ! -e /var/lib/hy2-account/accounts.sqlite ]] || { echo 'Already installed. Preserve state/secrets; use the documented maintenance procedure, not reinstall.' >&2; exit 1; }
for p in "$AUTH_PORT" "$PAGE_PORT" "$STATS_PORT"; do
  [[ "$p" =~ ^[0-9]+$ ]] && ((p>1024 && p<=65535)) || exit 1
  if ss -H -lnt | awk -v p=":$p" '$4 ~ p"$" {found=1} END {exit !found}'; then echo "Local TCP port $p already used." >&2; exit 1; fi
done
[[ "$AUTH_PORT" != "$PAGE_PORT" && "$AUTH_PORT" != "$STATS_PORT" && "$PAGE_PORT" != "$STATS_PORT" ]] || exit 1
case "$(uname -m)" in
  x86_64) hash=907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691 ;;
  aarch64) hash=a68a61a84452ca250ce0368202521965ca9cc9d801a404f1dc9008ac6cf677a7 ;;
  *) exit 1 ;;
esac
printf '%s  %s\n' "$hash" "$HYSTERIA_BINARY" | sha256sum -c -
"$ACCOUNT_BINARY" version
getent group hysteria >/dev/null || groupadd --system hysteria
getent group hy2-account >/dev/null || groupadd --system hy2-account
getent passwd hysteria >/dev/null || useradd --system --gid hysteria --home-dir /nonexistent --shell /usr/sbin/nologin hysteria
getent passwd hy2-account >/dev/null || useradd --system --gid hy2-account --home-dir /nonexistent --shell /usr/sbin/nologin hy2-account
for u in hysteria hy2-account; do [[ "$(id -u "$u")" -ne 0 ]] || exit 1; done
usermod -a -G hysteria hy2-account
install -d -m 0755 /usr/local/lib/hy2-account /usr/local/lib/hysteria
install -m 0755 "$ACCOUNT_BINARY" /usr/local/lib/hy2-account/hy2-account
install -m 0755 "$HYSTERIA_BINARY" /usr/local/lib/hysteria/hysteria
ln -s /usr/local/lib/hy2-account/hy2-account /usr/local/sbin/hy2-admin
install -d -o root -g hy2-account -m 0750 /etc/hy2-account
install -d -o root -g hysteria -m 0750 /etc/hysteria
install -d -o hy2-account -g hy2-account -m 0700 /var/lib/hy2-account /var/lib/hy2-account-witness
install -d -o root -g root -m 0700 /root/hy2-credentials
# The accountd user must be able to Lstat the latch path (directory execute),
# while the latch contents and writes stay root-only. A 0700 parent would make
# EACCES look like a permanent shared-health block.
install -d -o root -g root -m 0755 /var/lib/hy2-health
/usr/local/lib/hy2-account/hy2-account secrets-init /etc/hy2-account/secrets.json /root/hy2-credentials/page-login.txt
chown hy2-account:hy2-account /etc/hy2-account/secrets.json
python3 - "$HY2_SERVER_ADDRESS" "$HY2_DOMAIN" "$QUOTA_POOL_GIB" "$AUTH_PORT" "$PAGE_PORT" "$STATS_PORT" <<'PY'
import json,sys
server,sni,pool,auth,page,stats=sys.argv[1:]
c={"database":"/var/lib/hy2-account/accounts.sqlite","anchor":"/var/lib/hy2-account-witness/revision.json",
 "secrets_file":"/etc/hy2-account/secrets.json","auth_listen":f"127.0.0.1:{auth}","page_listen":f"127.0.0.1:{page}",
 "admin_socket":"/run/hy2-account/admin.sock","stats_url":f"http://127.0.0.1:{stats}",
 "generation_file":"/run/hysteria/generation.json","guard_status_file":"/run/gcp-egress-guard/status.json",
 "health_file":"/run/hy2-account/health.json","health_blocked_file":"/var/lib/hy2-health/blocked",
 "certificate":"/etc/hysteria/certs/current/fullchain.cer","server":server,"sni":sni,"port":443,
 "quota_pool_bytes":int(pool)*2**30,"max_accounts":8,"max_sessions_per_user_generation":512,
 "max_sessions_total_generation":2048,"backup_dir":"/var/lib/hy2-account/backups"}
with open('/etc/hy2-account/config.json','w',encoding='utf-8') as f: json.dump(c,f,indent=2);f.write('\n')
PY
chmod 0640 /etc/hy2-account/config.json
chown root:hy2-account /etc/hy2-account/config.json
/usr/local/lib/hy2-account/hy2-account init
chown -R hy2-account:hy2-account /var/lib/hy2-account /var/lib/hy2-account-witness
/usr/local/lib/hy2-account/hy2-account render-core 0.0.0.0:443 /etc/hysteria/certs/current/fullchain.cer /etc/hysteria/certs/current/private.key /etc/hysteria/server.json
chown root:hysteria /etc/hysteria/server.json; chmod 0640 /etc/hysteria/server.json
for unit in hy2-accountd.service hysteria-server.service hy2-health.service hy2-health.timer; do
  install -m 0644 "$ROOT/hy2-account/systemd/$unit" "/etc/systemd/system/$unit"
done
install -m 0755 "$ROOT/sync-hysteria-cert.sh" /usr/local/sbin/sync-hysteria-cert
install -d -m 0755 /etc/systemd/system-generators
install -m 0755 "$ROOT/hy2-account/systemd/hy2-health-generator.sh" /etc/systemd/system-generators/hy2-health
systemctl daemon-reload
systemctl enable --now hy2-accountd.service
# DO NOT start/enable hysteria or its health timer until guard + accounts +
# certificate renewal hook have all passed the authorized migration gates.
echo 'Prepared only: accountd is running but authentication is held closed. No proxy/config/cloud migration performed.'
echo 'Page credential file: /root/hy2-credentials/page-login.txt (0600; do not print into shared logs)'
