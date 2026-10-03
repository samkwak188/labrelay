#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/test-env.sh aws
bash scripts/host-preflight.sh
export LABRELAY_EVIDENCE_ROOT="${LABRELAY_EVIDENCE_ROOT:-$PWD/results/local/aws-acceptance-$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$LABRELAY_EVIDENCE_ROOT"
python3 scripts/aws-preflight.py
export LABRELAY_INTEGRATION=1
bash scripts/go.sh test -race -count=1 -v ./internal/server 2>&1 | tee "$LABRELAY_EVIDENCE_ROOT/integration.log"
bash scripts/fault-test.sh aws
bash scripts/restore-drill.sh aws
