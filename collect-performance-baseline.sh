#!/usr/bin/env bash
set -uo pipefail

SERVICE_NAME="${SERVICE_NAME:-sing-box}"
REALITY_PORT="${REALITY_PORT:-47370}"
TLS_PORT="${TLS_PORT:-443}"
TLS_FALLBACK_PORT="${TLS_FALLBACK_PORT:-8443}"
HYSTERIA2_PORT="${HYSTERIA2_PORT:-443}"

section() {
  printf '\n==== %s ====\n' "$1"
}

run() {
  "$@" 2>&1 || true
}

echo "Read-only performance baseline; no configuration or credentials are printed."
echo "timestampUtc: $(date -u +%Y-%m-%dT%H:%M:%SZ)"

section "Host"
run uname -a
run uptime
run free -h
if command -v vmstat >/dev/null 2>&1; then
  run vmstat 1 5
fi
if command -v lscpu >/dev/null 2>&1; then
  run lscpu
fi

section "sing-box"
if command -v sing-box >/dev/null 2>&1; then
  run sing-box version
else
  echo "sing-box binary not found in PATH"
fi
if command -v systemctl >/dev/null 2>&1; then
  run systemctl show "$SERVICE_NAME" \
    --property=ActiveState,SubState,MainPID,MemoryCurrent,CPUUsageNSec,TasksCurrent,ExecMainStartTimestamp
fi
run ps -C "$SERVICE_NAME" -o pid,etimes,%cpu,%mem,rss,vsz,cmd

section "Listeners"
if command -v ss >/dev/null 2>&1; then
  run ss -s
  ss -lntup 2>/dev/null | awk -v ports=":(${REALITY_PORT}|${TLS_PORT}|${TLS_FALLBACK_PORT}|${HYSTERIA2_PORT})" \
    'NR == 1 || $5 ~ ports {print}' || true
fi

section "Network"
iface="$(ip route show default 2>/dev/null | awk '{print $5; exit}')"
echo "defaultInterface: ${iface:-UNKNOWN}"
if [[ -n "$iface" ]]; then
  run ip -s link show dev "$iface"
  if command -v tc >/dev/null 2>&1; then
    run tc -s qdisc show dev "$iface"
  fi
fi
run ip route show default

section "Kernel transport settings"
if command -v sysctl >/dev/null 2>&1; then
  run sysctl \
    net.core.rmem_max \
    net.core.wmem_max \
    net.ipv4.tcp_congestion_control \
    net.ipv4.tcp_mtu_probing
fi

section "Egress guard"
if command -v systemctl >/dev/null 2>&1; then
  run systemctl show gcp-egress-guard.timer \
    --property=ActiveState,SubState,LastTriggerUSec,NextElapseUSecRealtime
fi
if [[ -r /etc/default/gcp-egress-guard ]]; then
  run sed -n '1,20p' /etc/default/gcp-egress-guard
fi
if [[ -r /var/lib/gcp-egress-guard/state ]]; then
  run sed -n '1,20p' /var/lib/gcp-egress-guard/state
fi

section "Sampling guidance"
cat <<'EOF'
Capture this output once while idle and once during each protocol test.
Also record client-side RTT, packet loss, success rate, connect time, TTFB,
total time, and transferred bytes. Never include UUIDs, passwords, private keys,
share links, access tokens, or complete sing-box configuration in the evidence.
EOF
