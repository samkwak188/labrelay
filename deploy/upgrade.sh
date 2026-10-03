#!/usr/bin/env bash
set -euo pipefail
cd /opt/labrelay
exec 9>/run/lock/labrelay-deploy.lock
flock -n 9
set -a; source .env; set +a
[[ "$LABRELAY_IMAGE" =~ @sha256:[0-9a-f]{64}$ ]] || { echo 'Image must be pinned by digest' >&2; exit 1; }
mountpoint -q /srv/labrelay || { echo 'Persistent data volume is not mounted' >&2; exit 1; }
install -d -m 0700 -o 65532 -g 65532 /srv/labrelay/uploads
docker compose pull
old=$(docker compose images -q app 2>/dev/null || true)
[[ -z "$old" ]] || printf '%s\n' "$old" > previous-image-id
docker compose stop app
docker compose up -d --wait postgres
docker compose run --rm --no-deps app migrate
docker compose up -d --no-deps app
for i in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:8080/health/ready; then exit 0; fi
  sleep 2
done
echo 'Readiness failed. Service remains inspectable; use the rollback runbook.' >&2
exit 1
