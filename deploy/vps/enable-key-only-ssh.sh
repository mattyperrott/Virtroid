#!/bin/bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

deploy_user="${VIRTROID_DEPLOY_USER:-virtroid}"
if [[ ! "${deploy_user}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]]; then
  echo "VIRTROID_DEPLOY_USER must be a simple Linux account name" >&2
  exit 1
fi
if ! id "${deploy_user}" >/dev/null 2>&1; then
  echo "deploy account does not exist: ${deploy_user}" >&2
  exit 1
fi

deploy_home="$(getent passwd "${deploy_user}" | cut -d: -f6)"
authorized_keys="${deploy_home}/.ssh/authorized_keys"
if [ ! -s "${authorized_keys}" ]; then
  echo "deploy account has no authorized SSH key" >&2
  exit 1
fi
if ! ssh-keygen -l -f "${authorized_keys}" >/dev/null; then
  echo "deploy account authorized_keys is invalid" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_config="${VIRTROID_SSHD_KEY_ONLY_CONFIG:-${script_dir}/sshd-key-only.conf}"
if [ ! -f "${source_config}" ] && [ -f /usr/local/share/virtroid/sshd-key-only.conf ]; then
  source_config=/usr/local/share/virtroid/sshd-key-only.conf
fi
if [ ! -f "${source_config}" ]; then
  echo "key-only SSH source configuration is missing" >&2
  exit 1
fi
target_config=/etc/ssh/sshd_config.d/00-virtroid-key-only.conf
rollback_config="$(mktemp /tmp/virtroid-sshd-key-only.XXXXXX)"
had_previous=false
cleanup() {
  if [ -e "${rollback_config}" ]; then
    find "${rollback_config}" -delete
  fi
}
trap cleanup EXIT

if [ -f "${target_config}" ]; then
  cp -p "${target_config}" "${rollback_config}"
  had_previous=true
fi
install -o root -g root -m 0644 "${source_config}" "${target_config}"

rollback() {
  if [ "${had_previous}" = true ]; then
    install -o root -g root -m 0644 "${rollback_config}" "${target_config}"
  else
    find "${target_config}" -delete
  fi
}

if ! sshd -t; then
  rollback
  echo "key-only SSH configuration failed validation and was rolled back" >&2
  exit 1
fi

effective_config="$(sshd -T -C "user=${deploy_user},host=$(hostname),addr=127.0.0.1")"
for expected in \
  'permitrootlogin no' \
  'passwordauthentication no' \
  'pubkeyauthentication yes' \
  'authenticationmethods publickey'; do
  if ! grep -qx "${expected}" <<< "${effective_config}"; then
    rollback
    echo "effective SSH configuration is missing: ${expected}" >&2
    exit 1
  fi
done

if ! systemctl reload ssh; then
  rollback
  systemctl reload ssh || true
  echo "SSH reload failed and the prior configuration was restored" >&2
  exit 1
fi

verify_helper=/usr/local/sbin/virtroid-verify-host-hardening
if [ ! -x "${verify_helper}" ]; then
  rollback
  systemctl reload ssh || true
  echo "host hardening verifier is missing: ${verify_helper}" >&2
  exit 1
fi
if ! VIRTROID_REQUIRE_ROOT_LOCKED=true \
     VIRTROID_REQUIRE_KEY_ONLY_SSH=true \
     "${verify_helper}"; then
  rollback
  systemctl reload ssh || true
  echo "host hardening verification failed and SSH configuration was rolled back" >&2
  exit 1
fi

echo "key-only SSH enabled; verify a fresh deploy-user login before closing the current root session"
