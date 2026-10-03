#!/usr/bin/env bash
set -euo pipefail
[[ "$EUID" == 0 ]] || { echo 'Run as root' >&2; exit 1; }
volume=${1:?supply the Terraform data_volume_id}
[[ "$volume" =~ ^vol-[0-9a-f]+$ ]] || exit 2
device="/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${volume//-/}"
[[ -b "$device" ]] || { echo "Expected attached data volume not found: $device" >&2; exit 1; }
[[ "$(blockdev --getsize64 "$device")" == 32212254720 ]] || { echo 'Expected exactly the 30 GiB data volume' >&2; exit 1; }
if findmnt -rn -S "$device" >/dev/null; then echo 'Volume already mounted; inspect findmnt instead of formatting'; exit 1; fi
kind=$(blkid -s TYPE -o value "$device" || true)
if [[ -z "$kind" ]]; then
  [[ "${2:-}" == --initialize-empty ]] || { echo 'Blank device; explicitly pass --initialize-empty for first use' >&2; exit 1; }
  [[ -z "$(wipefs -n --noheadings "$device")" ]] || { echo 'Device has signatures; refusing to format' >&2; exit 1; }
  mkfs.ext4 "$device"
elif [[ "$kind" != ext4 ]]; then echo "Unexpected existing filesystem $kind" >&2; exit 1; fi
uuid=$(blkid -s UUID -o value "$device")
install -d -m 0750 /srv/labrelay
[[ -z "$(ls -A /srv/labrelay)" ]] || { echo 'Mount directory is not empty; refusing to hide existing data' >&2; exit 1; }
if ! grep -q "UUID=$uuid " /etc/fstab; then printf 'UUID=%s /srv/labrelay ext4 defaults 0 2\n' "$uuid" >> /etc/fstab; fi
mount /srv/labrelay
findmnt /srv/labrelay
