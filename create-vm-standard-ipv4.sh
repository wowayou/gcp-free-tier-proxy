#!/usr/bin/env bash
set -euo pipefail

: "${PROJECT_ID:?Set PROJECT_ID to your Google Cloud project id.}"
: "${SSH_SOURCE_RANGE:?Set SSH_SOURCE_RANGE to your public IP CIDR, for example 203.0.113.10/32.}"

NAME="${NAME:-singbox-free}"
ZONE="${ZONE:-us-west1-b}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-micro}"
IMAGE_FAMILY="${IMAGE_FAMILY:-debian-12}"
IMAGE_PROJECT="${IMAGE_PROJECT:-debian-cloud}"
BOOT_DISK_SIZE="${BOOT_DISK_SIZE:-30GB}"
BOOT_DISK_TYPE="${BOOT_DISK_TYPE:-pd-standard}"
NETWORK="${NETWORK:-default}"
SUBNET="${SUBNET:-default}"
PROXY_PORT="${PROXY_PORT:-47370}"
PROXY_TAG="${PROXY_TAG:-singbox-proxy}"
SSH_TAG="${SSH_TAG:-ssh-restricted}"

require_gcloud() {
  if ! command -v gcloud >/dev/null 2>&1; then
    echo "gcloud is required. Install and authenticate Google Cloud CLI first." >&2
    exit 1
  fi
}

ensure_firewall_rule() {
  local name="$1"
  shift

  if gcloud compute firewall-rules describe "$name" --project "$PROJECT_ID" >/dev/null 2>&1; then
    echo "Firewall rule exists: $name"
    return
  fi

  gcloud compute firewall-rules create "$name" --project "$PROJECT_ID" "$@"
}

require_gcloud

echo "Project: $PROJECT_ID"
echo "Zone: $ZONE"
echo "Instance: $NAME"
echo "Network tier: STANDARD"
echo "Boot disk: $BOOT_DISK_SIZE $BOOT_DISK_TYPE"

ensure_firewall_rule "allow-${PROXY_TAG}-${PROXY_PORT}" \
  --network "$NETWORK" \
  --direction INGRESS \
  --priority 1000 \
  --action ALLOW \
  --rules "tcp:${PROXY_PORT}" \
  --source-ranges "0.0.0.0/0" \
  --target-tags "$PROXY_TAG" \
  --description "Allow sing-box proxy port only."

ensure_firewall_rule "allow-${SSH_TAG}-22" \
  --network "$NETWORK" \
  --direction INGRESS \
  --priority 1000 \
  --action ALLOW \
  --rules "tcp:22" \
  --source-ranges "$SSH_SOURCE_RANGE" \
  --target-tags "$SSH_TAG" \
  --description "Allow SSH only from the operator public IP range."

gcloud compute instances create "$NAME" \
  --project "$PROJECT_ID" \
  --zone "$ZONE" \
  --machine-type "$MACHINE_TYPE" \
  --provisioning-model STANDARD \
  --maintenance-policy MIGRATE \
  --network-interface "network=${NETWORK},subnet=${SUBNET},network-tier=STANDARD" \
  --tags "${PROXY_TAG},${SSH_TAG}" \
  --image-family "$IMAGE_FAMILY" \
  --image-project "$IMAGE_PROJECT" \
  --boot-disk-size "$BOOT_DISK_SIZE" \
  --boot-disk-type "$BOOT_DISK_TYPE" \
  --boot-disk-auto-delete \
  --no-service-account \
  --no-scopes

echo
echo "Created $NAME. Verify the external IP charge and set a billing budget before sustained use."
