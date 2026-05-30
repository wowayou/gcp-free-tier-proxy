#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null || true)}"

if ! command -v gcloud >/dev/null 2>&1; then
  echo "gcloud is required. Install and authenticate Google Cloud CLI first." >&2
  exit 1
fi

if [[ -z "${PROJECT_ID}" ]]; then
  echo "Set PROJECT_ID or configure a default gcloud project." >&2
  exit 1
fi

section() {
  printf '\n==== %s ====\n' "$1"
}

run() {
  "$@" || true
}

echo "Read-only leftover audit for project: $PROJECT_ID"

section "VM instances"
run gcloud compute instances list \
  --project "$PROJECT_ID" \
  --format "table(name,zone,machineType.basename(),status,networkInterfaces[].accessConfigs[].natIP)"

section "Persistent disks"
run gcloud compute disks list \
  --project "$PROJECT_ID" \
  --format "table(name,zone,type.basename(),sizeGb,users)"

section "Snapshots"
run gcloud compute snapshots list \
  --project "$PROJECT_ID" \
  --format "table(name,diskSizeGb,storageBytes,status,creationTimestamp)"

section "Machine images"
run gcloud compute machine-images list \
  --project "$PROJECT_ID" \
  --format "table(name,status,storageLocations,creationTimestamp)"

section "External IP addresses"
run gcloud compute addresses list \
  --project "$PROJECT_ID" \
  --format "table(name,region,address,addressType,status,users)"

section "Snapshot/resource policies"
run gcloud compute resource-policies list \
  --project "$PROJECT_ID" \
  --format "table(name,region,creationTimestamp)"

section "Forwarding rules / load balancer entrypoints"
run gcloud compute forwarding-rules list \
  --project "$PROJECT_ID" \
  --format "table(name,region,IPAddress,IPProtocol,target.basename())"

section "Cloud Routers / NAT"
run gcloud compute routers list \
  --project "$PROJECT_ID" \
  --format "table(name,region,network.basename())"

section "Cloud DNS managed zones"
run gcloud dns managed-zones list \
  --project "$PROJECT_ID" \
  --format "table(name,dnsName,visibility)"

cat <<'EOF'

Review notes:
- Delete snapshots and machine images you do not need.
- Release static external IP addresses whose USERS column is empty.
- Delete unattached disks unless you intentionally keep them.
- This script does not delete anything.
EOF
