#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

current_kernel="$(uname -r)"
next_kernel=""

if [ -e /vmlinuz ]; then
  next_kernel="$(basename "$(readlink -f /vmlinuz)")"
  next_kernel="${next_kernel#vmlinuz-}"
fi

apt-get update
apt-get install -y "linux-modules-extra-${current_kernel}"

if [ -n "${next_kernel}" ] && [ "${next_kernel}" != "${current_kernel}" ]; then
  if apt-cache show "linux-modules-extra-${next_kernel}" >/dev/null 2>&1; then
    apt-get install -y "linux-modules-extra-${next_kernel}"
  fi
fi

install -m 0755 /mnt/stash/virtroid/deploy/vps/virtdroid-binderfs-setup.sh /usr/local/sbin/virtdroid-binderfs-setup.sh
install -m 0644 /mnt/stash/virtroid/deploy/vps/redroid-binderfs.service /etc/systemd/system/redroid-binderfs.service

systemctl daemon-reload
systemctl enable --now redroid-binderfs.service

if ! mountpoint -q /dev/binderfs; then
  echo "binderfs did not mount" >&2
  exit 1
fi

install -d -m 0750 -o 1000 -g 1000 /srv/virtdroid /srv/virtdroid/runtimes

ls -la /dev/binderfs
printf 'docker group id: '
getent group docker | cut -d: -f3
