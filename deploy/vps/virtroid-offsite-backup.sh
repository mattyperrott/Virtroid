#!/bin/bash
set -euo pipefail

PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
unset BASH_ENV CDPATH ENV IFS
umask 0077

test_mode="${VIRTROID_RESTIC_TEST_MODE:-0}"
shopt -s nullglob
if [ "$(id -u)" -ne 0 ] && [ "${test_mode}" != 1 ]; then
  echo "run as root" >&2
  exit 1
fi

config_file="${VIRTROID_RESTIC_ENV_FILE:-/etc/virtroid/restic.env}"
backup_root=/var/backups/virtroid
restore_root=/var/lib/virtroid-restic
restic_binary=/usr/bin/restic
flock_binary=/usr/bin/flock
getent_binary=/usr/bin/getent
hostname_binary=/bin/hostname
maintenance_root=/var/lib/virtroid-deploy
expected_uid=0
if [ "${test_mode}" = 1 ]; then
  backup_root="${VIRTROID_RESTIC_TEST_BACKUP_ROOT:?set VIRTROID_RESTIC_TEST_BACKUP_ROOT}"
  restore_root="${VIRTROID_RESTIC_TEST_RESTORE_ROOT:?set VIRTROID_RESTIC_TEST_RESTORE_ROOT}"
  restic_binary="${VIRTROID_RESTIC_TEST_BINARY:?set VIRTROID_RESTIC_TEST_BINARY}"
  flock_binary="${VIRTROID_RESTIC_TEST_FLOCK:?set VIRTROID_RESTIC_TEST_FLOCK}"
  getent_binary="${VIRTROID_RESTIC_TEST_GETENT:?set VIRTROID_RESTIC_TEST_GETENT}"
  hostname_binary="${VIRTROID_RESTIC_TEST_HOSTNAME:?set VIRTROID_RESTIC_TEST_HOSTNAME}"
  maintenance_root="${VIRTROID_RESTIC_TEST_MAINTENANCE_ROOT:-${restore_root}/maintenance}"
  expected_uid="$(id -u)"
fi

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

cleanup_stale_work_paths() {
  local candidate
  local candidate_name
  local candidate_kind

  [ -d "${restore_root}" ] || return 0
  for candidate in \
    "${restore_root}"/restore-check.* \
    "${restore_root}"/tree-validation.* \
    "${restore_root}"/tree-list.*; do
    [ -e "${candidate}" ] || [ -L "${candidate}" ] || continue
    candidate_name="$(basename "${candidate}")"
    case "${candidate_name}" in
      restore-check.*|tree-validation.*)
        [[ "${candidate_name}" =~ ^(restore-check|tree-validation)\.[A-Za-z0-9]+$ ]] ||
          die "unexpected stale work path: ${candidate}"
        [ -d "${candidate}" ] && [ ! -L "${candidate}" ] ||
          die "stale work path is not a real directory: ${candidate}"
        candidate_kind=directory
        ;;
      tree-list.*)
        [[ "${candidate_name}" =~ ^tree-list\.[A-Za-z0-9]+$ ]] ||
          die "unexpected stale work path: ${candidate}"
        [ -f "${candidate}" ] && [ ! -L "${candidate}" ] ||
          die "stale work path is not a regular non-symlink file: ${candidate}"
        candidate_kind=file
        ;;
      *)
        die "unexpected stale work path: ${candidate}"
        ;;
    esac
    [ "$(file_uid "${candidate}")" = "${expected_uid}" ] ||
      die "stale work path has the wrong owner: ${candidate}"
    require_private_path "${candidate}" "stale work ${candidate_kind}"
    if [ "${candidate_kind}" = directory ]; then
      find "${candidate}" -xdev -depth -delete ||
        die "could not remove stale work directory: ${candidate}"
    else
      find "${candidate}" -xdev -depth -delete ||
        die "could not remove stale work file: ${candidate}"
      [ ! -e "${candidate}" ] && [ ! -L "${candidate}" ] ||
        die "could not remove stale work file: ${candidate}"
    fi
  done
}

file_uid() {
  stat -c '%u' -- "$1" 2>/dev/null || stat -f '%u' "$1"
}

file_mode() {
  stat -c '%a' -- "$1" 2>/dev/null || stat -f '%Lp' "$1"
}

file_mtime() {
  stat -c '%Y' -- "$1" 2>/dev/null || stat -f '%m' "$1"
}

require_secret_file() {
  local path="$1"
  local label="$2"
  local mode
  local parent

  [ "${path#/}" != "${path}" ] || die "${label} path must be absolute"
  [ -f "${path}" ] || die "${label} is not a regular file"
  [ ! -L "${path}" ] || die "${label} must not be a symlink"
  [ "$(file_uid "${path}")" = "${expected_uid}" ] || die "${label} has the wrong owner"
  mode="$(file_mode "${path}")"
  case "${mode}" in
    400|600) ;;
    *) die "${label} mode must be 0400 or 0600" ;;
  esac
  parent="$(dirname "${path}")"
  [ -d "${parent}" ] || die "${label} parent directory is missing"
  require_private_path "${parent}" "${label} parent directory"
}

require_private_path() {
  local path="$1"
  local label="$2"
  local mode
  local permissions

  [ ! -L "${path}" ] || die "${label} must not be a symlink"
  [ "$(file_uid "${path}")" = "${expected_uid}" ] || die "${label} has the wrong owner"
  mode="$(file_mode "${path}")"
  permissions="${mode: -3}"
  [[ "${permissions}" =~ ^[0-7]{3}$ ]] || die "could not validate ${label} mode"
  [ "${permissions:1:1}" = 0 ] && [ "${permissions:2:1}" = 0 ] ||
    die "${label} must not be accessible by group or other users"
}

for state_directory in "${restore_root}" "${maintenance_root}"; do
  if [ -e "${state_directory}" ] || [ -L "${state_directory}" ]; then
    [ -d "${state_directory}" ] && [ ! -L "${state_directory}" ] ||
      die "Restic state path must be a real directory: ${state_directory}"
  elif [ "${test_mode}" = 1 ]; then
    install -d -m 0700 "${state_directory}"
  else
    install -d -o root -g root -m 0700 "${state_directory}"
  fi
  require_private_path "${state_directory}" "Restic state directory"
done
[ -x "${flock_binary}" ] || die "flock is not installed"
[ -x "${getent_binary}" ] || die "getent is not installed"
[ -x "${hostname_binary}" ] || die "hostname is not installed"
maintenance_lock="${maintenance_root}/maintenance.lock"
exec 8>"${maintenance_lock}"
chmod 0600 "${maintenance_lock}"
if ! "${flock_binary}" -n 8; then
  echo "another Virtroid maintenance operation is already running" >&2
  exit 75
fi
if [ "${test_mode}" != 1 ]; then
  [ -r "/proc/$$/fd/255" ] || die "cannot verify the executing off-site backup helper descriptor"
  executing_helper_digest="$(sha256sum "/proc/$$/fd/255" | awk '{print $1}')"
  installed_helper_digest="$(sha256sum /usr/local/sbin/virtroid-offsite-backup.sh | awk '{print $1}')"
  [ "${executing_helper_digest}" = "${installed_helper_digest}" ] || {
    echo "off-site backup helper changed while this process was starting" >&2
    exit 75
  }
fi
operation_lock="${restore_root}/operation.lock"
if [ -e "${operation_lock}" ] || [ -L "${operation_lock}" ]; then
  [ -f "${operation_lock}" ] && [ ! -L "${operation_lock}" ] ||
    die "Restic operation lock must be a regular non-symlink file"
  [ "$(file_uid "${operation_lock}")" = "${expected_uid}" ] ||
    die "Restic operation lock has the wrong owner"
fi
exec 9>"${operation_lock}"
chmod 0600 "${operation_lock}"
if ! "${flock_binary}" -n 9; then
  echo "another Virtroid off-site operation is already running" >&2
  exit 75
fi

cleanup_stale_work_paths

require_trusted_file() {
  local path="$1"
  local label="$2"
  local mode
  local permissions
  local parent

  [ "${path#/}" != "${path}" ] || die "${label} path must be absolute"
  [ -f "${path}" ] || die "${label} is not a regular file"
  [ ! -L "${path}" ] || die "${label} must not be a symlink"
  [ "$(file_uid "${path}")" = "${expected_uid}" ] || die "${label} has the wrong owner"
  mode="$(file_mode "${path}")"
  permissions="${mode: -3}"
  [[ "${permissions}" =~ ^[0-7]{3}$ ]] || die "could not validate ${label} mode"
  (( (8#${permissions} & 0022) == 0 )) || die "${label} must not be group/world writable"
  parent="$(dirname "${path}")"
  require_private_path "${parent}" "${label} parent directory"
}

allowed_key() {
  case "$1" in
    RESTIC_REPOSITORY|RESTIC_PASSWORD_FILE|RESTIC_PASSWORD|RESTIC_PASSWORD_COMMAND|\
      RESTIC_CACERT|RESTIC_TLS_CLIENT_CERT|\
      RESTIC_REST_USERNAME|RESTIC_REST_PASSWORD|\
      AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN|AWS_DEFAULT_REGION|AWS_REGION|\
      B2_ACCOUNT_ID|B2_ACCOUNT_KEY|\
      AZURE_ACCOUNT_NAME|AZURE_ACCOUNT_KEY|AZURE_ACCOUNT_SAS|AZURE_ENDPOINT_SUFFIX|\
      GOOGLE_PROJECT_ID|GOOGLE_APPLICATION_CREDENTIALS|\
      OS_AUTH_URL|OS_REGION_NAME|OS_USERNAME|OS_USER_ID|OS_PASSWORD|\
      OS_USER_DOMAIN_NAME|OS_USER_DOMAIN_ID|OS_PROJECT_NAME|OS_PROJECT_ID|\
      OS_PROJECT_DOMAIN_NAME|OS_PROJECT_DOMAIN_ID|OS_TRUST_ID|\
      OS_APPLICATION_CREDENTIAL_ID|OS_APPLICATION_CREDENTIAL_SECRET|\
      OS_APPLICATION_CREDENTIAL_NAME|OS_STORAGE_URL|OS_AUTH_TOKEN|\
      VIRTROID_RESTIC_BACKUP_HOST|VIRTROID_RESTIC_TAG|\
      VIRTROID_RESTIC_KEEP_DAILY|VIRTROID_RESTIC_KEEP_WEEKLY|\
      VIRTROID_RESTIC_KEEP_MONTHLY|VIRTROID_RESTIC_KEEP_YEARLY|\
      VIRTROID_RESTIC_CHECK_PERCENT|VIRTROID_RESTIC_MAX_LOCAL_AGE_HOURS|\
      VIRTROID_RESTIC_PRUNE_ENABLED)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

is_restic_key() {
  case "$1" in
    VIRTROID_RESTIC_*) return 1 ;;
    *) return 0 ;;
  esac
}

require_secret_file "${config_file}" "Restic environment file"
config_parent="$(dirname "${config_file}")"
[ -d "${config_parent}" ] || die "Restic configuration directory is missing"
require_private_path "${config_parent}" "Restic configuration directory"

setting_keys=()
setting_values=()

setting_index() {
  local sought="$1"
  local index
  for ((index = 0; index < ${#setting_keys[@]}; index++)); do
    if [ "${setting_keys[${index}]}" = "${sought}" ]; then
      printf '%s\n' "${index}"
      return 0
    fi
  done
  return 1
}

setting_is_set() {
  setting_index "$1" >/dev/null
}

setting_value() {
  local index
  if ! index="$(setting_index "$1")"; then
    return 1
  fi
  printf '%s' "${setting_values[${index}]}"
}

while IFS= read -r line || [ -n "${line}" ]; do
  [[ "${line}" != *$'\r'* ]] || die "Restic environment file must use Unix line endings"
  [[ "${line}" =~ ^[[:space:]]*$ ]] && continue
  [[ "${line}" =~ ^[[:space:]]*# ]] && continue
  [[ "${line}" =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]] ||
    die "Restic environment file contains invalid syntax"
  key="${BASH_REMATCH[1]}"
  value="${BASH_REMATCH[2]}"
  allowed_key "${key}" || die "Restic environment file contains unsupported key: ${key}"
  ! setting_is_set "${key}" || die "Restic environment file contains duplicate key: ${key}"
  setting_keys+=("${key}")
  setting_values+=("${value}")
done < "${config_file}"

! setting_is_set RESTIC_PASSWORD || die "RESTIC_PASSWORD is not allowed; use RESTIC_PASSWORD_FILE"
! setting_is_set RESTIC_PASSWORD_COMMAND ||
  die "RESTIC_PASSWORD_COMMAND is not allowed; use RESTIC_PASSWORD_FILE"

repository="$(setting_value RESTIC_REPOSITORY || true)"
password_file="$(setting_value RESTIC_PASSWORD_FILE || true)"
[ -n "${repository}" ] || die "RESTIC_REPOSITORY is required"
[ -n "${password_file}" ] || die "RESTIC_PASSWORD_FILE is required"
case "${repository}" in
  rest:https://*|s3:*|b2:*|azure:*|gs:*|swift:*|sftp:*) ;;
  *) die "RESTIC_REPOSITORY must use a supported remote backend" ;;
esac
repository_lower="$(printf '%s' "${repository}" | tr '[:upper:]' '[:lower:]')"
if [[ "${repository_lower}" == *http://* ]]; then
  die "unencrypted HTTP repositories are not allowed"
fi

endpoint_host() {
  local endpoint="$1"
  local endpoint_kind=""
  local authority
  local host

  case "${endpoint}" in
    rest:*) endpoint_kind=rest; endpoint="${endpoint#rest:}" ;;
    s3:*) endpoint_kind=s3; endpoint="${endpoint#s3:}" ;;
  esac
  if [[ "${endpoint}" == https://* ]]; then
    authority="${endpoint#https://}"
    authority="${authority%%/*}"
  elif [[ "${endpoint}" == sftp://* ]]; then
    authority="${endpoint#sftp://}"
    authority="${authority%%/*}"
  elif [[ "${endpoint}" == sftp:* ]]; then
    authority="${endpoint#sftp:}"
    authority="${authority%%:*}"
  elif [ "${endpoint_kind}" = s3 ]; then
    authority="${endpoint%%/*}"
  else
    return 1
  fi

  authority="${authority##*@}"
  if [[ "${authority}" == \[*\]* ]]; then
    host="${authority#\[}"
    host="${host%%\]*}"
  else
    host="${authority%%:*}"
  fi
  [ -n "${host}" ] || return 1
  printf '%s\n' "${host}"
}

reject_local_endpoint() {
  local endpoint="$1"
  local label="$2"
  local host
  local host_lower
  local local_short
  local local_fqdn
  local resolved_records
  local resolved_address
  local resolved_address_lower
  local resolved_count
  local local_addresses
  local local_address
  local local_address_lower

  [ -n "${endpoint}" ] || return 0
  host="$(endpoint_host "${endpoint}")" || die "${label} has an invalid remote endpoint"
  host_lower="$(printf '%s' "${host%.}" | tr '[:upper:]' '[:lower:]')"
  local_short="$("${hostname_binary}" -s | tr '[:upper:]' '[:lower:]')" ||
    die "could not determine the local hostname"
  local_fqdn="$("${hostname_binary}" -f 2>/dev/null | tr '[:upper:]' '[:lower:]' || true)"
  local_fqdn="${local_fqdn%.}"
  case "${host_lower}" in
    localhost|*.localhost|127|127.*|0.0.0.0|::1|0:0:0:0:0:0:0:1)
      die "${label} must not target the local host"
      ;;
  esac
  if [ "${host_lower}" = "${local_short}" ] ||
     { [ -n "${local_fqdn}" ] && [ "${host_lower}" = "${local_fqdn}" ]; }; then
    die "${label} must not target the local host"
  fi

  resolved_records="$("${getent_binary}" ahosts "${host}" 2>/dev/null)" ||
    die "${label} host could not be resolved"
  [ -n "${resolved_records}" ] || die "${label} host resolved to no addresses"
  local_addresses="$("${hostname_binary}" -I 2>/dev/null)" ||
    die "could not determine local interface addresses"
  [ -n "${local_addresses}" ] || die "could not determine local interface addresses"

  resolved_count=0
  while read -r resolved_address _; do
    [ -n "${resolved_address}" ] || continue
    resolved_count=$((resolved_count + 1))
    resolved_address="${resolved_address%%\%*}"
    resolved_address_lower="$(printf '%s' "${resolved_address}" | tr '[:upper:]' '[:lower:]')"
    case "${resolved_address_lower}" in
      127.*|169.254.*|0.0.0.0|::|::1|0:0:0:0:0:0:0:1|\
        fe[89ab][0-9a-f]:*|::ffff:127.*|::ffff:169.254.*)
        die "${label} must not resolve to a loopback or link-local address"
        ;;
    esac
    for local_address in ${local_addresses}; do
      local_address="${local_address%%\%*}"
      local_address_lower="$(printf '%s' "${local_address}" | tr '[:upper:]' '[:lower:]')"
      [ "${resolved_address_lower}" != "${local_address_lower}" ] ||
        die "${label} must not resolve to a local interface address"
    done
  done <<< "${resolved_records}"
  [ "${resolved_count}" -gt 0 ] || die "${label} host resolved to no usable addresses"
}

case "${repository}" in
  rest:https://*|sftp:*|s3:*) reject_local_endpoint "${repository}" "Restic repository" ;;
esac
os_auth_url="$(setting_value OS_AUTH_URL || true)"
os_storage_url="$(setting_value OS_STORAGE_URL || true)"
os_auth_url_lower="$(printf '%s' "${os_auth_url}" | tr '[:upper:]' '[:lower:]')"
os_storage_url_lower="$(printf '%s' "${os_storage_url}" | tr '[:upper:]' '[:lower:]')"
if [[ "${os_auth_url_lower}" == http://* ]]; then
  die "unencrypted OpenStack authentication endpoints are not allowed"
fi
if [[ "${os_storage_url_lower}" == http://* ]]; then
  die "unencrypted OpenStack storage endpoints are not allowed"
fi
if [ -n "${os_auth_url}" ]; then
  reject_local_endpoint "${os_auth_url}" "OpenStack authentication endpoint"
fi
if [ -n "${os_storage_url}" ]; then
  reject_local_endpoint "${os_storage_url}" "OpenStack storage endpoint"
fi

require_secret_file "${password_file}" "Restic password file"
awk 'NR == 1 { ok = (length($0) > 0) } NR > 1 { extra = 1 } END { exit !(ok && !extra) }' \
  "${password_file}" || die "Restic password file must contain exactly one non-empty line"

for referenced_secret in \
  RESTIC_TLS_CLIENT_CERT GOOGLE_APPLICATION_CREDENTIALS; do
  referenced_path="$(setting_value "${referenced_secret}" || true)"
  if [ -n "${referenced_path}" ]; then
    require_secret_file "${referenced_path}" "${referenced_secret} file"
  fi
done
ca_cert="$(setting_value RESTIC_CACERT || true)"
if [ -n "${ca_cert}" ]; then
  require_trusted_file "${ca_cert}" "RESTIC_CACERT file"
fi

backup_host="$(setting_value VIRTROID_RESTIC_BACKUP_HOST || true)"
backup_tag="$(setting_value VIRTROID_RESTIC_TAG || true)"
backup_host="${backup_host:-$("${hostname_binary}" -s)}"
backup_tag="${backup_tag:-virtroid-offsite}"
[[ "${backup_host}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die "invalid backup host"
[[ "${backup_tag}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "invalid backup tag"

validate_integer() {
  local name="$1"
  local value="$2"
  local minimum="$3"
  local maximum="$4"
  [[ "${value}" =~ ^[0-9]+$ ]] || die "${name} must be an integer"
  [ "$((10#${value}))" -ge "${minimum}" ] && [ "$((10#${value}))" -le "${maximum}" ] ||
    die "${name} must be between ${minimum} and ${maximum}"
}

keep_daily="$(setting_value VIRTROID_RESTIC_KEEP_DAILY || true)"
keep_weekly="$(setting_value VIRTROID_RESTIC_KEEP_WEEKLY || true)"
keep_monthly="$(setting_value VIRTROID_RESTIC_KEEP_MONTHLY || true)"
keep_yearly="$(setting_value VIRTROID_RESTIC_KEEP_YEARLY || true)"
check_percent="$(setting_value VIRTROID_RESTIC_CHECK_PERCENT || true)"
max_age_hours="$(setting_value VIRTROID_RESTIC_MAX_LOCAL_AGE_HOURS || true)"
prune_enabled="$(setting_value VIRTROID_RESTIC_PRUNE_ENABLED || true)"
keep_daily="${keep_daily:-14}"
keep_weekly="${keep_weekly:-8}"
keep_monthly="${keep_monthly:-18}"
keep_yearly="${keep_yearly:-3}"
check_percent="${check_percent:-5}"
max_age_hours="${max_age_hours:-12}"
prune_enabled="${prune_enabled:-false}"
validate_integer VIRTROID_RESTIC_KEEP_DAILY "${keep_daily}" 1 3650
validate_integer VIRTROID_RESTIC_KEEP_WEEKLY "${keep_weekly}" 1 520
validate_integer VIRTROID_RESTIC_KEEP_MONTHLY "${keep_monthly}" 1 240
validate_integer VIRTROID_RESTIC_KEEP_YEARLY "${keep_yearly}" 1 100
validate_integer VIRTROID_RESTIC_CHECK_PERCENT "${check_percent}" 1 100
validate_integer VIRTROID_RESTIC_MAX_LOCAL_AGE_HOURS "${max_age_hours}" 1 72
case "${prune_enabled}" in
  true|false) ;;
  *) die "VIRTROID_RESTIC_PRUNE_ENABLED must be true or false" ;;
esac

[ "${restic_binary#/}" != "${restic_binary}" ] || die "Restic binary path must be absolute"
[ -x "${restic_binary}" ] || die "Restic is not installed"

restic_environment=(
  "PATH=${PATH}"
  "HOME=/root"
  "LANG=C"
  "LC_ALL=C"
  "RESTIC_CACHE_DIR=/var/cache/virtroid-restic"
)
if [ "${test_mode}" = 1 ]; then
  restic_environment[4]="RESTIC_CACHE_DIR=${restore_root}/cache"
fi
for ((index = 0; index < ${#setting_keys[@]}; index++)); do
  key="${setting_keys[${index}]}"
  if is_restic_key "${key}"; then
    restic_environment+=("${key}=${setting_values[${index}]}")
  fi
done

run_restic() {
  (
    while IFS= read -r inherited_key; do
      unset "${inherited_key}" 2>/dev/null || true
    done < <(compgen -e)
    while IFS= read -r inherited_function; do
      export -n -f "${inherited_function}" 2>/dev/null || true
    done < <(compgen -A function)
    for environment_entry in "${restic_environment[@]}"; do
      export "${environment_entry}"
    done
    exec "${restic_binary}" "$@"
  )
}

check_sha256_manifest() {
  local directory="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "${directory}" && sha256sum --check SHA256SUMS >/dev/null)
  elif command -v shasum >/dev/null 2>&1; then
    (cd "${directory}" && shasum -a 256 --check SHA256SUMS >/dev/null)
  else
    die "no SHA-256 verification tool is installed"
  fi
}

calculate_deployment_tree_digest() {
  local tree_root="$1"
  local unsafe_path listing_file absolute_path relative_path mode kind content_digest
  local -a pipeline_status
  [ -d "${tree_root}" ] && [ ! -L "${tree_root}" ] || return 1
  unsafe_path="$(find "${tree_root}" -xdev -mindepth 1 \
    \( -type l -o ! -type f ! -type d \) -print -quit)" || return 1
  [ -z "${unsafe_path}" ] || return 1
  listing_file="$(mktemp "${restore_root}/tree-list.XXXXXX")" || return 1
  if ! find "${tree_root}" -xdev -mindepth 1 \
    ! -path "${tree_root}/.env" \
    ! -path "${tree_root}/release.env" \
    ! -path "${tree_root}/restic.env" \
    ! -path "${tree_root}/restic-password" \
    -print0 > "${listing_file}"; then
    find "${listing_file}" -delete || true
    return 1
  fi
  [ -s "${listing_file}" ] || {
    find "${listing_file}" -delete || true
    return 1
  }
  sort -z "${listing_file}" |
    while IFS= read -r -d '' absolute_path; do
      relative_path="${absolute_path#"${tree_root}/"}"
      mode="$(file_mode "${absolute_path}")" || exit 1
      if [ -d "${absolute_path}" ]; then
        kind=directory
        content_digest=-
      elif command -v sha256sum >/dev/null 2>&1; then
        kind=file
        content_digest="$(sha256sum -- "${absolute_path}" | awk '{print $1}')" || exit 1
      else
        kind=file
        content_digest="$(shasum -a 256 "${absolute_path}" | awk '{print $1}')" || exit 1
      fi
      printf '%s\0%s\0%s\0%s\0' \
        "${kind}" "${mode}" "${relative_path}" "${content_digest}" || exit 1
    done |
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum | awk '{print $1}'
    else
      shasum -a 256 | awk '{print $1}'
    fi
  pipeline_status=("${PIPESTATUS[@]}")
  find "${listing_file}" -delete || return 1
  for status in "${pipeline_status[@]}"; do
    [ "${status}" -eq 0 ] || return 1
  done
}

validate_backup_set() {
  local directory="$1"
  local item
  local age_seconds
  local now
  local modified

  [ -d "${directory}" ] || die "backup set is missing"
  require_private_path "${directory}" "backup set"
  [[ "$(basename "${directory}")" =~ ^daily-[0-9]{8}T[0-9]{6}Z$ ]] ||
    die "backup set has an unexpected name"
  for item in deploy.env release-current.env deploy-tree.tgz metadata.txt srv-virtroid.tgz virtroid-postgres.dump SHA256SUMS; do
    [ -f "${directory}/${item}" ] || die "backup set is missing ${item}"
    require_private_path "${directory}/${item}" "backup file ${item}"
  done
  check_sha256_manifest "${directory}" || die "backup set checksum verification failed"
  tar -tzf "${directory}/srv-virtroid.tgz" >/dev/null || die "backup archive is unreadable"
  tar -tzf "${directory}/deploy-tree.tgz" >/dev/null || die "deployment-tree archive is unreadable"
  installed_tree_digest="$(awk -F= '$1 == "installed_deployment_tree_sha256" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/metadata.txt")" ||
    die "backup metadata is missing the installed deployment-tree digest"
  release_tree_digest="$(awk -F= '$1 == "release_deployment_tree_sha256" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/metadata.txt")" ||
    die "backup metadata is missing the release deployment-tree digest"
  preinstall_tree_included="$(awk -F= '$1 == "preinstall_deployment_tree_included" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/metadata.txt")" ||
    die "backup metadata is missing the pre-install deployment-tree state"
  [[ "${installed_tree_digest}" =~ ^[0-9a-f]{64}$ ]] ||
    die "backup installed deployment-tree digest is invalid"
  (
    validation_root="$(mktemp -d "${restore_root}/tree-validation.XXXXXX")" || exit 1
    trap 'find "${validation_root}" -xdev -depth -delete' EXIT
    tar --no-same-owner --same-permissions -xzf \
      "${directory}/deploy-tree.tgz" -C "${validation_root}" || exit 1
    actual_tree_digest="$(calculate_deployment_tree_digest "${validation_root}/vps")" || exit 1
    [ "${actual_tree_digest}" = "${installed_tree_digest}" ] || exit 1
  ) || die "installed deployment-tree archive failed trusted digest verification"
  release_state_tree_digest="$(awk -F= '$1 == "VIRTROID_DEPLOYMENT_TREE_SHA256" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/release-current.env")" ||
    die "backup release state is missing its deployment-tree digest"
  [ "${release_state_tree_digest}" = "${release_tree_digest}" ] ||
    die "backup metadata and release state disagree about the running deployment tree"
  case "${preinstall_tree_included}" in
    0|1) ;;
    *) die "backup pre-install deployment-tree state is invalid" ;;
  esac
  if [ "${release_tree_digest}" = legacy-unknown ] ||
     { [[ "${release_tree_digest}" =~ ^[0-9a-f]{64}$ ]] &&
       [ "${release_tree_digest}" != "${installed_tree_digest}" ]; }; then
    [ "${preinstall_tree_included}" = 1 ] ||
      die "backup omits the deployment tree used by its running release"
  elif [[ ! "${release_tree_digest}" =~ ^[0-9a-f]{64}$ ]]; then
    die "backup release deployment-tree digest is invalid"
  fi
  if [ "${preinstall_tree_included}" = 1 ]; then
    [ -f "${directory}/preinstall-deploy-tree.tgz" ] ||
      die "backup is missing its pre-install deployment tree"
    require_private_path "${directory}/preinstall-deploy-tree.tgz" "pre-install deployment-tree archive"
    tar -tzf "${directory}/preinstall-deploy-tree.tgz" >/dev/null ||
      die "pre-install deployment-tree archive is unreadable"
    preinstall_tree_digest="$(awk -F= '$1 == "preinstall_deployment_tree_sha256" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/metadata.txt")" ||
      die "backup metadata is missing the pre-install deployment-tree digest"
    [[ "${preinstall_tree_digest}" =~ ^[0-9a-f]{64}$ ]] ||
      die "backup pre-install deployment-tree digest is invalid"
    archived_tree_digest="$(tar -xOzf "${directory}/preinstall-deploy-tree.tgz" DEPLOYMENT_TREE_SHA256 | tr -d '\n')" ||
      die "could not read the pre-install deployment-tree digest"
    [ "${archived_tree_digest}" = "${preinstall_tree_digest}" ] ||
      die "pre-install deployment-tree archive digest does not match backup metadata"
    if [[ "${release_tree_digest}" =~ ^[0-9a-f]{64}$ ]] &&
       [ "${release_tree_digest}" != "${installed_tree_digest}" ] &&
       [ "${release_tree_digest}" != "${preinstall_tree_digest}" ]; then
      die "pre-install deployment tree does not match the running release"
    fi
    (
      validation_root="$(mktemp -d "${restore_root}/tree-validation.XXXXXX")" || exit 1
      trap 'find "${validation_root}" -xdev -depth -delete' EXIT
      tar --no-same-owner --same-permissions -xzf \
        "${directory}/preinstall-deploy-tree.tgz" -C "${validation_root}" || exit 1
      actual_tree_digest="$(calculate_deployment_tree_digest "${validation_root}/vps")" || exit 1
      [ "${actual_tree_digest}" = "${preinstall_tree_digest}" ] || exit 1
    ) || die "pre-install deployment-tree content failed trusted digest verification"
  fi
  release_image="$(awk -F= '$1 == "VIRTROID_BACKEND_IMAGE" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/release-current.env")" ||
    die "backup release state is invalid"
  release_image_id="$(awk -F= '$1 == "VIRTROID_BACKEND_IMAGE_ID" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/release-current.env")" ||
    die "backup release image ID is invalid"
  release_manifest_digest="$(awk -F= '$1 == "VIRTROID_BACKEND_MANIFEST_DIGEST" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${directory}/release-current.env")" ||
    die "backup release manifest digest is invalid"
  if [[ ! "${release_image}" =~ ^virtroid-backend:release-[0-9a-f]{40}$ ]] ||
     [[ ! "${release_image_id}" =~ ^sha256:[0-9a-f]{64}$ ]] ||
     [[ ! "${release_manifest_digest}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    [ -f "${directory}/legacy-backend-images.tar" ] ||
      die "legacy backup is missing its exact backend image export"
    require_private_path "${directory}/legacy-backend-images.tar" "legacy backend image export"
    tar -tf "${directory}/legacy-backend-images.tar" >/dev/null ||
      die "legacy backend image export is unreadable"
  fi
  blob_store_kind="$(awk -F= '$1 == "NODE_BLOB_STORE_KIND" { found++; value = substr($0, index($0, "=") + 1) } END { if (found > 1) exit 1; print value }' "${directory}/deploy.env")" ||
    die "backup deployment environment contains duplicate blob-store settings"
  if [ "${blob_store_kind:-local-disk}" = sia-renterd ]; then
    [ -f "${directory}/renterd-data.tgz" ] || die "renterd backup is missing renterd-data.tgz"
    require_private_path "${directory}/renterd-data.tgz" "renterd data archive"
    tar -tzf "${directory}/renterd-data.tgz" >/dev/null || die "renterd data archive is unreadable"
  fi
  now="$(date +%s)"
  modified="$(file_mtime "${directory}")"
  age_seconds=$((now - modified))
  [ "${age_seconds}" -ge -300 ] || die "backup set timestamp is unexpectedly in the future"
  [ "${age_seconds}" -le $((10#${max_age_hours} * 3600)) ] || die "latest local backup is stale"
}

latest_local_backup() {
  local candidate
  local latest=""
  for candidate in "${backup_root}"/daily-????????T??????Z; do
    [ -d "${candidate}" ] || continue
    [ ! -L "${candidate}" ] || continue
    if [ -z "${latest}" ] || [[ "${candidate}" > "${latest}" ]]; then
      latest="${candidate}"
    fi
  done
  [ -n "${latest}" ] || die "no completed local backup is available"
  printf '%s\n' "${latest}"
}

apply_retention() {
  run_restic forget \
    --host "${backup_host}" \
    --tag "${backup_tag}" \
    --group-by host,tags \
    --keep-daily "${keep_daily}" \
    --keep-weekly "${keep_weekly}" \
    --keep-monthly "${keep_monthly}" \
    --keep-yearly "${keep_yearly}" \
    --prune
}

verify_database_dump() {
  local dump_file="$1"
  if [ "${test_mode}" = 1 ]; then
    "${VIRTROID_RESTIC_TEST_PG_RESTORE:?set VIRTROID_RESTIC_TEST_PG_RESTORE}" --file=/dev/null "${dump_file}"
  elif [ -x /usr/bin/docker ] &&
    [ "$(/usr/bin/docker inspect -f '{{.State.Running}}' virtroid-postgres 2>/dev/null)" = true ]; then
    /usr/bin/docker exec -i virtroid-postgres pg_restore --file=/dev/null < "${dump_file}"
  elif [ -x /usr/bin/pg_restore ]; then
    /usr/bin/pg_restore --file=/dev/null "${dump_file}"
  else
    die "a running virtroid-postgres container or compatible pg_restore is required for the full dump check"
  fi
}

restore_temporary=""
cleanup_restore() {
  if [ -n "${restore_temporary}" ] && [ -d "${restore_temporary}" ]; then
    find "${restore_temporary}" -depth -delete
  fi
}

backup_now() {
  local latest
  latest="$(latest_local_backup)"
  validate_backup_set "${latest}"
  run_restic backup --host "${backup_host}" --tag "${backup_tag}" -- "${latest}"
  if [ "${prune_enabled}" = true ]; then
    apply_retention
  fi
  run_restic check "--read-data-subset=${check_percent}%"
  printf 'Encrypted off-site backup completed: %s\n' "$(basename "${latest}")"
}

restore_check() {
  local restored_root
  local restored_sets
  local restored_set

  install -d -m 0700 "${restore_root}"
  restore_temporary="$(mktemp -d "${restore_root}/restore-check.XXXXXX")"
  trap cleanup_restore EXIT
  run_restic restore latest --host "${backup_host}" --tag "${backup_tag}" --target "${restore_temporary}"
  restored_root="${restore_temporary}${backup_root}"
  [ -d "${restored_root}" ] || die "restored snapshot does not contain the expected backup path"
  restored_sets=("${restored_root}"/daily-????????T??????Z)
  [ "${#restored_sets[@]}" -eq 1 ] || die "restored snapshot must contain exactly one backup set"
  restored_set="${restored_sets[0]}"
  validate_backup_set "${restored_set}"
  verify_database_dump "${restored_set}/virtroid-postgres.dump"
  printf 'Encrypted off-site restore check passed: %s\n' "$(basename "${restored_set}")"
}

case "${1:-backup}" in
  backup)
    backup_now
    ;;
  check)
    run_restic check "--read-data-subset=${check_percent}%"
    ;;
  check-full)
    run_restic check --read-data
    ;;
  init)
    run_restic init
    ;;
  prune)
    apply_retention
    ;;
  restore-check)
    restore_check
    ;;
  snapshots)
    run_restic snapshots --host "${backup_host}" --tag "${backup_tag}"
    ;;
  *)
    die "usage: $0 {backup|check|check-full|init|prune|restore-check|snapshots}"
    ;;
esac
