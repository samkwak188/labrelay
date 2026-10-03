#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/host-preflight.sh
source scripts/local-env.sh
docker compose up -d --wait
mkdir -p bin
bash scripts/go.sh build -o bin/labrelay ./cmd/labrelay
bash scripts/go.sh build -o bin/labrelayd ./cmd/labrelayd
bin/labrelayd migrate
for i in $(seq 1 30); do
  if bin/labrelayd init-storage; then break; fi
  sleep 1
done
umask 077
tokenfile="${XDG_RUNTIME_DIR:-/tmp}/labrelay-dev-token-$(id -u)"
bin/labrelayd token pilot write > "$tokenfile"
echo "Token saved to $tokenfile; see README for client commands."
exec bin/labrelayd serve
