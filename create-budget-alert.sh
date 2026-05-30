#!/usr/bin/env bash
set -euo pipefail

: "${PROJECT_ID:?Set PROJECT_ID to the Google Cloud project id to monitor.}"
: "${BILLING_ACCOUNT:?Set BILLING_ACCOUNT to the Cloud Billing account id, for example 000000-000000-000000.}"

BUDGET_AMOUNT="${BUDGET_AMOUNT:-5USD}"
DISPLAY_NAME="${DISPLAY_NAME:-singbox-free-tier-guard}"

if ! command -v gcloud >/dev/null 2>&1; then
  echo "gcloud is required. Install and authenticate Google Cloud CLI first." >&2
  exit 1
fi

gcloud billing budgets create \
  --billing-account "$BILLING_ACCOUNT" \
  --display-name "$DISPLAY_NAME" \
  --budget-amount "$BUDGET_AMOUNT" \
  --calendar-period MONTH \
  --filter-projects "projects/${PROJECT_ID}" \
  --threshold-rule "percent=0.5,basis=current-spend" \
  --threshold-rule "percent=0.9,basis=current-spend" \
  --threshold-rule "percent=1.0,basis=current-spend" \
  --threshold-rule "percent=1.0,basis=forecasted-spend"

echo
echo "Created budget alert '${DISPLAY_NAME}' for ${PROJECT_ID} at ${BUDGET_AMOUNT}."
echo "Budget alerts are notifications, not hard spend caps."
