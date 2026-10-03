#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
case "${1:-unit}" in
 unit) bash scripts/go.sh test ./... ;;
 integration) source scripts/local-env.sh; export LABRELAY_INTEGRATION=1; bash scripts/go.sh test -count=1 -v ./internal/server ;;
 race) bash scripts/go.sh test -race ./... ;;
 *) echo 'usage: scripts/test.sh unit|integration|race' >&2; exit 2 ;;
esac
