#!/usr/bin/env bash
set -euo pipefail
if [[ -x /tmp/labrelay-tools/go/bin/go ]]; then
  export PATH="/tmp/labrelay-tools/go/bin:$PATH"
  export GOMODCACHE=/tmp/labrelay-tools/mod GOCACHE=/tmp/labrelay-tools/cache
fi
exec go "$@"
