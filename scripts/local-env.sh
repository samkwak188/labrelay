#!/usr/bin/env bash
# Source from Linux. These credentials only apply to the loopback development services.
export DATABASE_URL='postgres://labrelay:local-development-only@127.0.0.1:15432/labrelay?sslmode=disable'
export AWS_ACCESS_KEY_ID=labrelay-local
export AWS_SECRET_ACCESS_KEY=local-development-only
export AWS_REGION=us-east-2
export AWS_EC2_METADATA_DISABLED=true
export S3_BUCKET=labrelay-local
export S3_ENDPOINT=http://127.0.0.1:18333
export LABRELAY_SERVER=http://127.0.0.1:18080
export LABRELAY_LISTEN=127.0.0.1:18080
export LABRELAY_PROJECT=pilot
export LABRELAY_SPOOL="${LABRELAY_SPOOL:-$HOME/.local/state/labrelay}"
