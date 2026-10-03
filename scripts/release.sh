#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/host-preflight.sh
version=${1:-0.1.0-rc.2}
[[ "$version" =~ ^[0-9A-Za-z.-]+$ ]] || exit 2
mkdir -p dist
for arch in amd64 arm64; do
  dest="dist/labrelay-${version}-linux-${arch}"
  mkdir -p "$dest"
  for binary in labrelay labrelayd; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" bash scripts/go.sh build -trimpath -ldflags="-s -w -X main.version=$version" -o "$dest/$binary" "./cmd/$binary"
  done
  cp README.md "$dest/README.md"
  tar -C dist -czf "$dest.tar.gz" "$(basename "$dest")"
done
(cd dist && sha256sum "labrelay-${version}-linux-"*.tar.gz > "labrelay-${version}-SHA256SUMS")
