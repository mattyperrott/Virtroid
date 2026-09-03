#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly trusted_helper=/usr/local/sbin/virtroid-apply-local-release
readonly deploy_root=/opt/virtroid/deploy/vps
readonly deploy_env=${deploy_root}/.env
readonly state_root=/var/lib/virtroid-deploy
readonly releases_root=${state_root}/releases
readonly maintenance_lock=${state_root}/maintenance.lock

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

if [ "$(id -u)" -ne 0 ]; then
  die "run as root"
fi
if [ "$(readlink -f -- "$0")" != "${trusted_helper}" ]; then
  die "run the preinstalled root-owned local release helper: ${trusted_helper}"
fi
if [ "$#" -ne 2 ]; then
  die "usage: virtroid-apply-local-release <staged-bundle-directory> <expected-node-fingerprint>"
fi
if [ ! -r "/proc/$$/fd/255" ]; then
  die "cannot verify the executing local release helper descriptor"
fi

for command_name in awk cmp curl date docker find flock install ln mktemp mv openssl readlink seq sha256sum sleep stat; do
  command -v "${command_name}" >/dev/null 2>&1 || die "missing local release command: ${command_name}"
done

install -d -o root -g root -m 0700 "${state_root}" "${releases_root}"
for safe_dir in "${state_root}" "${releases_root}" /opt /opt/virtroid /opt/virtroid/deploy "${deploy_root}"; do
  [ -d "${safe_dir}" ] && [ ! -L "${safe_dir}" ] || die "unsafe release directory: ${safe_dir}"
  [ "$(stat -c '%u' "${safe_dir}")" -eq 0 ] || die "release directory is not root-owned: ${safe_dir}"
  (( (8#$(stat -c '%a' "${safe_dir}") & 0022) == 0 )) ||
    die "release directory is group/world writable: ${safe_dir}"
done
[ -f "${trusted_helper}" ] && [ ! -L "${trusted_helper}" ] || die "trusted release helper is unsafe"
[ "$(stat -c '%u' "${trusted_helper}")" -eq 0 ] || die "trusted release helper is not root-owned"
(( (8#$(stat -c '%a' "${trusted_helper}") & 0022) == 0 )) ||
  die "trusted release helper is group/world writable"

executing_helper_digest="$(sha256sum "/proc/$$/fd/255" | awk '{print $1}')"
exec 8>"${maintenance_lock}"
if ! flock -n 8; then
  echo "another Virtroid maintenance operation is running" >&2
  exit 75
fi
installed_helper_digest="$(sha256sum "${trusted_helper}" | awk '{print $1}')"
[ "${executing_helper_digest}" = "${installed_helper_digest}" ] || {
  echo "local release helper changed while this process was starting" >&2
  exit 75
}

source_dir="$(readlink -f -- "$1")"
expected_fingerprint="$2"
[[ "${source_dir}" =~ ^/tmp/virtroid-local-release\.[A-Za-z0-9]{10}$ ]] ||
  die "staged bundle must use the dedicated /tmp/virtroid-local-release.* path"
[ -d "${source_dir}" ] && [ ! -L "${source_dir}" ] || die "staged bundle must be a real directory"
(( (8#$(stat -c '%a' "${source_dir}") & 0022) == 0 )) || die "staged bundle directory is group/world writable"
[[ "${expected_fingerprint}" =~ ^[0-9a-f]{64}$ ]] || die "expected node fingerprint must be 64 lowercase hexadecimal characters"

profiles="${VIRTROID_PROFILES:-edge}"
[[ "${profiles}" =~ ^[a-z][a-z0-9-]*(,[a-z][a-z0-9-]*)*$ ]] || die "VIRTROID_PROFILES is invalid"
IFS=',' read -ra profile_list <<< "${profiles}"
for profile in "${profile_list[@]}"; do
  case "${profile}" in
    edge|renterd|falco|nids|monitoring) ;;
    *) die "unsupported production profile: ${profile}" ;;
  esac
done

profile_enabled() {
  local wanted="$1" profile
  for profile in "${profile_list[@]}"; do
    [ "${profile}" = "${wanted}" ] && return 0
  done
  return 1
}

bundle_snapshot="$(mktemp -d "${state_root}/local-release-bundle.XXXXXX")"
cutover_started=0
deployment_complete=0
cleanup() {
  local original_status="$?"
  trap - EXIT HUP INT TERM
  if [ "${cutover_started}" -eq 1 ] && [ "${deployment_complete}" -ne 1 ]; then
    docker stop --time 30 virtroid-edge >/dev/null 2>&1 || true
    echo "local release did not complete; public ingress is stopped for operator recovery" >&2
  fi
  if [ -d "${bundle_snapshot}" ]; then
    find "${bundle_snapshot}" -xdev -depth -delete || true
  fi
  exit "${original_status}"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

bundle_files=(backend-image.tar deployment-tree.tar.gz release.env SHA256SUMS)
for bundle_file in "${bundle_files[@]}"; do
  source_file="${source_dir}/${bundle_file}"
  [ -f "${source_file}" ] && [ ! -L "${source_file}" ] || die "staged bundle is missing ${bundle_file}"
  install -o root -g root -m 0600 "${source_file}" "${bundle_snapshot}/${bundle_file}"
done
image_archive_size="$(stat -c '%s' "${bundle_snapshot}/backend-image.tar")"
[[ "${image_archive_size}" =~ ^[0-9]+$ ]] && [ "${image_archive_size}" -ge 1048576 ] &&
  [ "${image_archive_size}" -le 4294967296 ] || die "backend image archive has an unsafe size"

manifest_names="$(awk '
  NF != 2 || $1 !~ /^[0-9a-f]{64}$/ { exit 1 }
  $2 != "backend-image.tar" && $2 != "deployment-tree.tar.gz" && $2 != "release.env" { exit 1 }
  { count[$2]++; print $2 }
  END {
    if (NR != 3 || count["backend-image.tar"] != 1 || count["deployment-tree.tar.gz"] != 1 || count["release.env"] != 1) exit 1
  }
' "${bundle_snapshot}/SHA256SUMS")" || die "release checksum manifest is not the exact expected file set"
[ -n "${manifest_names}" ] || die "release checksum manifest is empty"
(
  cd "${bundle_snapshot}"
  sha256sum --check --strict SHA256SUMS
) || die "release bundle checksum verification failed"

read_release_value() {
  local wanted="$1"
  awk -v wanted="${wanted}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      separator = index($0, "=")
      if (!separator) next
      key = substr($0, 1, separator - 1)
      if (key == wanted) {
        found++
        value = substr($0, separator + 1)
      } else if (key != "VIRTROID_BACKEND_IMAGE" &&
                 key != "VIRTROID_BACKEND_IMAGE_ID" &&
                 key != "VIRTROID_BACKEND_MANIFEST_DIGEST" &&
                 key != "VIRTROID_SOURCE_SHA" &&
                 key != "VIRTROID_SCHEMA_VERSION" &&
                 key != "VIRTROID_DEPLOYMENT_TREE_SHA256") {
        invalid = 1
      }
    }
    END { if (found != 1 || invalid) exit 1; print value }
  ' "${bundle_snapshot}/release.env"
}

read_deploy_value() {
  local wanted="$1"
  awk -v wanted="${wanted}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      separator = index($0, "=")
      if (!separator) next
      if (substr($0, 1, separator - 1) == wanted) {
        found++
        value = substr($0, separator + 1)
      }
    }
    END { if (found > 1) exit 1; if (found == 1) print value }
  ' "${deploy_env}"
}

ensure_callback_signing_keypair() {
  local private_key
  local public_key
  local private_tmp
  local env_tmp
  local derived_public
  local private_missing=0
  private_key="$(read_deploy_value CONTROL_PLANE_CALLBACK_PRIVATE_KEY_B64)" || die "CONTROL_PLANE_CALLBACK_PRIVATE_KEY_B64 is duplicated"
  public_key="$(read_deploy_value CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64)" || die "CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64 is duplicated"
  if [ -z "${private_key}" ] && [ -n "${public_key}" ]; then
    die "control-plane callback public key exists without its private key"
  fi
  if [ -z "${private_key}" ]; then
    private_missing=1
    private_key="$(openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:prime256v1 -outform DER 2>/dev/null | openssl base64 -A)"
  fi
  private_tmp="$(mktemp "${state_root}/callback-private.XXXXXX")"
  if ! printf '%s' "${private_key}" | openssl base64 -d -A > "${private_tmp}" ||
     ! openssl pkey -inform DER -in "${private_tmp}" -check -noout >/dev/null 2>&1; then
    find "${private_tmp}" -delete
    die "CONTROL_PLANE_CALLBACK_PRIVATE_KEY_B64 is invalid"
  fi
  derived_public="$(openssl pkey -inform DER -in "${private_tmp}" -pubout -outform DER 2>/dev/null | openssl base64 -A)"
  find "${private_tmp}" -delete
  if [ -n "${public_key}" ] && [ "${public_key}" != "${derived_public}" ]; then
    die "configured control-plane callback public key does not match its private key"
  fi
  [ -n "${public_key}" ] && return 0

  env_tmp="$(mktemp "${state_root}/deploy-env.XXXXXX")"
  {
    awk '{ print }' "${deploy_env}"
    if [ "${private_missing}" -eq 1 ]; then
      printf 'CONTROL_PLANE_CALLBACK_PRIVATE_KEY_B64=%s\n' "${private_key}"
    fi
    printf 'CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64=%s\n' "${derived_public}"
  } > "${env_tmp}"
  install -o root -g root -m 0600 "${env_tmp}" "${deploy_env}"
  find "${env_tmp}" -delete
}

release_image="$(read_release_value VIRTROID_BACKEND_IMAGE)" || die "release image is invalid"
release_image_id="$(read_release_value VIRTROID_BACKEND_IMAGE_ID)" || die "release image ID is invalid"
release_manifest_digest="$(read_release_value VIRTROID_BACKEND_MANIFEST_DIGEST)" || die "release manifest digest is invalid"
release_source_sha="$(read_release_value VIRTROID_SOURCE_SHA)" || die "release source SHA is invalid"
release_schema="$(read_release_value VIRTROID_SCHEMA_VERSION)" || die "release schema is invalid"
release_tree="$(read_release_value VIRTROID_DEPLOYMENT_TREE_SHA256)" || die "release deployment-tree digest is invalid"
postgres_user="$(read_deploy_value POSTGRES_USER)" || die "POSTGRES_USER is duplicated"
postgres_database="$(read_deploy_value POSTGRES_DB)" || die "POSTGRES_DB is duplicated"
postgres_user="${postgres_user:-virtroid}"
postgres_database="${postgres_database:-virtroid}"
ensure_callback_signing_keypair
[[ "${postgres_user}" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || die "POSTGRES_USER is invalid"
[[ "${postgres_database}" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || die "POSTGRES_DB is invalid"
[[ "${release_image}" =~ ^virtroid-backend:release-([0-9a-f]{40})$ ]] &&
  [ "${BASH_REMATCH[1]}" = "${release_source_sha}" ] || die "release tag and source SHA do not match"
[[ "${release_image_id}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "release image ID must be an exact sha256 ID"
[[ "${release_manifest_digest}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "release manifest digest must be an exact sha256 digest"
[[ "${release_schema}" =~ ^[0-9]+$ ]] && [ "${release_schema}" -gt 0 ] || die "release schema must be positive"
[[ "${release_tree}" =~ ^[0-9a-f]{64}$ ]] || die "release deployment-tree digest is invalid"
installed_tree="$("${deploy_root}/deployment-tree-digest.sh" "${deploy_root}")"
[ "${installed_tree}" = "${release_tree}" ] || die "installed deployment tree does not match the release bundle"

docker load --input "${bundle_snapshot}/backend-image.tar" >/dev/null
actual_image_id="$(docker image inspect --format '{{.Id}}' "${release_image}")"
architecture="$(docker image inspect --format '{{.Architecture}}' "${actual_image_id}")"
operating_system="$(docker image inspect --format '{{.Os}}' "${actual_image_id}")"
image_revision="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "${actual_image_id}")"
image_schema="$(docker image inspect --format '{{index .Config.Labels "com.virtroid.schema-version"}}' "${actual_image_id}")"
image_tree="$(docker image inspect --format '{{index .Config.Labels "com.virtroid.deployment-tree-sha256"}}' "${actual_image_id}")"
{ [ "${actual_image_id}" = "${release_image_id}" ] ||
  [ "${actual_image_id}" = "${release_manifest_digest}" ]; } &&
  [ "${operating_system}/${architecture}" = linux/amd64 ] &&
  [ "${image_revision}" = "${release_source_sha}" ] &&
  [ "${image_schema}" = "${release_schema}" ] &&
  [ "${image_tree}" = "${release_tree}" ] || die "loaded image identity or labels do not match the release bundle"

release_name="${release_source_sha}-${release_image_id#sha256:}.env"
release_path="${releases_root}/${release_name}"
current_same=0
if [ -e "${state_root}/current.env" ] || [ -L "${state_root}/current.env" ]; then
  current_path="$(readlink -f "${state_root}/current.env")"
  case "${current_path}" in
    "${releases_root}"/*.env) ;;
    *) die "current release state points outside the protected releases directory" ;;
  esac
  if cmp -s "${current_path}" "${bundle_snapshot}/release.env"; then
    current_same=1
  fi
fi

existing_backend_containers=0
for container_name in virtroidd virtnoded virtroid-falco-forwarder; do
  if docker inspect "${container_name}" >/dev/null 2>&1; then
    existing_backend_containers=$((existing_backend_containers + 1))
  fi
done
if [ "${current_same}" -eq 0 ] && [ "${existing_backend_containers}" -gt 0 ]; then
  VIRTROID_MAINTENANCE_LOCK_HELD=1 /usr/local/sbin/virtroid-backup.sh
fi

if [ -e "${release_path}" ] || [ -L "${release_path}" ]; then
  [ -f "${release_path}" ] && [ ! -L "${release_path}" ] &&
    cmp -s "${release_path}" "${bundle_snapshot}/release.env" || die "protected release filename already contains different state"
else
  install -o root -g root -m 0400 "${bundle_snapshot}/release.env" "${release_path}"
fi

VIRTROID_ENV_FILE="${deploy_env}" \
VIRTROID_RELEASE_ENV_FILE="${release_path}" \
VIRTROID_MAINTENANCE_LOCK_HELD=1 \
VIRTROID_PROFILES="${profiles}" \
  "${deploy_root}/deploy.sh" pull

docker stop --time 30 virtroid-edge >/dev/null 2>&1 || true
cutover_started=1
link_temp="${state_root}/.current.${release_source_sha}.$$"
ln -s "releases/${release_name}" "${link_temp}"
mv -Tf "${link_temp}" "${state_root}/current.env"

compose_args=(compose --env-file "${deploy_env}" --env-file "${release_path}" -f "${deploy_root}/docker-compose.yml")
for profile in "${profile_list[@]}"; do
  compose_args+=(--profile "${profile}")
done
compose() {
  docker "${compose_args[@]}" "$@"
}

backend_services=(virtroidd virtnoded)
if profile_enabled falco || profile_enabled nids; then
  backend_services+=(falco-forwarder)
fi
docker stop --time 60 virtnoded virtroidd virtroid-falco-forwarder >/dev/null 2>&1 || true
compose up --no-start --no-deps --force-recreate --no-build --pull never "${backend_services[@]}"
compose up -d --no-build --pull never postgres
if profile_enabled renterd; then
  compose up -d --no-build --pull never renterd
fi
compose up -d --no-deps --no-build --pull never virtroidd

control_ready=0
for _ in $(seq 1 60); do
  if health_response="$(curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8080/healthz 2>/dev/null)" &&
     [[ "${health_response}" =~ \"ok\"[[:space:]]*:[[:space:]]*true ]]; then
    control_ready=1
    break
  fi
  sleep 1
done
[ "${control_ready}" -eq 1 ] || die "candidate control plane did not become healthy"
schema_after="$(docker exec virtroid-postgres psql -U "${postgres_user}" -d "${postgres_database}" -Atc \
  'SELECT COALESCE(MAX(version), 0) FROM schema_migrations;')"
[ "${schema_after}" = "${release_schema}" ] ||
  die "candidate database schema ${schema_after:-unknown} does not match release schema ${release_schema}"

fingerprint_state="$(/usr/local/sbin/virtroid-derive-node-fingerprint)"
node_id="$(printf '%s\n' "${fingerprint_state}" | awk -F= '$1 == "node_id" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }')" || die "could not derive the production node ID"
node_public_key="$(printf '%s\n' "${fingerprint_state}" | awk -F= '$1 == "public_key_b64" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }')" || die "could not derive the production node public key"
node_fingerprint="$(printf '%s\n' "${fingerprint_state}" | awk -F= '$1 == "fingerprint_sha256" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }')" || die "could not derive the production node fingerprint"
[[ "${node_id}" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ ]] || die "derived production node ID is invalid"
[[ "${node_public_key}" =~ ^[A-Za-z0-9+/]+={0,2}$ ]] || die "derived production node public key is invalid"
[ "${node_fingerprint}" = "${expected_fingerprint}" ] || die "production node fingerprint does not match the independently recorded value"

actor="local-release:${SUDO_USER:-root}"
compose run --rm --no-deps -T virtroid-admin approve \
  --node "${node_id}" \
  --operator production \
  --operator-name 'Virtroid production operator' \
  --public-key "${node_public_key}" \
  --actor "${actor}" \
  --reason "approved during local release ${release_source_sha}"

compose up -d --no-deps --no-build --pull never virtnoded
node_ready=0
for _ in $(seq 1 90); do
  if health_response="$(curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8090/healthz 2>/dev/null)" &&
     [[ "${health_response}" =~ \"ok\"[[:space:]]*:[[:space:]]*true ]]; then
    heartbeat_count="$(docker exec virtroid-postgres psql \
      -U "${postgres_user}" -d "${postgres_database}" -At \
      -c "SELECT COUNT(*) FROM hosts h JOIN approved_nodes n ON n.node_id = h.id AND n.status = 'approved' JOIN approved_node_keys k ON k.node_id = n.node_id AND k.key_version = n.active_key_version AND k.state = 'active' WHERE h.id = '${node_id}' AND k.fingerprint_sha256 = '${node_fingerprint}' AND h.public_key = k.public_key AND h.last_heartbeat_at >= NOW() - INTERVAL '2 minutes';" 2>/dev/null || true)"
    if [ "${heartbeat_count}" = 1 ]; then
      node_ready=1
      break
    fi
  fi
  sleep 1
done
[ "${node_ready}" -eq 1 ] || die "approved node did not publish a fresh matching heartbeat"

if profile_enabled falco || profile_enabled nids; then
  compose up -d --no-deps --no-build --pull never falco-forwarder
fi
if profile_enabled falco; then
  compose up -d --no-deps --no-build --pull never falco
fi
if profile_enabled nids; then
  compose up -d --no-deps --no-build --pull never suricata
fi
if profile_enabled monitoring; then
  compose up -d --no-build --pull never alertmanager prometheus
fi
if profile_enabled edge; then
  compose up -d --no-deps --no-build --pull never edge
fi

VIRTROID_ENV_FILE="${deploy_env}" \
VIRTROID_RELEASE_ENV_FILE="${release_path}" \
VIRTROID_PROFILES="${profiles}" \
  "${deploy_root}/deploy.sh" health

for container_name in virtroidd virtnoded; do
  [ "$(docker inspect --format '{{.Image}}' "${container_name}")" = "${release_image_id}" ] ||
  [ "$(docker inspect --format '{{.Image}}' "${container_name}")" = "${release_manifest_digest}" ] ||
    die "${container_name} is not running an expected engine image identity"
done
if profile_enabled falco || profile_enabled nids; then
  falco_forwarder_image="$(docker inspect --format '{{.Image}}' virtroid-falco-forwarder)"
  [ "${falco_forwarder_image}" = "${release_image_id}" ] ||
  [ "${falco_forwarder_image}" = "${release_manifest_digest}" ] ||
    die "virtroid-falco-forwarder is not running an expected engine image identity"
fi

deployment_complete=1
cutover_started=0
trap - EXIT HUP INT TERM
find "${bundle_snapshot}" -xdev -depth -delete
printf 'local release applied source=%s image_id=%s schema=%s tree=%s\n' \
  "${release_source_sha}" "${release_image_id}" "${release_schema}" "${release_tree}"
