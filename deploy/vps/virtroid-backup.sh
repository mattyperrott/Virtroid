#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly backup_root=/var/backups/virtroid
readonly deploy_env=/opt/virtroid/deploy/vps/.env
readonly release_state_root=/var/lib/virtroid-deploy
readonly maintenance_lock=${release_state_root}/maintenance.lock
readonly preinstall_backup_root=/var/backups/virtroid-deploy-tree
retention_days="${VIRTROID_BACKUP_RETENTION_DAYS:-7}"
if [[ ! "${retention_days}" =~ ^[0-9]+$ ]] ||
  [ "${retention_days}" -lt 1 ] || [ "${retention_days}" -gt 365 ]; then
  echo "VIRTROID_BACKUP_RETENTION_DAYS must be between 1 and 365" >&2
  exit 1
fi

for command_name in awk curl cut date df docker du find flock install mktemp mv readlink seq sha256sum sleep sort stat sync tail tar tr; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "missing backup command: ${command_name}" >&2
    exit 1
  }
done

install -d -o root -g root -m 0700 "${backup_root}" "${release_state_root}"
if [ "${VIRTROID_MAINTENANCE_LOCK_HELD:-0}" = 1 ]; then
  inherited_lock="$(readlink -f "/proc/$$/fd/8" 2>/dev/null || true)"
  if [ "${inherited_lock}" != "${maintenance_lock}" ] || ! flock -n 8; then
    echo "inherited Virtroid maintenance lock is missing or invalid" >&2
    exit 1
  fi
elif [ "${VIRTROID_MAINTENANCE_LOCK_HELD:-0}" = 0 ]; then
  exec 8>"${maintenance_lock}"
  if ! flock -n 8; then
    echo "another Virtroid maintenance operation is running" >&2
    exit 75
  fi
else
  echo "VIRTROID_MAINTENANCE_LOCK_HELD must be 0 or 1" >&2
  exit 1
fi
if [ ! -r "/proc/$$/fd/255" ]; then
  echo "cannot verify the executing local-backup helper descriptor" >&2
  exit 1
fi
executing_helper_digest="$(/usr/bin/sha256sum "/proc/$$/fd/255" | /usr/bin/awk '{print $1}')"
installed_helper_digest="$(/usr/bin/sha256sum /usr/local/sbin/virtroid-backup.sh | /usr/bin/awk '{print $1}')"
if [ "${executing_helper_digest}" != "${installed_helper_digest}" ]; then
  echo "local-backup helper changed while this process was starting" >&2
  exit 75
fi
exec 9>"${backup_root}/.backup.lock"
if ! flock -n 9; then
  echo "another Virtroid backup is already running" >&2
  exit 1
fi

run_id="$(date -u +%Y%m%dT%H%M%SZ)"
partial_dir="$(mktemp -d "${backup_root}/.partial-${run_id}.XXXXXX")"
final_dir="${backup_root}/daily-${run_id}"
edge_was_running=0
control_was_running=0
node_was_running=0
renterd_was_running=0
containers_resumed=0
legacy_release=0
legacy_image_bytes=0
legacy_image_ids=()
legacy_mapping_containers=()
legacy_mapping_ids=()
legacy_mapping_refs=()

container_is_running() {
  [ "$(docker inspect --format '{{.State.Running}}' "$1" 2>/dev/null)" = true ]
}

start_if_needed() {
  local container_name="$1"
  local was_running="$2"
  if [ "${was_running}" -eq 1 ] && ! container_is_running "${container_name}"; then
    docker start "${container_name}" >/dev/null
  fi
}

wait_for_control_plane() {
  local attempt response
  if [ "${control_was_running}" -ne 1 ]; then
    return 0
  fi
  for attempt in $(seq 1 60); do
    if response="$(curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8080/healthz 2>/dev/null)" &&
       [[ "${response}" =~ \"ok\"[[:space:]]*:[[:space:]]*true ]]; then
      return 0
    fi
    sleep 1
  done
  echo "control plane did not recover after the backup" >&2
  return 1
}

wait_for_runtime_node() {
  local attempt response
  if [ "${node_was_running}" -ne 1 ]; then
    return 0
  fi
  for attempt in $(seq 1 60); do
    if container_is_running virtnoded &&
       response="$(curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8090/healthz 2>/dev/null)" &&
       [[ "${response}" =~ \"ok\"[[:space:]]*:[[:space:]]*true ]]; then
      return 0
    fi
    sleep 1
  done
  echo "runtime node did not recover after the backup" >&2
  return 1
}

wait_for_edge() {
  local attempt response
  if [ "${edge_was_running}" -ne 1 ]; then
    return 0
  fi
  for attempt in $(seq 1 30); do
    if container_is_running virtroid-edge &&
       response="$(curl --noproxy '*' --proto '=https' --tlsv1.2 \
         --resolve "${public_hostname}:${public_port}:127.0.0.1" \
         -fsS --connect-timeout 2 --max-time 10 "${public_base_url%/}/healthz" 2>/dev/null)" &&
       [[ "${response}" =~ \"ok\"[[:space:]]*:[[:space:]]*true ]]; then
      return 0
    fi
    sleep 1
  done
  echo "public HTTPS edge did not recover after the backup" >&2
  return 1
}

resume_containers() {
  if [ "${containers_resumed}" -eq 1 ]; then
    return 0
  fi
  start_if_needed virtroidd "${control_was_running}" || return 1
  wait_for_control_plane || return 1
  if docker inspect virtroid-renterd >/dev/null 2>&1; then
    start_if_needed virtroid-renterd "${renterd_was_running}" || return 1
    if [ "${renterd_was_running}" -eq 1 ] && ! container_is_running virtroid-renterd; then
      echo "renterd did not recover after the backup" >&2
      return 1
    fi
  fi
  start_if_needed virtnoded "${node_was_running}" || return 1
  wait_for_runtime_node || return 1
  start_if_needed virtroid-edge "${edge_was_running}" || return 1
  wait_for_edge || return 1
  containers_resumed=1
}

cleanup() {
  local cleanup_status=0
  if [ -d "${partial_dir}" ]; then
    find "${partial_dir}" -depth -delete || cleanup_status=1
  fi
  resume_containers || cleanup_status=1
  return "${cleanup_status}"
}
trap cleanup EXIT

active_session_count() {
  docker exec virtroid-postgres \
    psql -U "${postgres_user}" -d "${postgres_database}" -Atc \
    "SELECT COUNT(*)
       FROM sessions
      WHERE status IN ('pending', 'active')
        AND expires_at > NOW();"
}

validate_zero_sessions() {
  local active_sessions
  active_sessions="$(active_session_count)"
  if [[ ! "${active_sessions}" =~ ^[0-9]+$ ]] || [ "${active_sessions}" -ne 0 ]; then
    echo "backup refused while pending or active sessions exist: ${active_sessions}" >&2
    return 75
  fi
}

running_runtime_guest_count() {
  local container_id
  local data_source
  local running_containers
  local count=0

  running_containers="$(docker ps --quiet)" || {
    echo "could not enumerate running Docker containers" >&2
    return 1
  }
  while IFS= read -r container_id; do
    [ -n "${container_id}" ] || continue
    data_source="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' "${container_id}")"
    case "${data_source}" in
      /srv/virtroid/runtimes/*) count=$((count + 1)) ;;
    esac
  done <<< "${running_containers}"
  printf '%s\n' "${count}"
}

validate_no_running_runtime_guests() {
  local guest_count
  guest_count="$(running_runtime_guest_count)"
  if [[ ! "${guest_count}" =~ ^[0-9]+$ ]] || [ "${guest_count}" -ne 0 ]; then
    echo "backup refused while managed Android runtime containers are running: ${guest_count}" >&2
    return 75
  fi
}

require_container() {
  docker inspect "$1" >/dev/null 2>&1 || {
    echo "required backup container is missing: $1" >&2
    exit 1
  }
}

for container_name in virtroid-postgres virtroidd virtnoded virtroid-edge; do
  require_container "${container_name}"
done
if ! container_is_running virtroid-postgres; then
  echo "virtroid-postgres is not running" >&2
  exit 1
fi
if [ ! -f "${deploy_env}" ] || [ -L "${deploy_env}" ]; then
  echo "deployment environment is not a regular readable file" >&2
  exit 1
fi
if [ "$(stat -c '%u' "${deploy_env}")" -ne 0 ]; then
  echo "deployment environment must be root-owned" >&2
  exit 1
fi
case "$(stat -c '%a' "${deploy_env}")" in
  400|600) ;;
  *)
    echo "deployment environment must have mode 0400 or 0600" >&2
    exit 1
    ;;
esac

read_file_value() {
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
      if (found > 1) exit 1
      if (found == 1) print value
    }
  ' "${input_file}"
}

read_deploy_value() {
  local wanted_key="$1"
  read_file_value "${deploy_env}" "${wanted_key}"
}

validate_infrastructure_container() {
  local container_name="$1"
  local image_key="$2"
  local expected_image configured_image image_id deployment_tree

  expected_image="$(read_deploy_value "${image_key}")"
  [ -n "${expected_image}" ] || {
    echo "deployment environment is missing ${image_key}" >&2
    return 1
  }
  configured_image="$(docker inspect --format '{{.Config.Image}}' "${container_name}")"
  image_id="$(docker inspect --format '{{.Image}}' "${container_name}")"
  deployment_tree="$(docker inspect --format '{{index .Config.Labels "com.virtroid.deployment-tree-sha256"}}' "${container_name}")"
  if [ "${configured_image}" != "${expected_image}" ] ||
     [[ ! "${image_id}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "deployment environment does not match infrastructure container ${container_name}" >&2
    return 1
  fi
  if [ "${legacy_release}" -ne 1 ] &&
     [ "${deployment_tree}" != "${current_release_tree_digest}" ]; then
    echo "protected deployment tree does not match infrastructure container ${container_name}" >&2
    return 1
  fi
}

validate_container_release_state() {
  local container_name="$1"
  local configured_image image_id source_sha schema_version deployment_tree

  configured_image="$(docker inspect --format '{{.Config.Image}}' "${container_name}")"
  image_id="$(docker inspect --format '{{.Image}}' "${container_name}")"
  source_sha="$(docker inspect --format '{{index .Config.Labels "com.virtroid.source-sha"}}' "${container_name}")"
  schema_version="$(docker inspect --format '{{index .Config.Labels "com.virtroid.schema-version"}}' "${container_name}")"
  deployment_tree="$(docker inspect --format '{{index .Config.Labels "com.virtroid.deployment-tree-sha256"}}' "${container_name}")"
  if [ "${configured_image}" != "${current_release_image}" ] ||
     { [ "${image_id}" != "${current_release_image_id}" ] &&
       [ "${image_id}" != "${current_release_manifest_digest}" ]; } ||
     [ "${source_sha}" != "${current_release_source_sha}" ] ||
     [ "${schema_version}" != "${current_release_schema}" ] ||
     [ "${deployment_tree}" != "${current_release_tree_digest}" ]; then
    echo "protected release state does not match running container ${container_name}" >&2
    return 1
  fi
}

postgres_user="$(read_deploy_value POSTGRES_USER)"
postgres_database="$(read_deploy_value POSTGRES_DB)"
public_base_url="$(read_deploy_value PUBLIC_BASE_URL)"
postgres_user="${postgres_user:-virtroid}"
postgres_database="${postgres_database:-virtroid}"
[[ "${postgres_user}" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || {
  echo "POSTGRES_USER is invalid" >&2
  exit 1
}
[[ "${postgres_database}" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || {
  echo "POSTGRES_DB is invalid" >&2
  exit 1
}
if [[ ! "${public_base_url}" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:[0-9]{1,5})?$ ]]; then
  echo "PUBLIC_BASE_URL is not an approved HTTPS origin" >&2
  exit 1
fi
public_authority="${public_base_url#https://}"
public_hostname="${public_authority%%:*}"
if [[ "${public_authority}" == *:* ]]; then
  public_port="${public_authority##*:}"
else
  public_port=443
fi
if [ "$((10#${public_port}))" -lt 1 ] || [ "$((10#${public_port}))" -gt 65535 ]; then
  echo "PUBLIC_BASE_URL contains an invalid port" >&2
  exit 1
fi

installed_tree_digest="$(/opt/virtroid/deploy/vps/deployment-tree-digest.sh /opt/virtroid/deploy/vps)"
[[ "${installed_tree_digest}" =~ ^[0-9a-f]{64}$ ]] || {
  echo "could not determine the installed deployment-tree digest" >&2
  exit 1
}
current_release_tree_digest=
current_release_image=
current_release_image_id=
current_release_manifest_digest=
current_release_source_sha=
current_release_schema=
if [ -e "${release_state_root}/current.env" ] || [ -L "${release_state_root}/current.env" ]; then
  current_release_path="$(readlink -f "${release_state_root}/current.env")"
  case "${current_release_path}" in
    "${release_state_root}"/releases/*.env) ;;
    *)
      echo "current release state points outside the protected releases directory" >&2
      exit 1
      ;;
  esac
  [ -f "${current_release_path}" ] && [ ! -L "${current_release_path}" ] || {
    echo "current release state target is not a regular file" >&2
    exit 1
  }
  [ "$(stat -c '%u' "${current_release_path}")" -eq 0 ] || {
    echo "current release state must be root-owned" >&2
    exit 1
  }
  case "$(stat -c '%a' "${current_release_path}")" in
    400|600) ;;
    *)
      echo "current release state must have mode 0400 or 0600" >&2
      exit 1
      ;;
  esac
  current_release_image="$(read_file_value "${current_release_path}" VIRTROID_BACKEND_IMAGE)"
  current_release_image_id="$(read_file_value "${current_release_path}" VIRTROID_BACKEND_IMAGE_ID)"
  current_release_manifest_digest="$(read_file_value "${current_release_path}" VIRTROID_BACKEND_MANIFEST_DIGEST)"
  current_release_source_sha="$(read_file_value "${current_release_path}" VIRTROID_SOURCE_SHA)"
  current_release_schema="$(read_file_value "${current_release_path}" VIRTROID_SCHEMA_VERSION)"
  current_release_tree_digest="$(read_file_value "${current_release_path}" VIRTROID_DEPLOYMENT_TREE_SHA256)"
  [[ "${current_release_image}" =~ ^virtroid-backend:release-([0-9a-f]{40})$ ]] &&
    [ "${BASH_REMATCH[1]}" = "${current_release_source_sha}" ] &&
    [[ "${current_release_image_id}" =~ ^sha256:[0-9a-f]{64}$ ]] &&
    [[ "${current_release_manifest_digest}" =~ ^sha256:[0-9a-f]{64}$ ]] &&
    [[ "${current_release_source_sha}" =~ ^[0-9a-f]{40}$ ]] &&
    [[ "${current_release_schema}" =~ ^[0-9]+$ ]] &&
    [ "${current_release_schema}" -gt 0 ] &&
    [[ "${current_release_tree_digest}" =~ ^[0-9a-f]{64}$ ]] || {
      echo "current release state contains an invalid immutable identity" >&2
      exit 1
    }
  validate_container_release_state virtroidd
  validate_container_release_state virtnoded
  if docker inspect virtroid-falco-forwarder >/dev/null 2>&1; then
    validate_container_release_state virtroid-falco-forwarder
  fi
else
  legacy_release=1
  legacy_container_tree_digest="$(docker inspect --format '{{index .Config.Labels "com.virtroid.deployment-tree-sha256"}}' virtroidd)"
  if [[ "${legacy_container_tree_digest}" =~ ^[0-9a-f]{64}$ ]]; then
    current_release_tree_digest="${legacy_container_tree_digest}"
  else
    current_release_tree_digest=legacy-unknown
  fi
fi

blob_store_kind="$(read_deploy_value NODE_BLOB_STORE_KIND)"
blob_store_kind="${blob_store_kind:-local-disk}"
case "${blob_store_kind}" in
  local-disk|sia-renterd) ;;
  *)
    echo "deployment environment contains an unsupported blob-store kind" >&2
    exit 1
    ;;
esac
validate_infrastructure_container virtroid-postgres POSTGRES_IMAGE
validate_infrastructure_container virtroid-edge HAPROXY_IMAGE
if [ "${blob_store_kind}" = sia-renterd ]; then
  require_container virtroid-renterd
fi
if docker inspect virtroid-renterd >/dev/null 2>&1; then
  validate_infrastructure_container virtroid-renterd RENTERD_IMAGE
fi
if docker inspect virtroid-falco >/dev/null 2>&1; then
  validate_infrastructure_container virtroid-falco FALCO_IMAGE
fi

prior_tree_required=0
if [ "${legacy_release}" -eq 1 ] ||
   [ "${current_release_tree_digest}" != "${installed_tree_digest}" ]; then
  prior_tree_required=1
fi
preinstall_tree_path=
preinstall_tree_digest=
if [ "${prior_tree_required}" -eq 1 ]; then
  preinstall_state="${release_state_root}/preinstall-tree.env"
  [ -f "${preinstall_state}" ] && [ ! -L "${preinstall_state}" ] || {
    echo "pre-install deployment-tree recovery state is missing" >&2
    exit 1
  }
  [ "$(stat -c '%u' "${preinstall_state}")" -eq 0 ] || {
    echo "pre-install deployment-tree recovery state must be root-owned" >&2
    exit 1
  }
  case "$(stat -c '%a' "${preinstall_state}")" in
    400|600) ;;
    *)
      echo "pre-install deployment-tree recovery state must have mode 0400 or 0600" >&2
      exit 1
      ;;
  esac
  preinstall_tree_path="$(read_file_value "${preinstall_state}" VIRTROID_PREINSTALL_TREE_PATH)"
  preinstall_tree_digest="$(read_file_value "${preinstall_state}" VIRTROID_PREINSTALL_TREE_SHA256)"
  state_installed_tree_digest="$(read_file_value "${preinstall_state}" VIRTROID_INSTALLED_TREE_SHA256)"
  case "${preinstall_tree_path}" in
    "${preinstall_backup_root}"/[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z.*) ;;
    *)
      echo "pre-install deployment-tree backup path is outside the protected backup root" >&2
      exit 1
      ;;
  esac
  [ -d "${preinstall_tree_path}/vps" ] && [ ! -L "${preinstall_tree_path}" ] || {
    echo "pre-install deployment-tree backup is missing or unsafe" >&2
    exit 1
  }
  [[ "${preinstall_tree_digest}" =~ ^[0-9a-f]{64}$ ]] &&
    [ "${state_installed_tree_digest}" = "${installed_tree_digest}" ] || {
    echo "pre-install deployment-tree recovery state does not match the installed tree" >&2
    exit 1
  }
  recorded_preinstall_digest="$(tr -d '\n' < "${preinstall_tree_path}/DEPLOYMENT_TREE_SHA256")"
  actual_preinstall_digest="$(/opt/virtroid/deploy/vps/deployment-tree-digest.sh "${preinstall_tree_path}/vps")"
  if [ "${recorded_preinstall_digest}" != "${preinstall_tree_digest}" ] ||
     [ "${actual_preinstall_digest}" != "${preinstall_tree_digest}" ]; then
    echo "pre-install deployment-tree backup failed digest verification" >&2
    exit 1
  fi
  if [[ "${current_release_tree_digest}" =~ ^[0-9a-f]{64}$ ]] &&
     [ "${current_release_tree_digest}" != "${preinstall_tree_digest}" ]; then
    echo "pre-install deployment tree does not match the running release state" >&2
    exit 1
  fi
fi
if [ -e "${final_dir}" ]; then
  echo "backup destination already exists: ${final_dir}" >&2
  exit 1
fi

if [ "${legacy_release}" -eq 1 ]; then
  for container_name in virtroidd virtnoded virtroid-falco-forwarder; do
    if docker inspect "${container_name}" >/dev/null 2>&1; then
      image_id="$(docker inspect --format '{{.Image}}' "${container_name}")"
      image_ref="$(docker inspect --format '{{.Config.Image}}' "${container_name}")"
      if [[ ! "${image_id}" =~ ^sha256:[0-9a-f]{64}$ ]] ||
         [[ "${image_ref}" == *$'\n'* || "${image_ref}" == *$'\r'* || -z "${image_ref}" ]]; then
        echo "legacy container has invalid image identity: ${container_name}" >&2
        exit 1
      fi
      legacy_mapping_containers+=("${container_name}")
      legacy_mapping_ids+=("${image_id}")
      legacy_mapping_refs+=("${image_ref}")
      duplicate=0
      for existing_image in "${legacy_image_ids[@]}"; do
        if [ "${existing_image}" = "${image_id}" ]; then
          duplicate=1
          break
        fi
      done
      if [ "${duplicate}" -eq 0 ]; then
        legacy_image_ids+=("${image_id}")
        image_size="$(docker image inspect --format '{{.Size}}' "${image_id}")"
        [[ "${image_size}" =~ ^[0-9]+$ ]] || {
          echo "could not determine legacy image size" >&2
          exit 1
        }
        legacy_image_bytes=$((legacy_image_bytes + image_size))
      fi
    fi
  done
  [ "${#legacy_image_ids[@]}" -gt 0 ] || {
    echo "legacy release has no exportable backend image" >&2
    exit 1
  }
fi

container_is_running virtroid-edge && edge_was_running=1
container_is_running virtroidd && control_was_running=1
container_is_running virtnoded && node_was_running=1
if docker inspect virtroid-renterd >/dev/null 2>&1 && container_is_running virtroid-renterd; then
  renterd_was_running=1
fi

# Refuse without interruption when work is active. Once the edge is stopped,
# external clients cannot win a session-creation race with the second check.
validate_zero_sessions
validate_no_running_runtime_guests
if [ "${edge_was_running}" -eq 1 ]; then
  docker stop --time 30 virtroid-edge >/dev/null
fi
validate_zero_sessions
if [ "${node_was_running}" -eq 1 ]; then
  docker stop --time 60 virtnoded >/dev/null
fi
if [ "${control_was_running}" -eq 1 ]; then
  docker stop --time 30 virtroidd >/dev/null
fi
if [ "${renterd_was_running}" -eq 1 ]; then
  docker stop --time 300 virtroid-renterd >/dev/null
fi
validate_zero_sessions
validate_no_running_runtime_guests

# Recheck immutable identities after the writers are quiesced. This prevents a
# partially interrupted deployment (or an out-of-band container replacement)
# from becoming the newest completed recovery set.
if [ "${legacy_release}" -ne 1 ]; then
  validate_container_release_state virtroidd
  validate_container_release_state virtnoded
  if docker inspect virtroid-falco-forwarder >/dev/null 2>&1; then
    validate_container_release_state virtroid-falco-forwarder
  fi
fi
validate_infrastructure_container virtroid-postgres POSTGRES_IMAGE
validate_infrastructure_container virtroid-edge HAPROXY_IMAGE
if docker inspect virtroid-renterd >/dev/null 2>&1; then
  validate_infrastructure_container virtroid-renterd RENTERD_IMAGE
fi
if docker inspect virtroid-falco >/dev/null 2>&1; then
  validate_infrastructure_container virtroid-falco FALCO_IMAGE
fi

source_bytes="$(du -sb /srv/virtroid | awk '{print $1}')"
if [ "${prior_tree_required}" -eq 1 ]; then
  source_bytes=$((source_bytes + $(du -sb "${preinstall_tree_path}/vps" | awk '{print $1}')))
fi
renterd_volume_path=
if docker inspect virtroid-renterd >/dev/null 2>&1; then
  renterd_volume_path="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' virtroid-renterd)"
  case "${renterd_volume_path}" in
    /var/lib/docker/volumes/*/_data) ;;
    *)
      echo "renterd data volume is missing or outside Docker's managed volume root" >&2
      exit 1
      ;;
  esac
  [ -d "${renterd_volume_path}" ] && [ ! -L "${renterd_volume_path}" ] || {
    echo "renterd data volume path is unsafe" >&2
    exit 1
  }
  source_bytes=$((source_bytes + $(du -sb "${renterd_volume_path}" | awk '{print $1}')))
fi
available_bytes="$(df -B1 --output=avail "${backup_root}" | tail -n 1 | tr -d ' ')"
database_bytes="$(docker exec virtroid-postgres psql -U "${postgres_user}" -d "${postgres_database}" -Atc \
  'SELECT pg_database_size(current_database());')"
[[ "${database_bytes}" =~ ^[0-9]+$ ]] || {
  echo "could not determine the current database size" >&2
  exit 1
}
minimum_free=$((source_bytes * 2 + database_bytes * 2 + legacy_image_bytes + 1073741824))
if [ "${available_bytes}" -lt "${minimum_free}" ]; then
  echo "insufficient free space for a safe Virtroid backup" >&2
  exit 1
fi

current_schema="$(docker exec virtroid-postgres psql -U "${postgres_user}" -d "${postgres_database}" -Atc \
  'SELECT COALESCE(MAX(version), 0) FROM schema_migrations;')"
[[ "${current_schema}" =~ ^[0-9]+$ ]] || {
  echo "could not determine the current database schema" >&2
  exit 1
}
if [ "${legacy_release}" -ne 1 ] && [ "${current_schema}" != "${current_release_schema}" ]; then
  echo "protected release schema does not match the live database schema" >&2
  exit 1
fi

copy_release_state() {
  local state_link="$1"
  local destination="$2"
  local resolved

  [ -e "${state_link}" ] || [ -L "${state_link}" ] || return 1
  resolved="$(readlink -f "${state_link}")"
  case "${resolved}" in
    "${release_state_root}"/releases/*.env) ;;
    *)
      echo "release state points outside the protected releases directory" >&2
      exit 1
      ;;
  esac
  [ -f "${resolved}" ] && [ ! -L "${resolved}" ] || {
    echo "release state target is not a regular file" >&2
    exit 1
  }
  [ "$(stat -c '%u' "${resolved}")" -eq 0 ] || {
    echo "release state must be root-owned" >&2
    exit 1
  }
  case "$(stat -c '%a' "${resolved}")" in
    400|600) ;;
    *)
      echo "release state must have mode 0400 or 0600" >&2
      exit 1
      ;;
  esac
  install -o root -g root -m 0600 "${resolved}" "${destination}"
}

write_legacy_release_state() {
  local image_ref
  local image_id
  local manifest_digest
  local source_sha
  local schema_label
  local deployment_tree

  image_ref="$(docker inspect --format '{{.Config.Image}}' virtroidd)"
  image_id="$(docker inspect --format '{{.Image}}' virtroidd)"
  manifest_digest=legacy-unknown
  source_sha="$(docker inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' virtroidd)"
  schema_label="$(docker inspect --format '{{index .Config.Labels "com.virtroid.schema-version"}}' virtroidd)"
  deployment_tree="$(docker inspect --format '{{index .Config.Labels "com.virtroid.deployment-tree-sha256"}}' virtroidd)"
  [ "${source_sha}" != "<no value>" ] || source_sha=
  [ "${schema_label}" != "<no value>" ] || schema_label=
  [ "${deployment_tree}" != "<no value>" ] || deployment_tree=
  for value in "${image_ref}" "${image_id}" "${manifest_digest}" "${source_sha}" "${schema_label}" "${deployment_tree}"; do
    [[ "${value}" != *$'\n'* && "${value}" != *$'\r'* ]] || {
      echo "container release metadata contains a control character" >&2
      exit 1
    }
  done
  printf 'VIRTROID_BACKEND_IMAGE=%s\nVIRTROID_BACKEND_IMAGE_ID=%s\nVIRTROID_BACKEND_MANIFEST_DIGEST=%s\nVIRTROID_SOURCE_SHA=%s\nVIRTROID_SCHEMA_VERSION=%s\nVIRTROID_DEPLOYMENT_TREE_SHA256=%s\n' \
    "${image_ref:-legacy-unknown}" \
    "${image_id:-legacy-unknown}" \
    "${manifest_digest}" \
    "${source_sha:-legacy-unknown}" \
    "${schema_label:-${current_schema}}" \
    "${deployment_tree:-legacy-unknown}" > "${partial_dir}/release-current.env"
}

docker exec virtroid-postgres pg_dump -U "${postgres_user}" -d "${postgres_database}" --format=custom > "${partial_dir}/virtroid-postgres.dump"
docker exec -i virtroid-postgres pg_restore --list < "${partial_dir}/virtroid-postgres.dump" >/dev/null
docker exec -i virtroid-postgres pg_restore --file=/dev/null < "${partial_dir}/virtroid-postgres.dump"
tar --xattrs --acls -C /srv -czf "${partial_dir}/srv-virtroid.tgz" virtroid
tar -tzf "${partial_dir}/srv-virtroid.tgz" >/dev/null
tar -C /opt/virtroid/deploy \
  --exclude='vps/.env' \
  --exclude='vps/release.env' \
  -czf "${partial_dir}/deploy-tree.tgz" vps
tar -tzf "${partial_dir}/deploy-tree.tgz" >/dev/null
if [ "${prior_tree_required}" -eq 1 ]; then
  tar --xattrs --acls -C "${preinstall_tree_path}" \
    -czf "${partial_dir}/preinstall-deploy-tree.tgz" \
    vps DEPLOYMENT_TREE_SHA256
  tar -tzf "${partial_dir}/preinstall-deploy-tree.tgz" >/dev/null
fi
if [ -n "${renterd_volume_path}" ]; then
  tar --xattrs --acls -C "${renterd_volume_path}" -czf "${partial_dir}/renterd-data.tgz" .
  tar -tzf "${partial_dir}/renterd-data.tgz" >/dev/null
fi
install -o root -g root -m 0600 "${deploy_env}" "${partial_dir}/deploy.env"
if [ -e "${release_state_root}/current.env" ] || [ -L "${release_state_root}/current.env" ]; then
  copy_release_state "${release_state_root}/current.env" "${partial_dir}/release-current.env"
else
  write_legacy_release_state
fi
if [ "${legacy_release}" -eq 1 ]; then
  docker image save --output "${partial_dir}/legacy-backend-images.tar" "${legacy_image_ids[@]}"
  tar -tf "${partial_dir}/legacy-backend-images.tar" >/dev/null
fi
previous_included=0
if [ -e "${release_state_root}/previous.env" ] || [ -L "${release_state_root}/previous.env" ]; then
  copy_release_state "${release_state_root}/previous.env" "${partial_dir}/release-previous.env"
  previous_included=1
fi
{
  date -u +%FT%TZ
  docker inspect --format '{{.Name}} configured_image={{.Config.Image}} image_id={{.Image}} source={{index .Config.Labels "org.opencontainers.image.revision"}} schema={{index .Config.Labels "com.virtroid.schema-version"}} deployment_tree={{index .Config.Labels "com.virtroid.deployment-tree-sha256"}} restart={{.RestartCount}}' \
    virtroid-postgres virtroidd virtnoded virtroid-edge
  for container_name in virtroid-renterd virtroid-falco virtroid-falco-forwarder; do
    if docker inspect "${container_name}" >/dev/null 2>&1; then
      docker inspect --format '{{.Name}} configured_image={{.Config.Image}} image_id={{.Image}} source={{index .Config.Labels "org.opencontainers.image.revision"}} schema={{index .Config.Labels "com.virtroid.schema-version"}} deployment_tree={{index .Config.Labels "com.virtroid.deployment-tree-sha256"}} restart={{.RestartCount}}' \
        "${container_name}"
    fi
  done
  for ((index = 0; index < ${#legacy_mapping_containers[@]}; index++)); do
    printf 'legacy_container_image container=%s image_id=%s configured_image=%s\n' \
      "${legacy_mapping_containers[index]}" \
      "${legacy_mapping_ids[index]}" \
      "${legacy_mapping_refs[index]}"
  done
  printf 'database_schema=%s\n' "${current_schema}"
  printf 'database_size_bytes=%s\n' "${database_bytes}"
  printf 'installed_deployment_tree_sha256=%s\n' "${installed_tree_digest}"
  printf 'release_deployment_tree_sha256=%s\n' "${current_release_tree_digest:-legacy-unknown}"
  printf 'preinstall_deployment_tree_included=%s\n' "${prior_tree_required}"
  if [ "${prior_tree_required}" -eq 1 ]; then
    printf 'preinstall_deployment_tree_sha256=%s\n' "${preinstall_tree_digest}"
  fi
} > "${partial_dir}/metadata.txt"

checksum_files=(
  deploy.env
  release-current.env
  deploy-tree.tgz
  metadata.txt
  srv-virtroid.tgz
  virtroid-postgres.dump
)
if [ "${previous_included}" -eq 1 ]; then
  checksum_files+=(release-previous.env)
fi
if [ -n "${renterd_volume_path}" ]; then
  checksum_files+=(renterd-data.tgz)
fi
if [ "${legacy_release}" -eq 1 ]; then
  checksum_files+=(legacy-backend-images.tar)
fi
if [ "${prior_tree_required}" -eq 1 ]; then
  checksum_files+=(preinstall-deploy-tree.tgz)
fi
(
  cd "${partial_dir}"
  sha256sum "${checksum_files[@]}" > SHA256SUMS
  sha256sum --check SHA256SUMS >/dev/null
)
sync -f "${partial_dir}"
mv "${partial_dir}" "${final_dir}"
partial_dir=
sync -f "${backup_root}"
resume_containers
trap - EXIT

mapfile -t completed_backups < <(
  find "${backup_root}" -mindepth 1 -maxdepth 1 -type d \
    -name 'daily-????????T??????Z' -printf '%T@ %p\n' | sort -nr | cut -d' ' -f2-
)
for ((index = retention_days; index < ${#completed_backups[@]}; index++)); do
  find "${completed_backups[index]}" -depth -delete
done

printf 'Virtroid backup completed: %s\n' "${final_dir}"
