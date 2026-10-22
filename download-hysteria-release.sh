#!/usr/bin/env bash
# Download is deliberately separate from install: no network on the VM unless
# explicitly authorized. Exactly one bounded request; no paid fallback/mirrors.
set -euo pipefail
arch="${1:-amd64}"
output="${2:?Usage: bash download-hysteria-release.sh amd64 OUTPUT_PATH}"
case "$arch" in
  amd64) hash=907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691 ;;
  arm64) hash=a68a61a84452ca250ce0368202521965ca9cc9d801a404f1dc9008ac6cf677a7 ;;
  *) echo 'Only pinned Linux amd64/arm64 release binaries are supported.' >&2; exit 1 ;;
esac
[[ ! -e "$output" ]] || { echo 'Output already exists; refusing overwrite.' >&2; exit 1; }
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl --fail --location --proto '=https' --proto-redir '=https' --max-redirs 3 \
  --retry 0 --max-time 90 --max-filesize 26000000 \
  -o "$tmp" "https://github.com/apernet/hysteria/releases/download/app/v2.13.0/hysteria-linux-$arch"
printf '%s  %s\n' "$hash" "$tmp" | sha256sum -c -
install -m 0755 "$tmp" "$output"
echo "Verified pinned official core: $output (v2.13.0, ca8fbd874fd6ca82413948de6dc5924405e103e1)"
