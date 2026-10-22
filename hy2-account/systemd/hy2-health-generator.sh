#!/usr/bin/env bash
set -euo pipefail
out="${2:?early output directory required}"
blocked="${HY2_HEALTH_BLOCKED_FILE:-/var/lib/hy2-health/blocked}"
if [[ -e "$blocked" || -L "$blocked" ]]; then
  mkdir -p "$out"
  ln -sf /dev/null "$out/hysteria-server.service"
fi
