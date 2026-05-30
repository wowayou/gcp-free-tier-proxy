#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null || true)}"
VM_NAME="${VM_NAME:-singbox-free}"
ZONE="${ZONE:-us-west1-b}"
NETWORK="${NETWORK:-default}"
TLS_PORT="${TLS_PORT:-443}"
PROXY_TAG="${PROXY_TAG:-singbox-proxy}"
RULE_NAME="${RULE_NAME:-allow-singbox-vless-tls-${TLS_PORT}}"
SOURCE_RANGES="${SOURCE_RANGES:-0.0.0.0/0}"

if ! command -v gcloud >/dev/null 2>&1; then
  echo "gcloud is required. Run this script in Google Cloud Shell." >&2
  exit 1
fi

if [[ -z "$PROJECT_ID" ]]; then
  echo "Set PROJECT_ID or configure a default gcloud project." >&2
  exit 1
fi

echo "Project: $PROJECT_ID"
echo "VM: $VM_NAME"
echo "Zone: $ZONE"
echo "Port: tcp:$TLS_PORT"
echo "Target tag: $PROXY_TAG"
echo "Source ranges: $SOURCE_RANGES"

gcloud compute instances add-tags "$VM_NAME" \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --tags "$PROXY_TAG"

if gcloud compute firewall-rules describe "$RULE_NAME" --project "$PROJECT_ID" >/dev/null 2>&1; then
  gcloud compute firewall-rules update "$RULE_NAME" \
    --project "$PROJECT_ID" \
    --rules "tcp:${TLS_PORT}" \
    --source-ranges "$SOURCE_RANGES" \
    --target-tags "$PROXY_TAG"
else
  gcloud compute firewall-rules create "$RULE_NAME" \
    --project "$PROJECT_ID" \
    --network "$NETWORK" \
    --direction INGRESS \
    --priority 1000 \
    --action ALLOW \
    --rules "tcp:${TLS_PORT}" \
    --source-ranges "$SOURCE_RANGES" \
    --target-tags "$PROXY_TAG" \
    --description "Allow VLESS TLS sing-box port."
fi

echo
echo "Done. Verify with:"
echo "gcloud compute firewall-rules list --project ${PROJECT_ID} --filter='name=${RULE_NAME}'"
echo "gcloud compute instances describe ${VM_NAME} --zone ${ZONE} --project ${PROJECT_ID} --format='get(tags.items)'"
echo
echo "After validation, tighten the source range, for example:"
echo "SOURCE_RANGES='203.0.113.10/32' TLS_PORT='${TLS_PORT}' bash open-vless-tls-port.sh"
