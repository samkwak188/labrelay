#!/usr/bin/env bash
set -euo pipefail
# WSL ext4 free space does not reveal exhaustion of its Windows backing disk.
if grep -qi microsoft /proc/version && [[ -d /mnt/c ]]; then
  available_kib=$(df -Pk /mnt/c | awk 'NR==2 {print $4}')
  if (( available_kib < 12 * 1024 * 1024 )); then
    echo 'At least 12 GiB free on Windows C: is required before local builds/benchmarks. WSL virtual free space is not sufficient.' >&2
    exit 1
  fi
fi
available_kib=$(df -Pk "${TMPDIR:-/tmp}" | awk 'NR==2 {print $4}')
if (( available_kib < 2 * 1024 * 1024 )); then
  echo 'At least 2 GiB free Linux temporary space is required.' >&2
  exit 1
fi
