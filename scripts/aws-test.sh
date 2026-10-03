#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
: "${S3_BUCKET:?dedicated private versioned acceptance bucket required}"
: "${DATABASE_URL:?isolated PostgreSQL database required}"
: "${AWS_REGION:?required}"
[[ -z "${S3_ENDPOINT:-}" ]] || { echo 'AWS acceptance must use real S3, not a custom endpoint' >&2; exit 2; }
export LABRELAY_INTEGRATION=1
bash scripts/go.sh test -count=1 -v ./internal/server
