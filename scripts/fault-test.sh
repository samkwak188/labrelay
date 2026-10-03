#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/local-env.sh
mkdir -p bin
bash scripts/go.sh build -o bin/labrelay ./cmd/labrelay
bash scripts/go.sh build -o bin/labrelayd ./cmd/labrelayd
python3 scripts/fault-test.py
