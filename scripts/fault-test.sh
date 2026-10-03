#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/test-env.sh "${1:-local}"
mkdir -p bin
bash scripts/go.sh build -o bin/labrelay ./cmd/labrelay
bash scripts/go.sh build -o bin/labrelayd ./cmd/labrelayd
python3 scripts/fault-test.py
