#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

export DEBIAN_FRONTEND="${DEBIAN_FRONTEND:-noninteractive}"

current_kernel="$(uname -r)"
next_kernel=""

if [ -e /vmlinuz ]; then
  next_kernel="$(basename "$(readlink -f /vmlinuz)")"
  next_kernel="${next_kernel#vmlinuz-}"
fi

apt-get update
apt-get install -y \
  adb \
  ca-certificates \
  curl \
  docker-compose-v2 \
  docker.io \
  gnupg \
  python3 \
  rsync \
  "linux-modules-extra-${current_kernel}"

if [ -n "${next_kernel}" ] && [ "${next_kernel}" != "${current_kernel}" ]; then
  if apt-cache show "linux-modules-extra-${next_kernel}" >/dev/null 2>&1; then
    apt-get install -y "linux-modules-extra-${next_kernel}"
  fi
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

install -m 0755 "${script_dir}/virtroid-binderfs-setup.sh" /usr/local/sbin/virtroid-binderfs-setup.sh
install -m 0644 "${script_dir}/redroid-binderfs.service" /etc/systemd/system/redroid-binderfs.service

systemctl daemon-reload
systemctl enable --now docker
systemctl enable --now redroid-binderfs.service

if ! mountpoint -q /dev/binderfs; then
  echo "binderfs did not mount" >&2
  exit 1
fi

host_uid="${VIRTROID_HOST_UID:-${SUDO_UID:-1000}}"
host_gid="${VIRTROID_HOST_GID:-${SUDO_GID:-1000}}"

install -d -m 0750 -o "${host_uid}" -g "${host_gid}" \
  /srv/virtroid \
  /srv/virtroid/apks \
  /srv/virtroid/assets \
  /srv/virtroid/runtimes \
  /srv/virtroid/tls
chown -R "${host_uid}:${host_gid}" /srv/virtroid

ls -la /dev/binderfs
printf 'docker group id: '
getent group docker | cut -d: -f3
