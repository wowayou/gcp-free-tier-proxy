#!/usr/bin/env bash
set -euo pipefail

: "${PROJECT_ID:?Set PROJECT_ID to the Google Cloud project id.}"

VM_NAME="${VM_NAME:-singbox-free}"
ZONE="${ZONE:-us-west1-b}"
WARN_GIB="${WARN_GIB:-150}"
LIMIT_GIB="${LIMIT_GIB:-180}"
STANDARD_FREE_GIB="${STANDARD_FREE_GIB:-200}"
BILLING_TIMEZONE="${BILLING_TIMEZONE:-America/Los_Angeles}"
export CLOUDSDK_CORE_DISABLE_PROMPTS=1

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

section() {
  printf '\n==== %s ====\n' "$1"
}

require_cmd gcloud
require_cmd curl
require_cmd python3
require_cmd date

if ! [[ "$BILLING_TIMEZONE" =~ ^[A-Za-z0-9_+.-]+(/[A-Za-z0-9_+.-]+)*$ ]] ||
   [[ "$BILLING_TIMEZONE" == *".."* ]] ||
   [[ ! -e "/usr/share/zoneinfo/${BILLING_TIMEZONE}" ]]; then
  echo "BILLING_TIMEZONE is not an installed IANA time zone." >&2
  exit 1
fi

section "Project billing link"
billing_json="$(gcloud billing projects describe "$PROJECT_ID" --format=json)"
python3 -c '
import json, sys
data = json.load(sys.stdin)
print("billingEnabled: {}".format(data.get("billingEnabled", False)))
print("billingAccountName: {}".format(data.get("billingAccountName", "UNLINKED")))
' <<<"$billing_json"

billing_account="$({
  python3 -c '
import json, sys
name = json.load(sys.stdin).get("billingAccountName", "")
print(name.rsplit("/", 1)[-1] if name else "")
' <<<"$billing_json"
})"

section "VM cost-critical properties"
instance_json="$({
  gcloud compute instances describe "$VM_NAME" \
    --project "$PROJECT_ID" \
    --zone "$ZONE" \
    --format=json
})"

python3 -c '
import json, sys
data = json.load(sys.stdin)
interfaces = data.get("networkInterfaces", [])
tiers = []
ips = []
for interface in interfaces:
    for access in interface.get("accessConfigs", []):
        tiers.append(access.get("networkTier", "UNSPECIFIED"))
        if access.get("natIP"):
            ips.append(access["natIP"])
print("name: {}".format(data.get("name")))
print("id: {}".format(data.get("id")))
print("status: {}".format(data.get("status")))
print("zone: {}".format(data.get("zone", "").rsplit("/", 1)[-1]))
print("machineType: {}".format(data.get("machineType", "").rsplit("/", 1)[-1]))
print("networkTier: {}".format(",".join(tiers) if tiers else "NO_EXTERNAL_IP"))
print("externalIPv4Count: {}".format(len(ips)))
' <<<"$instance_json"

instance_id="$({
  python3 -c 'import json, sys; print(json.load(sys.stdin)["id"])' <<<"$instance_json"
})"

gcloud compute disks list \
  --project "$PROJECT_ID" \
  --filter="users~/${VM_NAME}$" \
  --format="table(name,zone.basename(),type.basename(),sizeGb,status,users)"

section "Budgets"
if [[ -n "$billing_account" ]]; then
  if ! gcloud billing budgets list \
    --billing-account "$billing_account" \
    --format="table(displayName,amount.specifiedAmount.currencyCode,amount.specifiedAmount.units,thresholdRules[].thresholdPercent)"; then
    echo "[WARN] Cannot list budgets with the current identity; verify them in Cloud Billing." >&2
  fi
else
  echo "[FAIL] Project is not linked to a billing account." >&2
fi

section "Cloud Monitoring month-to-date sent bytes"
month_start="$(python3 -c '
from datetime import datetime, timezone
from zoneinfo import ZoneInfo
import sys

now = datetime.now(ZoneInfo(sys.argv[1]))
start = now.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
print(start.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
' "$BILLING_TIMEZONE")"
now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
start_epoch="$(date -u -d "$month_start" +%s)"
end_epoch="$(date -u -d "$now" +%s)"
alignment_seconds="$((end_epoch - start_epoch))"
if (( alignment_seconds < 60 )); then
  alignment_seconds=60
fi
alignment_seconds="$((((alignment_seconds + 59) / 60) * 60))"

access_token="$(gcloud auth print-access-token)"
metric_filter="metric.type=\"compute.googleapis.com/instance/network/sent_bytes_count\" AND resource.type=\"gce_instance\" AND resource.labels.instance_id=\"${instance_id}\""
metrics_json="$({
  curl -fsS -G \
    -H "Authorization: Bearer ${access_token}" \
    --data-urlencode "filter=${metric_filter}" \
    --data-urlencode "interval.startTime=${month_start}" \
    --data-urlencode "interval.endTime=${now}" \
    --data-urlencode "aggregation.alignmentPeriod=${alignment_seconds}s" \
    --data-urlencode "aggregation.perSeriesAligner=ALIGN_SUM" \
    --data-urlencode "view=FULL" \
    "https://monitoring.googleapis.com/v3/projects/${PROJECT_ID}/timeSeries"
})"

python3 -c '
import json
import math
import sys

warn_gib, limit_gib, free_gib = map(float, sys.argv[1:])
data = json.load(sys.stdin)
if "error" in data:
    raise SystemExit("Cloud Monitoring API error: {}".format(data["error"]))
total = 0
for series in data.get("timeSeries", []):
    for point in series.get("points", []):
        value = point.get("value", {})
        total += int(value.get("int64Value", 0))
        total += float(value.get("doubleValue", 0))

gib = total / (1024 ** 3)
print(f"sentBytesApproxGiB: {gib:.3f}")
print(f"initialGuardBaselineGiBCeil: {math.ceil(gib)}")
print("scope: all VM network sent bytes; this is not a Cloud Billing meter")
if gib >= limit_gib:
    print(f"decision: STOP ({gib:.3f} GiB >= {limit_gib:g} GiB guard limit)")
elif gib >= warn_gib:
    print(f"decision: HOLD ({gib:.3f} GiB >= {warn_gib:g} GiB warning)")
else:
    print(f"decision: below local warning ({warn_gib:g} GiB)")
print(f"standardTierReferenceGiB: {free_gib:g}")
' "$WARN_GIB" "$LIMIT_GIB" "$STANDARD_FREE_GIB" <<<"$metrics_json"

echo "billingTimezone: $BILLING_TIMEZONE"
echo "intervalStartUtc: $month_start"
echo "intervalEndUtc: $now"

section "Required manual billing verification"
cat <<EOF
[MANUAL REQUIRED]
Open Cloud Billing > Reports for billing account ${billing_account:-UNKNOWN}.
Use calendar month-to-date, filter Project=${PROJECT_ID}, and group by SKU.
Record gross cost, credits, net cost, and every non-zero SKU.

Do not approve a Hysteria 2 trial when:
- an unexpected paid SKU exists;
- Standard Tier cannot be confirmed on the VM;
- sent bytes are at or above ${WARN_GIB} GiB;
- the 180 GiB VM guard is missing, unhealthy, or its baseline was not seeded from this audit.

This script is read-only. Cloud Monitoring sent bytes include traffic that might not
be billable and can differ from the finalized billing report.
EOF
