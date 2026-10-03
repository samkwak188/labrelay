#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/host-preflight.sh
if [[ -x /tmp/labrelay-tools/go/bin/gofmt ]]; then export PATH="/tmp/labrelay-tools/go/bin:$PATH"; fi
unformatted=$(gofmt -l cmd internal tools)
if [[ -n "$unformatted" ]]; then printf '%s\n' "$unformatted"; exit 1; fi
bash scripts/go.sh vet ./...
bash scripts/go.sh test ./...
bash scripts/go.sh test -race ./...
bash scripts/go.sh test ./internal/manifest -run='^$' -fuzz=FuzzManifest -fuzztime=3s -parallel=2
bash scripts/go.sh test ./internal/manifest -run='^$' -fuzz=FuzzPath -fuzztime=3s -parallel=2
