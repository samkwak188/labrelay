#!/usr/bin/env bash
# Source from a test entry point. AWS mode must preserve the caller's credentials.
case "${1:-local}" in
  local)
    unset AWS_SESSION_TOKEN AWS_ENDPOINT_URL AWS_ENDPOINT_URL_S3 AWS_IGNORE_CONFIGURED_ENDPOINT_URLS
    source scripts/local-env.sh
    export LABRELAY_TEST_BACKEND=local
    ;;
  aws)
    : "${S3_BUCKET:?dedicated private versioned acceptance bucket required}"
    : "${DATABASE_URL:?isolated PostgreSQL database required}"
    : "${AWS_REGION:?required}"
    if [[ -n "${S3_ENDPOINT:-}${AWS_ENDPOINT_URL:-}${AWS_ENDPOINT_URL_S3:-}" ]]; then
      echo 'AWS acceptance must use real S3, not a custom endpoint' >&2
      return 2
    fi
    export AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true
    export LABRELAY_TEST_BACKEND=aws
    ;;
  *)
    echo 'usage: test backend must be local|aws' >&2
    return 2
    ;;
esac
