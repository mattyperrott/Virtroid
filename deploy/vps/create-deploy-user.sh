#!/bin/bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

deploy_user="${VIRTROID_DEPLOY_USER:-virtroid}"
project_root="${VIRTROID_PROJECT_ROOT:-/opt/virtroid}"
data_root="${VIRTROID_DATA_ROOT:-/srv/virtroid}"
authorized_key_file="${VIRTROID_AUTHORIZED_KEY_FILE:-}"

if [[ ! "${deploy_user}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]]; then
  echo "VIRTROID_DEPLOY_USER must be a simple Linux account name" >&2
  exit 1
fi
if [ ! -d "${project_root}/deploy/vps" ]; then
  echo "Virtroid checkout is missing: ${project_root}" >&2
  exit 1
fi
if ! getent group docker >/dev/null 2>&1; then
  echo "docker group is missing; prepare the host first" >&2
  exit 1
fi
if ! getent group virtroid-cert >/dev/null 2>&1; then
  echo "virtroid-cert group is missing; prepare the host first" >&2
  exit 1
fi

if ! id "${deploy_user}" >/dev/null 2>&1; then
  if getent group "${deploy_user}" >/dev/null 2>&1; then
    useradd --create-home --shell /bin/bash --gid "${deploy_user}" "${deploy_user}"
  else
    useradd --create-home --shell /bin/bash --user-group "${deploy_user}"
  fi
fi

deploy_uid="$(id -u "${deploy_user}")"
if [ "${deploy_uid}" -eq 0 ] || [ "${deploy_uid}" -lt 1000 ]; then
  echo "refusing deploy account with privileged/system uid: ${deploy_uid}" >&2
  exit 1
fi
deploy_group="$(id -gn "${deploy_user}")"
deploy_home="$(getent passwd "${deploy_user}" | cut -d: -f6)"
if [ -z "${deploy_home}" ] || [ "${deploy_home}" = /root ]; then
  echo "deploy account has an unsafe home directory" >&2
  exit 1
fi

usermod --append --groups docker,virtroid-cert "${deploy_user}"
usermod --shell /bin/bash "${deploy_user}"
passwd --lock "${deploy_user}" >/dev/null
install -d -o "${deploy_user}" -g "${deploy_group}" -m 0750 "${deploy_home}"
install -d -o "${deploy_user}" -g "${deploy_group}" -m 0700 "${deploy_home}/.ssh"

if [ -n "${authorized_key_file}" ]; then
  if [ ! -f "${authorized_key_file}" ]; then
    echo "authorized key file is missing: ${authorized_key_file}" >&2
    exit 1
  fi
  key_count="$(awk 'NF && $1 !~ /^#/ { count++ } END { print count + 0 }' "${authorized_key_file}")"
  if [ "${key_count}" -ne 1 ]; then
    echo "authorized key file must contain exactly one public key" >&2
    exit 1
  fi
  if ! ssh-keygen -l -f "${authorized_key_file}" >/dev/null; then
    echo "authorized key file is not a valid SSH public key" >&2
    exit 1
  fi
  install -o "${deploy_user}" -g "${deploy_group}" -m 0600 \
    "${authorized_key_file}" "${deploy_home}/.ssh/authorized_keys"
fi

sudoers_path=/etc/sudoers.d/90-virtroid-deploy
sudoers_temp="$(mktemp /etc/sudoers.d/.90-virtroid-deploy.XXXXXX)"
cleanup() {
  if [ -e "${sudoers_temp}" ]; then
    find "${sudoers_temp}" -delete
  fi
}
trap cleanup EXIT
printf '%s ALL=(ALL:ALL) NOPASSWD: ALL\n' "${deploy_user}" > "${sudoers_temp}"
chmod 0440 "${sudoers_temp}"
visudo -cf "${sudoers_temp}" >/dev/null
mv "${sudoers_temp}" "${sudoers_path}"
trap - EXIT

chown root:root "${project_root}" "${project_root}/deploy" "${project_root}/deploy/vps"
chown -R root:root "${project_root}/deploy/vps"
chmod 0755 "${project_root}" "${project_root}/deploy" "${project_root}/deploy/vps"
chmod -R go-w "${project_root}/deploy/vps"
if [ -f "${project_root}/deploy/vps/.env" ]; then
  chown root:root "${project_root}/deploy/vps/.env"
  chmod 0600 "${project_root}/deploy/vps/.env"
fi

install -d -o "${deploy_user}" -g "${deploy_group}" -m 0750 \
  "${data_root}" \
  "${data_root}/apks" \
  "${data_root}/assets" \
  "${data_root}/runtimes"
install -d -o root -g virtroid-cert -m 0750 "${data_root}/tls"
if [ -f "${data_root}/tls/virtroid.pem" ]; then
  chown root:virtroid-cert "${data_root}/tls/virtroid.pem"
  chmod 0640 "${data_root}/tls/virtroid.pem"
fi

sudo -u "${deploy_user}" -H docker version --format '{{.Server.Version}}' >/dev/null
sudo -u "${deploy_user}" -H sudo -n true

if [ -s "${deploy_home}/.ssh/authorized_keys" ]; then
  passwd --lock root >/dev/null
fi

printf 'deploy user ready: %s uid=%s groups=%s\n' \
  "${deploy_user}" "${deploy_uid}" "$(id -Gn "${deploy_user}")"
if [ ! -s "${deploy_home}/.ssh/authorized_keys" ]; then
  echo "warning: no SSH public key installed; do not disable current SSH access" >&2
fi
