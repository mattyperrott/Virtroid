#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly trusted_installer=/usr/local/sbin/virtroid-install-reviewed-deployment-tree
readonly target_dir=/opt/virtroid/deploy/vps
readonly target_parent=/opt/virtroid/deploy
readonly state_root=/var/lib/virtroid-deploy
readonly maintenance_lock=${state_root}/maintenance.lock
readonly backup_root=/var/backups/virtroid-deploy-tree

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi
if [ "$(readlink -f -- "$0")" != "${trusted_installer}" ]; then
  echo "run the preinstalled root-owned deployment-tree installer: ${trusted_installer}" >&2
  exit 1
fi
if [ "$#" -ne 2 ]; then
  echo "usage: virtroid-install-reviewed-deployment-tree <staging-directory> <expected-sha256>" >&2
  exit 64
fi

if [ ! -r "/proc/$$/fd/255" ]; then
  echo "cannot verify the executing deployment-tree installer descriptor" >&2
  exit 1
fi
executing_installer_digest="$(/usr/bin/sha256sum "/proc/$$/fd/255" | /usr/bin/awk '{print $1}')"
/usr/bin/install -d -o root -g root -m 0700 "${state_root}"
if [ ! -d "${state_root}" ] || [ -L "${state_root}" ] ||
   [ "$(/usr/bin/stat -c '%u' "${state_root}")" -ne 0 ] ||
   (( (8#$(/usr/bin/stat -c '%a' "${state_root}") & 0022) != 0 )); then
  echo "deployment state directory is unsafe" >&2
  exit 1
fi
exec 8>"${maintenance_lock}"
if ! /usr/bin/flock -n 8; then
  echo "another Virtroid maintenance operation is running" >&2
  exit 75
fi
installed_installer_digest="$(/usr/bin/sha256sum "${trusted_installer}" | /usr/bin/awk '{print $1}')"
if [ "${executing_installer_digest}" != "${installed_installer_digest}" ]; then
  echo "deployment-tree installer changed while this process was starting" >&2
  exit 75
fi

source_dir="$(readlink -f -- "$1")"
expected_digest="$2"
[[ "${expected_digest}" =~ ^[0-9a-f]{64}$ ]] || {
  echo "expected deployment-tree digest must be 64 lowercase hexadecimal characters" >&2
  exit 64
}
[ -d "${source_dir}" ] && [ ! -L "${source_dir}" ] || {
  echo "staging directory must be a real directory" >&2
  exit 1
}
if [ "${source_dir}" = "${target_dir}" ]; then
  echo "staging directory must be separate from the installed production tree" >&2
  exit 1
fi

for command_name in awk chmod chown cmp cp date dirname fail2ban-client find flock install mktemp mv readlink rsync sha256sum sort sshd stat sync systemctl tr; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "missing reviewed-tree installation command: ${command_name}" >&2
    exit 1
  }
done

require_safe_directory() {
  local directory="$1"
  local mode
  [ -d "${directory}" ] && [ ! -L "${directory}" ] || {
    echo "deployment installation directory is unsafe: ${directory}" >&2
    exit 1
  }
  [ "$(stat -c '%u' "${directory}")" -eq 0 ] || {
    echo "deployment installation directory is not root-owned: ${directory}" >&2
    exit 1
  }
  mode="$(stat -c '%a' "${directory}")"
  if (( (8#${mode} & 0022) != 0 )); then
    echo "deployment installation directory is group/world writable: ${directory}" >&2
    exit 1
  fi
}

require_safe_file() {
  local file="$1"
  local mode
  [ -f "${file}" ] && [ ! -L "${file}" ] || {
    echo "trusted deployment installer is not a regular file: ${file}" >&2
    exit 1
  }
  [ "$(stat -c '%u' "${file}")" -eq 0 ] || {
    echo "trusted deployment installer is not root-owned: ${file}" >&2
    exit 1
  }
  mode="$(stat -c '%a' "${file}")"
  if (( (8#${mode} & 0022) != 0 )); then
    echo "trusted deployment installer is group/world writable: ${file}" >&2
    exit 1
  fi
}

calculate_tree_digest() {
  local tree_root="$1"
  local unsafe_path listing_file absolute_path relative_path mode kind content_digest
  [ -d "${tree_root}" ] && [ ! -L "${tree_root}" ] || return 1
  unsafe_path="$(find "${tree_root}" -xdev -mindepth 1 \
    \( -type l -o ! -type f ! -type d \) -print -quit)"
  [ -z "${unsafe_path}" ] || {
    echo "deployment tree contains a symlink or special path: ${unsafe_path}" >&2
    return 1
  }
  listing_file="$(mktemp "${state_root}/tree-list.XXXXXX")"
  find "${tree_root}" -xdev -mindepth 1 \
    ! -path "${tree_root}/.env" \
    ! -path "${tree_root}/release.env" \
    -print0 > "${listing_file}"
  if [ ! -s "${listing_file}" ]; then
    find "${listing_file}" -delete
    echo "deployment tree contains no distributable paths" >&2
    return 1
  fi
  sort -z "${listing_file}" |
    while IFS= read -r -d '' absolute_path; do
      relative_path="${absolute_path#"${tree_root}/"}"
      mode="$(stat -c '%a' "${absolute_path}")"
      if [ -d "${absolute_path}" ]; then
        kind=directory
        content_digest=-
      else
        kind=file
        content_digest="$(sha256sum -- "${absolute_path}" | awk '{print $1}')"
      fi
      printf '%s\0%s\0%s\0%s\0' \
        "${kind}" "${mode}" "${relative_path}" "${content_digest}"
    done |
    sha256sum | awk '{print $1}'
  local pipeline_status=("${PIPESTATUS[@]}")
  find "${listing_file}" -delete
  for status in "${pipeline_status[@]}"; do
    [ "${status}" -eq 0 ] || return 1
  done
}

read_state_value() {
  local input_file="$1"
  local wanted_key="$2"
  awk -v wanted="${wanted_key}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      separator = index($0, "=")
      if (separator == 0) next
      key = substr($0, 1, separator - 1)
      if (key == wanted) {
        found++
        value = substr($0, separator + 1)
      }
    }
    END {
      if (found != 1) exit 1
      print value
    }
  ' "${input_file}"
}

runtime_sources=(
  virtroid-backup.sh
  configure-renterd-secrets.sh
  renterd-admin.sh
  renterd-smoke-test.sh
  derive-node-fingerprint.sh
  virtroid-binderfs-setup.sh
  create-deploy-user.sh
  enable-key-only-ssh.sh
  install-reviewed-deployment-tree.sh
  apply-local-release.sh
  release-on-vps.sh
  sshd-key-only.conf
  redroid-binderfs.service
  virtroid-backup.service
  virtroid-backup.timer
  fail2ban-virtroid.conf
  sshd-virtroid-hardening.conf
  certbot-deploy-hook.sh
)
runtime_destinations=(
  /usr/local/sbin/virtroid-backup.sh
  /usr/local/sbin/virtroid-configure-renterd-secrets
  /usr/local/sbin/virtroid-renterd-admin
  /usr/local/sbin/virtroid-renterd-smoke-test
  /usr/local/sbin/virtroid-derive-node-fingerprint
  /usr/local/sbin/virtroid-binderfs-setup.sh
  /usr/local/sbin/virtroid-create-deploy-user.sh
  /usr/local/sbin/virtroid-enable-key-only-ssh.sh
  /usr/local/sbin/virtroid-install-reviewed-deployment-tree
  /usr/local/sbin/virtroid-apply-local-release
  /usr/local/sbin/virtroid-release-on-vps
  /usr/local/share/virtroid/sshd-key-only.conf
  /etc/systemd/system/redroid-binderfs.service
  /etc/systemd/system/virtroid-backup.service
  /etc/systemd/system/virtroid-backup.timer
  /etc/fail2ban/jail.d/virtroid.conf
  /etc/ssh/sshd_config.d/60-virtroid-hardening.conf
  /etc/letsencrypt/renewal-hooks/deploy/virtroid-haproxy.sh
)
runtime_modes=(
  0755 0755 0755 0755 0755 0755 0755 0755 0755 0755 0755
  0644
  0644 0644 0644 0644 0644
  0755
)

retired_runtime_destinations=(
  /usr/local/sbin/virtroid-offsite-backup.sh
  /usr/local/share/virtroid/restic.env.example
  /etc/systemd/system/virtroid-offsite-backup.service
  /etc/systemd/system/virtroid-offsite-backup.timer
  /etc/systemd/system/virtroid-offsite-restore-check.service
  /etc/systemd/system/virtroid-offsite-restore-check.timer
)
retired_units=(
  virtroid-offsite-backup.service
  virtroid-offsite-backup.timer
  virtroid-offsite-restore-check.service
  virtroid-offsite-restore-check.timer
)
managed_runtime_destinations=(
  "${runtime_destinations[@]}"
  "${retired_runtime_destinations[@]}"
)

if [ "${#runtime_sources[@]}" -ne "${#runtime_destinations[@]}" ] ||
   [ "${#runtime_sources[@]}" -ne "${#runtime_modes[@]}" ]; then
  echo "reviewed runtime installation arrays are inconsistent" >&2
  exit 1
fi

required_tree_files=(
  .env.example README.md apply-local-release.sh build-local-release.sh
  certbot-deploy-hook.sh configure-renterd-secrets.sh create-deploy-user.sh
  deploy.sh deployment-tree-digest.sh
  derive-node-fingerprint.sh docker-compose.yml enable-key-only-ssh.sh
  fail2ban-virtroid.conf generate-env.sh haproxy.cfg
  install-reviewed-deployment-tree.sh prepare-redroid-host.sh
  release-on-vps.sh renterd-admin.sh renterd-mysql-init.sql
  renterd-smoke-test.sh
  redroid-binderfs.service release.env.example renterd.md
  sshd-key-only.conf sshd-virtroid-hardening.conf test-compose-config.sh
  test-env-safety.sh test-node-fingerprint.sh
  test-certbot-deploy-hook.sh
  virtroid-backup.service virtroid-backup.sh
  virtroid-backup.timer virtroid-binderfs-setup.sh
)

atomic_install_file() {
  local source_file="$1"
  local destination="$2"
  local mode="$3"
  local destination_dir temporary
  destination_dir="$(dirname "${destination}")"
  temporary="$(mktemp "${destination_dir}/.virtroid-install.XXXXXX")"
  install -o root -g root -m "${mode}" "${source_file}" "${temporary}"
  mv -Tf "${temporary}" "${destination}"
}

install_runtime_copies() {
  local index retired_destination retired_unit
  for ((index = 0; index < ${#runtime_sources[@]}; index++)); do
    atomic_install_file \
      "${target_dir}/${runtime_sources[index]}" \
      "${runtime_destinations[index]}" \
      "${runtime_modes[index]}"
  done
  for retired_unit in "${retired_units[@]}"; do
    systemctl disable --now "${retired_unit}" >/dev/null 2>&1 || true
  done
  for retired_destination in "${retired_runtime_destinations[@]}"; do
    if [ -e "${retired_destination}" ] || [ -L "${retired_destination}" ]; then
      find "${retired_destination}" -maxdepth 0 -delete
    fi
  done
  systemctl daemon-reload
  sshd -t
  fail2ban-client -t >/dev/null
  systemctl reload ssh
  systemctl reload fail2ban
}

verify_runtime_copies() {
  local index protected_unit retired_destination
  for ((index = 0; index < ${#runtime_sources[@]}; index++)); do
    cmp -s \
      "${target_dir}/${runtime_sources[index]}" \
      "${runtime_destinations[index]}" || {
      echo "installed runtime copy is stale: ${runtime_destinations[index]}" >&2
      return 1
    }
  done
  for retired_destination in "${retired_runtime_destinations[@]}"; do
    if [ -e "${retired_destination}" ] || [ -L "${retired_destination}" ]; then
      echo "retired runtime integration still exists: ${retired_destination}" >&2
      return 1
    fi
  done
  for protected_unit in \
    virtroid-backup.service \
    virtroid-backup.timer \
    redroid-binderfs.service; do
    if [ -n "$(systemctl show --property=DropInPaths --value "${protected_unit}")" ]; then
      echo "${protected_unit} must not have unreviewed systemd drop-ins" >&2
      return 1
    fi
  done
}

restore_runtime_copies() {
  local index destination relative backup_file temporary
  local restore_status=0
  for ((index = 0; index < ${#managed_runtime_destinations[@]}; index++)); do
    destination="${managed_runtime_destinations[index]}"
    relative="${destination#/}"
    backup_file="${backup_dir}/system/${relative}"
    if [ -f "${backup_file}" ] && [ ! -L "${backup_file}" ]; then
      temporary=
      if temporary="$(mktemp "$(dirname "${destination}")/.virtroid-restore.XXXXXX")"; then
        if ! cp -a "${backup_file}" "${temporary}" ||
           ! mv -Tf "${temporary}" "${destination}"; then
          restore_status=1
          if [ -e "${temporary}" ]; then
            find "${temporary}" -maxdepth 0 -delete || restore_status=1
          fi
        fi
      else
        restore_status=1
      fi
    elif [ -e "${destination}" ] || [ -L "${destination}" ]; then
      find "${destination}" -maxdepth 0 -delete || restore_status=1
    fi
  done
  systemctl daemon-reload || restore_status=1
  sshd -t || restore_status=1
  systemctl reload ssh || restore_status=1
  fail2ban-client -t >/dev/null || restore_status=1
  systemctl reload fail2ban || restore_status=1
  return "${restore_status}"
}

require_safe_file "${trusted_installer}"
for safe_parent in /opt /opt/virtroid "${target_parent}" /usr/local /usr/local/sbin /usr/local/share /etc /etc/systemd /etc/systemd/system /etc/ssh /etc/ssh/sshd_config.d; do
  require_safe_directory "${safe_parent}"
done
install -d -o root -g root -m 0700 /etc/virtroid /etc/virtroid/secrets
require_safe_directory /etc/virtroid
require_safe_directory /etc/virtroid/secrets
if [ -e /etc/virtroid/secrets/renterd-api-password ] ||
   [ -L /etc/virtroid/secrets/renterd-api-password ]; then
  require_safe_file /etc/virtroid/secrets/renterd-api-password
  case "$(stat -c '%a' /etc/virtroid/secrets/renterd-api-password)" in
    400|600) ;;
    *) echo "renterd API password file has an unsafe mode" >&2; exit 1 ;;
  esac
else
  install -o root -g root -m 0400 /dev/null /etc/virtroid/secrets/renterd-api-password
fi
[ -d "${target_dir}" ] && [ ! -L "${target_dir}" ] || {
  echo "production deployment tree is missing or is a symlink" >&2
  exit 1
}
require_safe_directory "${target_dir}"
[ -f "${target_dir}/.env" ] && [ ! -L "${target_dir}/.env" ] || {
  echo "root-only production environment is missing: ${target_dir}/.env" >&2
  exit 1
}
[ "$(stat -c '%u' "${target_dir}/.env")" -eq 0 ] || {
  echo "production environment must be root-owned" >&2
  exit 1
}
case "$(stat -c '%a' "${target_dir}/.env")" in
  400|600) ;;
  *)
    echo "production environment must have mode 0400 or 0600" >&2
    exit 1
    ;;
esac

if [ -e "${backup_root}" ] || [ -L "${backup_root}" ]; then
  require_safe_directory "${backup_root}"
else
  install -d -o root -g root -m 0700 "${backup_root}"
fi
require_safe_directory "${backup_root}"

for destination_dir in \
  /usr/local/share/virtroid \
  /etc/fail2ban /etc/fail2ban/jail.d \
  /etc/letsencrypt /etc/letsencrypt/renewal-hooks /etc/letsencrypt/renewal-hooks/deploy; do
  install -d -o root -g root -m 0755 "${destination_dir}"
  require_safe_directory "${destination_dir}"
done

run_id="$(date -u +%Y%m%dT%H%M%SZ)"
snapshot_dir="$(mktemp -d "${state_root}/staged-tree.XXXXXX")"
candidate_dir="$(mktemp -d "${target_parent}/.vps.candidate.XXXXXX")"
old_swap="${target_parent}/.vps.previous.${run_id}.$$"
failed_swap="${target_parent}/.vps.failed.${run_id}.$$"
backup_dir="$(mktemp -d "${backup_root}/${run_id}.XXXXXX")"
tree_switched=0
installation_complete=0
preinstall_state_file="${state_root}/preinstall-tree.env"
preinstall_state_existed=0
state_marker_changed=0
preserve_preinstall_state=0
state_temp=

cleanup_installation() {
  local original_status="$?"
  trap - EXIT HUP INT TERM
  set +e
  if [ "${tree_switched}" -eq 1 ] && [ "${installation_complete}" -ne 1 ] && [ -d "${old_swap}" ]; then
    if [ -d "${target_dir}" ]; then
      mv -T "${target_dir}" "${failed_swap}"
    fi
    if mv -T "${old_swap}" "${target_dir}"; then
      restore_runtime_copies || echo "failed to restore one or more prior runtime copies" >&2
    else
      echo "failed to restore the prior production deployment tree" >&2
    fi
  fi
  if [ "${state_marker_changed}" -eq 1 ] && [ "${installation_complete}" -ne 1 ]; then
    if [ "${preinstall_state_existed}" -eq 1 ] &&
       [ -f "${backup_dir}/state/preinstall-tree.env" ]; then
      atomic_install_file \
        "${backup_dir}/state/preinstall-tree.env" \
        "${preinstall_state_file}" 0600 ||
        echo "failed to restore the prior pre-install tree marker" >&2
    elif [ -e "${preinstall_state_file}" ] || [ -L "${preinstall_state_file}" ]; then
      find "${preinstall_state_file}" -maxdepth 0 -delete ||
        echo "failed to remove the uncommitted pre-install tree marker" >&2
    fi
  fi
  for temporary_path in "${snapshot_dir}" "${candidate_dir}" "${failed_swap}" "${state_temp}"; do
    if [ -n "${temporary_path}" ] && [ -e "${temporary_path}" ]; then
      find "${temporary_path}" -depth -delete
    fi
  done
  exit "${original_status}"
}
trap cleanup_installation EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# The unprivileged staging directory is copied once, without crossing nested
# mounts or importing secret files. Only this root-owned private snapshot is
# verified and subsequently consumed.
rsync -a --one-file-system --delete \
  --exclude=/.env \
  --exclude=/release.env \
  "${source_dir}/" "${snapshot_dir}/"
snapshot_digest="$(calculate_tree_digest "${snapshot_dir}")"
if [ "${snapshot_digest}" != "${expected_digest}" ]; then
  echo "root-private staging snapshot does not match the independently reviewed digest" >&2
  exit 1
fi
for required_file in "${required_tree_files[@]}"; do
  [ -f "${snapshot_dir}/${required_file}" ] && [ ! -L "${snapshot_dir}/${required_file}" ] || {
    echo "reviewed deployment bundle is missing ${required_file}" >&2
    exit 1
  }
done

rsync -a --one-file-system --delete "${snapshot_dir}/" "${candidate_dir}/"
install -o root -g root -m 0600 "${target_dir}/.env" "${candidate_dir}/.env"
chown -R root:root "${candidate_dir}"
chmod -R go-w "${candidate_dir}"
candidate_digest="$(calculate_tree_digest "${candidate_dir}")"
if [ "${candidate_digest}" != "${expected_digest}" ]; then
  echo "prepared production tree failed trusted digest verification" >&2
  exit 1
fi

old_tree_digest="$(calculate_tree_digest "${target_dir}")"
if [ -e "${preinstall_state_file}" ] || [ -L "${preinstall_state_file}" ]; then
  [ -f "${preinstall_state_file}" ] && [ ! -L "${preinstall_state_file}" ] || {
    echo "pre-install deployment-tree marker is unsafe" >&2
    exit 1
  }
  [ "$(stat -c '%u' "${preinstall_state_file}")" -eq 0 ] || {
    echo "pre-install deployment-tree marker is not root-owned" >&2
    exit 1
  }
  case "$(stat -c '%a' "${preinstall_state_file}")" in
    400|600) ;;
    *)
      echo "pre-install deployment-tree marker has an unsafe mode" >&2
      exit 1
      ;;
  esac
  prior_marker_path="$(read_state_value "${preinstall_state_file}" VIRTROID_PREINSTALL_TREE_PATH)"
  prior_marker_tree="$(read_state_value "${preinstall_state_file}" VIRTROID_PREINSTALL_TREE_SHA256)"
  prior_marker_installed="$(read_state_value "${preinstall_state_file}" VIRTROID_INSTALLED_TREE_SHA256)"
  [[ "${prior_marker_tree}" =~ ^[0-9a-f]{64}$ ]] &&
    [[ "${prior_marker_installed}" =~ ^[0-9a-f]{64}$ ]] || {
    echo "pre-install deployment-tree marker contains invalid digests" >&2
    exit 1
  }
  case "${prior_marker_path}" in
    "${backup_root}"/[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z.*) ;;
    *)
      echo "pre-install deployment-tree marker points outside the protected backup root" >&2
      exit 1
      ;;
  esac
  [ -d "${prior_marker_path}/vps" ] && [ ! -L "${prior_marker_path}" ] &&
    [ -f "${prior_marker_path}/DEPLOYMENT_TREE_SHA256" ] || {
    echo "pre-install deployment-tree marker target is incomplete" >&2
    exit 1
  }
  recorded_prior_marker_tree="$(tr -d '\n' < "${prior_marker_path}/DEPLOYMENT_TREE_SHA256")"
  actual_prior_marker_tree="$(calculate_tree_digest "${prior_marker_path}/vps")"
  if [ "${recorded_prior_marker_tree}" != "${prior_marker_tree}" ] ||
     [ "${actual_prior_marker_tree}" != "${prior_marker_tree}" ]; then
    echo "pre-install deployment-tree marker target failed digest verification" >&2
    exit 1
  fi

  running_release_tree=
  if [ -e "${state_root}/current.env" ] || [ -L "${state_root}/current.env" ]; then
    running_release_file="$(readlink -f "${state_root}/current.env")"
    case "${running_release_file}" in
      "${state_root}"/releases/*.env) ;;
      *)
        echo "current release state points outside the protected releases directory" >&2
        exit 1
        ;;
    esac
    running_release_tree="$(read_state_value "${running_release_file}" VIRTROID_DEPLOYMENT_TREE_SHA256)"
  fi
  if [ "${running_release_tree}" != "${old_tree_digest}" ]; then
    if [ "${prior_marker_installed}" != "${old_tree_digest}" ] ||
       [ "${expected_digest}" != "${old_tree_digest}" ]; then
      echo "an earlier deployment-tree transition is still awaiting a protected release; refusing to overwrite its recovery lineage" >&2
      exit 75
    fi
    preserve_preinstall_state=1
  fi
fi

install -d -o root -g root -m 0700 "${backup_dir}/vps" "${backup_dir}/system" "${backup_dir}/state"
if [ -f "${preinstall_state_file}" ] && [ ! -L "${preinstall_state_file}" ]; then
  cp -a "${preinstall_state_file}" "${backup_dir}/state/preinstall-tree.env"
  preinstall_state_existed=1
fi
rsync -a --one-file-system "${target_dir}/" "${backup_dir}/vps/"
for ((index = 0; index < ${#managed_runtime_destinations[@]}; index++)); do
  destination="${managed_runtime_destinations[index]}"
  if [ -f "${destination}" ] && [ ! -L "${destination}" ]; then
    install -d -o root -g root -m 0700 "${backup_dir}/system/$(dirname "${destination#/}")"
    cp -a "${destination}" "${backup_dir}/system/${destination#/}"
  elif [ -e "${destination}" ] || [ -L "${destination}" ]; then
    echo "managed runtime destination is not a regular file: ${destination}" >&2
    exit 1
  fi
done
printf '%s\n' "${old_tree_digest}" > "${backup_dir}/DEPLOYMENT_TREE_SHA256"
chmod 0600 "${backup_dir}/DEPLOYMENT_TREE_SHA256"
sync

mv -T "${target_dir}" "${old_swap}"
if ! mv -T "${candidate_dir}" "${target_dir}"; then
  mv -T "${old_swap}" "${target_dir}"
  echo "failed to atomically activate the reviewed deployment tree" >&2
  exit 1
fi
candidate_dir=
tree_switched=1

install_runtime_copies
installed_digest="$(calculate_tree_digest "${target_dir}")"
if [ "${installed_digest}" != "${expected_digest}" ]; then
  echo "installed deployment tree failed trusted post-install verification" >&2
  exit 1
fi
verify_runtime_copies

if [ "${preserve_preinstall_state}" -ne 1 ]; then
  state_temp="$(mktemp "${state_root}/preinstall-tree.XXXXXX")"
  printf 'VIRTROID_PREINSTALL_TREE_PATH=%s\nVIRTROID_PREINSTALL_TREE_SHA256=%s\nVIRTROID_INSTALLED_TREE_SHA256=%s\n' \
    "${backup_dir}" "${old_tree_digest}" "${installed_digest}" > "${state_temp}"
  chmod 0600 "${state_temp}"
  state_marker_changed=1
  mv -Tf "${state_temp}" "${preinstall_state_file}"
  state_temp=
fi
sync

installation_complete=1
if [ -d "${old_swap}" ]; then
  find "${old_swap}" -depth -delete
fi
sync
tree_switched=0
trap - EXIT HUP INT TERM
find "${snapshot_dir}" -depth -delete

printf 'reviewed deployment tree installed digest=%s prior_tree_backup=%s\n' \
  "${installed_digest}" \
  "$(read_state_value "${preinstall_state_file}" VIRTROID_PREINSTALL_TREE_PATH)"
