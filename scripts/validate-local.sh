#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
out="results/local/checks-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$out"
bash scripts/check.sh 2>&1 | tee "$out/checks.log"
source scripts/local-env.sh
LABRELAY_INTEGRATION=1 bash scripts/go.sh test -race -count=1 -v ./internal/server 2>&1 | tee "$out/integration.log"
bash scripts/fault-test.sh
printf '%s\n' "$out"
