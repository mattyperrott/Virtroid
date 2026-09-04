#!/bin/bash
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

packages=(
  adb
  apparmor
  apparmor-utils
  auditd
  audispd-plugins
  ca-certificates
  certbot
  curl
  docker-compose-v2
  docker.io
  fail2ban
  git
  gnupg
  openssl
  python3
  rsync
  ufw
  unattended-upgrades
  "linux-modules-extra-${current_kernel}"
)

apt-get update
apt-get install -y "${packages[@]}"

if [ -n "${next_kernel}" ] && [ "${next_kernel}" != "${current_kernel}" ]; then
  if apt-cache show "linux-modules-extra-${next_kernel}" >/dev/null 2>&1; then
    apt-get install -y "linux-modules-extra-${next_kernel}"
  fi
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

install -m 0755 "${script_dir}/virtroid-binderfs-setup.sh" /usr/local/sbin/virtroid-binderfs-setup.sh
install -m 0644 "${script_dir}/redroid-binderfs.service" /etc/systemd/system/redroid-binderfs.service
install -m 0644 "${script_dir}/fail2ban-virtroid.conf" /etc/fail2ban/jail.d/virtroid.conf
install -m 0640 "${script_dir}/audit-virtroid.rules" /etc/audit/rules.d/99-virtroid-hardening.rules
install -m 0644 "${script_dir}/sshd-virtroid-hardening.conf" /etc/ssh/sshd_config.d/60-virtroid-hardening.conf
install -m 0644 "${script_dir}/sysctl-virtroid-hardening.conf" /etc/sysctl.d/99-virtroid-hardening.conf
install -m 0755 "${script_dir}/configure-renterd-secrets.sh" /usr/local/sbin/virtroid-configure-renterd-secrets
install -m 0755 "${script_dir}/renterd-admin.sh" /usr/local/sbin/virtroid-renterd-admin
install -m 0755 "${script_dir}/renterd-smoke-test.sh" /usr/local/sbin/virtroid-renterd-smoke-test
install -m 0755 "${script_dir}/derive-node-fingerprint.sh" /usr/local/sbin/virtroid-derive-node-fingerprint
install -m 0755 "${script_dir}/create-deploy-user.sh" /usr/local/sbin/virtroid-create-deploy-user.sh
install -m 0755 "${script_dir}/enable-key-only-ssh.sh" /usr/local/sbin/virtroid-enable-key-only-ssh.sh
install -m 0755 "${script_dir}/configure-host-firewall.sh" /usr/local/sbin/virtroid-configure-host-firewall
install -m 0755 "${script_dir}/verify-host-hardening.sh" /usr/local/sbin/virtroid-verify-host-hardening
install -m 0755 "${script_dir}/install-reviewed-deployment-tree.sh" /usr/local/sbin/virtroid-install-reviewed-deployment-tree
install -m 0755 "${script_dir}/apply-local-release.sh" /usr/local/sbin/virtroid-apply-local-release
install -m 0755 "${script_dir}/release-on-vps.sh" /usr/local/sbin/virtroid-release-on-vps
install -d -o root -g root -m 0755 /usr/local/share/virtroid
install -d -o root -g root -m 0700 /var/lib/virtroid-deploy
install -d -o root -g root -m 0700 /etc/virtroid /etc/virtroid/secrets
if [ ! -e /etc/virtroid/secrets/renterd-api-password ]; then
  install -o root -g root -m 0400 /dev/null /etc/virtroid/secrets/renterd-api-password
fi
install -d -o root -g root -m 0700 /var/lib/virtroid-build /var/lib/virtroid-release-bundles
install -d -o root -g root -m 0755 /etc/letsencrypt/renewal-hooks/deploy
install -m 0755 "${script_dir}/certbot-deploy-hook.sh" /etc/letsencrypt/renewal-hooks/deploy/virtroid-haproxy.sh
install -m 0644 "${script_dir}/sshd-key-only.conf" /usr/local/share/virtroid/sshd-key-only.conf
systemctl disable --now virtroid-backup.timer virtroid-backup.service >/dev/null 2>&1 || true
for retired_backup_path in \
  /usr/local/sbin/virtroid-backup.sh \
  /etc/systemd/system/virtroid-backup.service \
  /etc/systemd/system/virtroid-backup.timer; do
  if [ -e "${retired_backup_path}" ] || [ -L "${retired_backup_path}" ]; then
    find "${retired_backup_path}" -maxdepth 0 -delete
  fi
done
install -m 0644 "${script_dir}/unattended-upgrades-virtroid.conf" \
  /etc/apt/apt.conf.d/52virtroid-unattended-upgrades
systemctl daemon-reload
systemctl enable --now apparmor
systemctl enable --now docker
systemctl enable --now redroid-binderfs.service
sysctl --system >/dev/null
systemctl enable --now auditd
augenrules --load
sshd -t
systemctl reload ssh
systemctl enable --now fail2ban
systemctl enable --now ufw
systemctl enable --now unattended-upgrades
systemctl enable --now apt-daily.timer apt-daily-upgrade.timer
/usr/local/sbin/virtroid-configure-host-firewall

if ! mountpoint -q /dev/binderfs; then
  echo "binderfs did not mount" >&2
  exit 1
fi

host_uid="${VIRTROID_HOST_UID:-${SUDO_UID:-1000}}"
host_gid="${VIRTROID_HOST_GID:-${SUDO_GID:-1000}}"

if ! getent group virtroid-cert >/dev/null 2>&1; then
  groupadd --system virtroid-cert
fi
haproxy_cert_gid="$(getent group virtroid-cert | cut -d: -f3)"

install -d -m 0750 -o "${host_uid}" -g "${host_gid}" \
  /srv/virtroid \
  /srv/virtroid/apks \
  /srv/virtroid/assets \
  /srv/virtroid/runtimes \
  /srv/virtroid/tls
chown root:"${haproxy_cert_gid}" /srv/virtroid/tls
chmod 0750 /srv/virtroid/tls
if [ -f /srv/virtroid/tls/virtroid.pem ]; then
  chown root:"${haproxy_cert_gid}" /srv/virtroid/tls/virtroid.pem
  chmod 0640 /srv/virtroid/tls/virtroid.pem
fi

ls -la /dev/binderfs
printf 'docker group id: '
getent group docker | cut -d: -f3
printf 'HAProxy certificate group id: '
printf '%s\n' "${haproxy_cert_gid}"

/usr/local/sbin/virtroid-verify-host-hardening
