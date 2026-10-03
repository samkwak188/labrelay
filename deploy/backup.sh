#!/usr/bin/env bash
set -euo pipefail
cd /opt/labrelay
set -a; source .env; set +a
umask 077
tmp=$(mktemp /srv/labrelay/catalog-backup.XXXXXX)
trap 'rm -f -- "$tmp"' EXIT
docker compose exec -T postgres pg_dump -U labrelay -d labrelay -Fc > "$tmp"
test -s "$tmp"
key="catalog/$(date -u +%Y%m%dT%H%M%SZ).dump"
aws s3 cp "$tmp" "s3://$BACKUP_BUCKET/$key" --only-show-errors --sse AES256
printf '%s\n' "$key"
