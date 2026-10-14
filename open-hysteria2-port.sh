#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null || true)}"
VM_NAME="${VM_NAME:-singbox-free}"
ZONE="${ZONE:-us-west1-b}"
NETWORK="${NETWORK:-default}"
HY2_PORT="${HY2_PORT:-443}"
HY2_TAG="${HY2_TAG:-singbox-hysteria2}"
RULE_NAME="${RULE_NAME:-allow-singbox-hysteria2-${HY2_PORT}}"
: "${SOURCE_RANGES:?Set SOURCE_RANGES explicitly. Use trusted CIDRs when possible; use 0.0.0.0/0 only when clients have dynamic addresses.}"

if [[ "${CONFIRM_COST_AUDIT:-}" != "yes" ]]; then
  echo "Refusing to open HY2 before the cost gate is acknowledged." >&2
  echo "Run audit-cost-and-usage.sh and review Billing, then set CONFIRM_COST_AUDIT=yes." >&2
  exit 1
fi

if ! command -v gcloud >/dev/null 2>&1; then
  echo "gcloud is required. Run this script in Google Cloud Shell." >&2
  exit 1
fi

if [[ -z "$PROJECT_ID" ]]; then
  echo "Set PROJECT_ID or configure a default gcloud project." >&2
  exit 1
fi

if ! [[ "$HY2_PORT" =~ ^[0-9]+$ ]] || ((HY2_PORT < 1 || HY2_PORT > 65535)); then
  echo "HY2_PORT must be an integer from 1 to 65535." >&2
  exit 1
fi

if [[ "$SOURCE_RANGES" == "0.0.0.0/0" ]]; then
  echo "[WARN] UDP ${HY2_PORT} will be reachable from the public IPv4 internet." >&2
  echo "[WARN] Keep per-user passwords strong and revoke unused users promptly." >&2
fi

echo "Project: $PROJECT_ID"
echo "VM: $VM_NAME"
echo "Zone: $ZONE"
echo "Port: udp:$HY2_PORT"
echo "Source ranges: $SOURCE_RANGES"
echo "Target tag: $HY2_TAG"

gcloud compute instances add-tags "$VM_NAME" \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --tags "$HY2_TAG"

if gcloud compute firewall-rules describe "$RULE_NAME" --project "$PROJECT_ID" >/dev/null 2>&1; then
  gcloud compute firewall-rules update "$RULE_NAME" \
    --project "$PROJECT_ID" \
    --rules "udp:${HY2_PORT}" \
    --source-ranges "$SOURCE_RANGES" \
    --target-tags "$HY2_TAG"
else
  gcloud compute firewall-rules create "$RULE_NAME" \
    --project "$PROJECT_ID" \
    --network "$NETWORK" \
    --direction INGRESS \
    --priority 1000 \
    --action ALLOW \
    --rules "udp:${HY2_PORT}" \
    --source-ranges "$SOURCE_RANGES" \
    --target-tags "$HY2_TAG" \
    --description "Allow the sing-box Hysteria 2 test inbound."
fi

echo
echo "Firewall rule is ready. Verify with:"
echo "gcloud compute firewall-rules describe ${RULE_NAME} --project ${PROJECT_ID}"
echo
echo "Rollback after the test:"
echo "gcloud compute firewall-rules delete ${RULE_NAME} --project ${PROJECT_ID}"
