#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/host-preflight.sh
source scripts/local-env.sh
mkdir -p bin
for binary in labrelay labrelayd; do bash scripts/go.sh build -o "bin/$binary" "./cmd/$binary"; done
python3 scripts/acceptance-workloads.py "$@"
