#!/bin/bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="${VIRTROID_ENV_FILE:-${script_dir}/.env}"
if [ -n "${VIRTROID_RELEASE_ENV_FILE:-}" ]; then
  release_env_file="${VIRTROID_RELEASE_ENV_FILE}"
elif [ -e /var/lib/virtroid-deploy/current.env ] || [ -L /var/lib/virtroid-deploy/current.env ]; then
  release_env_file=/var/lib/virtroid-deploy/current.env
else
  release_env_file="${script_dir}/release.env"
fi
profiles="${VIRTROID_PROFILES:-edge}"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./deploy.sh [validate|up|pull|restart|logs|ps|health|down]

Environment:
  VIRTROID_ENV_FILE   Path to .env file. Defaults to deploy/vps/.env.
  VIRTROID_RELEASE_ENV_FILE
                       Path to the non-secret immutable release state. Defaults
                       to /var/lib/virtroid-deploy/current.env when present,
                       otherwise deploy/vps/release.env.
  VIRTROID_PROFILES   Comma-separated compose profiles. Defaults to edge.

Typical first deploy:
  sudo ./prepare-redroid-host.sh
  ./generate-env.sh https://your-domain.example
  cp release.env.example release.env
  # Import the locally built release image and fill the exact local tag, image
  # ID, source commit, schema version, and deployment-tree digest.
  sudo install -m 0644 fullchain-plus-key.pem /srv/virtroid/tls/virtroid.pem
  ./deploy.sh up
EOF
}

command_name="${1:-up}"
case "${command_name}" in
  -h|--help)
    usage
    exit 0
    ;;
esac

if [ ! -f "${env_file}" ]; then
  echo "missing env file: ${env_file}" >&2
  echo "copy .env.example to .env or run ./generate-env.sh https://your-domain.example" >&2
  exit 1
fi
if [ ! -f "${release_env_file}" ]; then
  echo "missing release env file: ${release_env_file}" >&2
  echo "copy release.env.example to release.env or use the direct local release bundle helper" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is not installed. Run sudo ./prepare-redroid-host.sh first." >&2
  exit 1
fi

compose() {
  local args=(compose --env-file "${env_file}" --env-file "${release_env_file}")
  local profile
  IFS=',' read -ra profile_list <<< "${profiles}"
  for profile in "${profile_list[@]}"; do
    profile="$(echo "${profile}" | xargs)"
    if [ -n "${profile}" ]; then
      args+=(--profile "${profile}")
    fi
  done
  docker "${args[@]}" "$@"
}

load_env_file() {
  local input_file="$1"
  local line key value
  while IFS= read -r line || [ -n "${line}" ]; do
    line="${line%$'\r'}"
    if [[ -z "${line}" || "${line}" == \#* ]]; then
      continue
    fi
    if [[ "${line}" != *=* ]]; then
      echo "invalid env line in ${input_file}: ${line}" >&2
      exit 1
    fi
    key="${line%%=*}"
    value="${line#*=}"
    if [[ ! "${key}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "invalid env key in ${input_file}: ${key}" >&2
      exit 1
    fi
    export "${key}=${value}"
  done < "${input_file}"
}

load_release_env() {
  local line key value
  local seen_image=0
  local seen_image_id=0
  local seen_manifest_digest=0
  local seen_sha=0
  local seen_schema=0
  local seen_deployment_tree=0
  while IFS= read -r line || [ -n "${line}" ]; do
    line="${line%$'\r'}"
    if [[ -z "${line}" || "${line}" == \#* ]]; then
      continue
    fi
    if [[ "${line}" != *=* ]]; then
      echo "invalid release env line in ${release_env_file}: ${line}" >&2
      exit 1
    fi
    key="${line%%=*}"
    value="${line#*=}"
    case "${key}" in
      VIRTROID_BACKEND_IMAGE)
        if [ "${seen_image}" -ne 0 ]; then
          echo "duplicate release env key: ${key}" >&2
          exit 1
        fi
        seen_image=1
        ;;
      VIRTROID_BACKEND_IMAGE_ID)
        if [ "${seen_image_id}" -ne 0 ]; then
          echo "duplicate release env key: ${key}" >&2
          exit 1
        fi
        seen_image_id=1
        ;;
      VIRTROID_BACKEND_MANIFEST_DIGEST)
        if [ "${seen_manifest_digest}" -ne 0 ]; then
          echo "duplicate release env key: ${key}" >&2
          exit 1
        fi
        seen_manifest_digest=1
        ;;
      VIRTROID_SOURCE_SHA)
        if [ "${seen_sha}" -ne 0 ]; then
          echo "duplicate release env key: ${key}" >&2
          exit 1
        fi
        seen_sha=1
        ;;
      VIRTROID_SCHEMA_VERSION)
        if [ "${seen_schema}" -ne 0 ]; then
          echo "duplicate release env key: ${key}" >&2
          exit 1
        fi
        seen_schema=1
        ;;
      VIRTROID_DEPLOYMENT_TREE_SHA256)
        if [ "${seen_deployment_tree}" -ne 0 ]; then
          echo "duplicate release env key: ${key}" >&2
          exit 1
        fi
        seen_deployment_tree=1
        ;;
      *)
        echo "unexpected release env key: ${key}" >&2
        exit 1
        ;;
    esac
    export "${key}=${value}"
  done < "${release_env_file}"

  if [ "${seen_image}" -ne 1 ] || [ "${seen_image_id}" -ne 1 ] ||
     [ "${seen_manifest_digest}" -ne 1 ] ||
     [ "${seen_sha}" -ne 1 ] ||
     [ "${seen_schema}" -ne 1 ] || [ "${seen_deployment_tree}" -ne 1 ]; then
    echo "release env must define the local image tag, config ID, manifest digest, source SHA, schema version, and deployment-tree digest exactly once" >&2
    exit 1
  fi
}

require_env() {
  local missing=0
  local key
  for key in "$@"; do
    if [ -z "${!key:-}" ]; then
      echo "missing required env: ${key}" >&2
      missing=1
    fi
  done
  if [ "${missing}" -ne 0 ]; then
    exit 1
  fi
}

profile_enabled() {
  local wanted="$1"
  local profile
  local profile_list
  IFS=',' read -ra profile_list <<< "${profiles}"
  for profile in "${profile_list[@]}"; do
    profile="$(echo "${profile}" | xargs)"
    if [ "${profile}" = "${wanted}" ]; then
      return 0
    fi
  done
  return 1
}

require_digest_image() {
  local key="$1"
  local value="${!key:-}"
  if [[ ! "${value}" =~ ^[^@[:space:]]+@sha256:[0-9a-fA-F]{64}$ ]]; then
    echo "${key} must be a full immutable image reference ending in @sha256:<64 hex characters>" >&2
    return 1
  fi
}

require_generated_hex_secret() {
  local key="$1"
  local value="${!key:-}"
  if [[ ! "${value}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "${key} must be a generated 256-bit lowercase hexadecimal secret" >&2
    return 1
  fi
}

require_p256_private_key() {
  local encoded_key="${NODE_PRIVATE_KEY_B64:-}"
  local temporary
  local canonical
  local description

  [[ "${encoded_key}" =~ ^[A-Za-z0-9+/]+={0,2}$ ]] || {
    echo "NODE_PRIVATE_KEY_B64 must be canonical base64" >&2
    return 1
  }
  command -v openssl >/dev/null 2>&1 || {
    echo "openssl is required to validate NODE_PRIVATE_KEY_B64" >&2
    return 1
  }
  temporary="$(mktemp)"
  if ! printf '%s' "${encoded_key}" | openssl base64 -d -A > "${temporary}" ||
     ! openssl pkey -inform DER -in "${temporary}" -check -noout >/dev/null 2>&1; then
    find "${temporary}" -delete
    echo "NODE_PRIVATE_KEY_B64 is not a valid PKCS8 private key" >&2
    return 1
  fi
  canonical="$(openssl base64 -A -in "${temporary}")"
  description="$(openssl pkey -inform DER -in "${temporary}" -text -noout 2>/dev/null)"
  find "${temporary}" -delete
  if [ "${canonical}" != "${encoded_key}" ]; then
    echo "NODE_PRIVATE_KEY_B64 must use canonical base64 encoding" >&2
    return 1
  fi
  if [[ "${description}" != *"ASN1 OID: prime256v1"* ]] &&
     [[ "${description}" != *"NIST CURVE: P-256"* ]]; then
    echo "NODE_PRIVATE_KEY_B64 must contain a P-256 private key" >&2
    return 1
  fi
}

validate_environment() {
  load_env_file "${env_file}"
  load_release_env
  require_env \
    POSTGRES_PASSWORD \
    NODE_SHARED_SECRET \
    NODE_DEVELOPMENT_ENROLLMENT_ENABLED \
    NODE_PRIVATE_KEY_B64 \
    NODE_ID \
    PUBLIC_BASE_URL \
    PUBLIC_RELAY_URL \
    NODE_ADVERTISE_ADDR \
    NODE_ALLOWED_ADVERTISE_ADDRS \
    BOOTSTRAP_ENABLED \
    DOCKER_GID \
    POSTGRES_IMAGE \
    VIRTROID_BACKEND_IMAGE \
    VIRTROID_BACKEND_IMAGE_ID \
    VIRTROID_BACKEND_MANIFEST_DIGEST \
    VIRTROID_SOURCE_SHA \
    VIRTROID_SCHEMA_VERSION \
    VIRTROID_DEPLOYMENT_TREE_SHA256 \
    NODE_RUNTIME_IMAGE \
    NODE_DOCKER_NETWORK \
    NODE_RUNTIME_NETWORK_MODE \
    NODE_AGENT_CONTAINER_NAME

  require_generated_hex_secret POSTGRES_PASSWORD
  require_generated_hex_secret NODE_SHARED_SECRET
  require_p256_private_key

  local invalid_image=0
  local image_key
  for image_key in POSTGRES_IMAGE NODE_RUNTIME_IMAGE; do
    if ! require_digest_image "${image_key}"; then
      invalid_image=1
    fi
  done
  if profile_enabled edge; then
    require_env HAPROXY_CERT_GID
    if [ "${HOST_API_BIND:-}" != 127.0.0.1 ] || [ "${HOST_RELAY_BIND:-}" != 127.0.0.1 ]; then
      echo "HOST_API_BIND and HOST_RELAY_BIND must both be 127.0.0.1 when the TLS edge profile is enabled" >&2
      exit 1
    fi
    if [[ ! "${HAPROXY_CERT_GID}" =~ ^[0-9]+$ ]]; then
      echo "HAPROXY_CERT_GID must be numeric" >&2
      exit 1
    fi
    if ! require_digest_image HAPROXY_IMAGE; then
      invalid_image=1
    fi
  fi
  if profile_enabled renterd; then
    require_env RENTERD_IMAGE RENTERD_API_PASSWORD RENTERD_SEED NODE_SIA_RENTERD_PASSWORD NODE_SIA_RENTERD_WORKER_URL
    if ! require_digest_image RENTERD_IMAGE; then
      invalid_image=1
    fi
  fi
  if profile_enabled falco && ! require_digest_image FALCO_IMAGE; then
    invalid_image=1
  fi
  if [ "${invalid_image}" -ne 0 ]; then
    exit 1
  fi

  if [[ ! "${VIRTROID_BACKEND_IMAGE}" =~ ^virtroid-backend:release-([0-9a-f]{40})$ ]]; then
    echo "VIRTROID_BACKEND_IMAGE must be a local release tag in the form virtroid-backend:release-<40 lowercase hex>" >&2
    exit 1
  fi
  local image_source_sha="${BASH_REMATCH[1]}"
  if [[ ! "${VIRTROID_SOURCE_SHA}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "VIRTROID_SOURCE_SHA must be a lowercase 40-character Git commit SHA" >&2
    exit 1
  fi
  if [ "${image_source_sha}" != "${VIRTROID_SOURCE_SHA}" ]; then
    echo "VIRTROID_BACKEND_IMAGE tag does not match VIRTROID_SOURCE_SHA" >&2
    exit 1
  fi
  if [[ ! "${VIRTROID_BACKEND_IMAGE_ID}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "VIRTROID_BACKEND_IMAGE_ID must be an exact local Docker image ID" >&2
    exit 1
  fi
  if [[ ! "${VIRTROID_BACKEND_MANIFEST_DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "VIRTROID_BACKEND_MANIFEST_DIGEST must be an exact image manifest digest" >&2
    exit 1
  fi
  if [[ ! "${VIRTROID_SCHEMA_VERSION}" =~ ^[0-9]+$ ]] || [ "${VIRTROID_SCHEMA_VERSION}" -le 0 ]; then
    echo "VIRTROID_SCHEMA_VERSION must be a positive decimal integer" >&2
    exit 1
  fi
  if [[ ! "${VIRTROID_DEPLOYMENT_TREE_SHA256}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "VIRTROID_DEPLOYMENT_TREE_SHA256 must be a lowercase SHA-256 digest" >&2
    exit 1
  fi
  actual_deployment_tree_digest="$("${script_dir}/deployment-tree-digest.sh" "${script_dir}")"
  if [ "${actual_deployment_tree_digest}" != "${VIRTROID_DEPLOYMENT_TREE_SHA256}" ]; then
    echo "installed deployment tree does not match VIRTROID_DEPLOYMENT_TREE_SHA256" >&2
    exit 1
  fi
  if [ "${NODE_DEVELOPMENT_ENROLLMENT_ENABLED}" != false ]; then
    echo "NODE_DEVELOPMENT_ENROLLMENT_ENABLED must be false in production" >&2
    exit 1
  fi

  if [[ ! "${NODE_DOCKER_NETWORK}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$ ]]; then
    echo "NODE_DOCKER_NETWORK must be a simple dedicated Docker network name" >&2
    exit 1
  fi
  case "${NODE_DOCKER_NETWORK}" in
    virtroid_default|default|database|control|blob|monitoring)
      echo "NODE_DOCKER_NETWORK=${NODE_DOCKER_NETWORK} is not an isolated guest network" >&2
      exit 1
      ;;
  esac

  if [ "${NODE_RUNTIME_NETWORK_MODE}" != "per-runtime" ]; then
    echo "NODE_RUNTIME_NETWORK_MODE must be per-runtime for production deployment" >&2
    exit 1
  fi
  if [ "${NODE_AGENT_CONTAINER_NAME}" != "virtnoded" ]; then
    echo "NODE_AGENT_CONTAINER_NAME must match the Compose node container name: virtnoded" >&2
    exit 1
  fi
  if [ "${BOOTSTRAP_ENABLED}" != "false" ]; then
    echo "BOOTSTRAP_ENABLED must remain false until production bootstrap has a durable invite or billing gate" >&2
    exit 1
  fi
  if [ "${NODE_ALLOWED_ADVERTISE_ADDRS}" != "${NODE_ADVERTISE_ADDR}" ]; then
    echo "NODE_ALLOWED_ADVERTISE_ADDRS must exactly match NODE_ADVERTISE_ADDR for this single-node Compose deployment" >&2
    exit 1
  fi

  case "${NODE_BLOB_STORE_KIND:-local-disk}" in
    local-disk)
      if profile_enabled renterd; then
        echo "the renterd profile requires NODE_BLOB_STORE_KIND=sia-renterd" >&2
        exit 1
      fi
      if [ -n "${NODE_SIA_RENTERD_WORKER_URL:-}" ]; then
        echo "NODE_SIA_RENTERD_WORKER_URL must be empty in local-disk mode" >&2
        exit 1
      fi
      ;;
    sia-renterd)
      if ! profile_enabled renterd; then
        echo "NODE_BLOB_STORE_KIND=sia-renterd requires the renterd Compose profile" >&2
        exit 1
      fi
      if [ "${NODE_SIA_RENTERD_WORKER_URL}" != "http://renterd:9980" ]; then
        echo "the Compose renterd worker URL must be http://renterd:9980" >&2
        exit 1
      fi
      if [ "${NODE_SIA_RENTERD_PASSWORD}" != "${RENTERD_API_PASSWORD}" ]; then
        echo "NODE_SIA_RENTERD_PASSWORD must match RENTERD_API_PASSWORD for the local Compose renterd service" >&2
        exit 1
      fi
      if [[ ! "${NODE_SIA_RENTERD_BUCKET}" =~ ^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$ ]]; then
        echo "NODE_SIA_RENTERD_BUCKET must be a private S3-compatible bucket name" >&2
        exit 1
      fi
      if [[ ! "${NODE_SIA_RENTERD_MIN_SHARDS}" =~ ^[1-9][0-9]*$ ]] ||
         [[ ! "${NODE_SIA_RENTERD_TOTAL_SHARDS}" =~ ^[1-9][0-9]*$ ]] ||
         [ "${NODE_SIA_RENTERD_MIN_SHARDS}" -gt "${NODE_SIA_RENTERD_TOTAL_SHARDS}" ]; then
        echo "NODE_SIA_RENTERD_MIN_SHARDS and NODE_SIA_RENTERD_TOTAL_SHARDS must be explicit positive integers with min <= total" >&2
        exit 1
      fi
      ;;
    *)
      echo "unsupported NODE_BLOB_STORE_KIND=${NODE_BLOB_STORE_KIND}" >&2
      exit 1
      ;;
  esac

  if [ "${APP_CATALOG_SYNC_ENABLED:-false}" = "true" ] && [ -z "${APP_CATALOG_SYNC_SHA256:-}" ]; then
    echo "APP_CATALOG_SYNC_SHA256 is required when APP_CATALOG_SYNC_ENABLED=true" >&2
    exit 1
  fi
}

validate_local_backend_image() {
  local actual_id architecture operating_system revision schema tree
  actual_id="$(docker image inspect --format '{{.Id}}' "${VIRTROID_BACKEND_IMAGE}" 2>/dev/null)" || {
    echo "local backend image is not loaded: ${VIRTROID_BACKEND_IMAGE}" >&2
    return 1
  }
  if [ "${actual_id}" != "${VIRTROID_BACKEND_IMAGE_ID}" ] &&
     [ "${actual_id}" != "${VIRTROID_BACKEND_MANIFEST_DIGEST}" ]; then
    echo "local backend tag resolves to unapproved engine identity ${actual_id}" >&2
    return 1
  fi
  architecture="$(docker image inspect --format '{{.Architecture}}' "${actual_id}")"
  operating_system="$(docker image inspect --format '{{.Os}}' "${actual_id}")"
  revision="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "${actual_id}")"
  schema="$(docker image inspect --format '{{index .Config.Labels "com.virtroid.schema-version"}}' "${actual_id}")"
  tree="$(docker image inspect --format '{{index .Config.Labels "com.virtroid.deployment-tree-sha256"}}' "${actual_id}")"
  if [ "${operating_system}/${architecture}" != linux/amd64 ]; then
    echo "local backend image must target linux/amd64, got ${operating_system}/${architecture}" >&2
    return 1
  fi
  if [ "${revision}" != "${VIRTROID_SOURCE_SHA}" ] ||
     [ "${schema}" != "${VIRTROID_SCHEMA_VERSION}" ] ||
     [ "${tree}" != "${VIRTROID_DEPLOYMENT_TREE_SHA256}" ]; then
    echo "local backend image labels do not match the protected release state" >&2
    return 1
  fi
}

acquire_maintenance_lock() {
  local inherited_lock
  if [ "$(id -u)" -ne 0 ]; then
    echo "mutating deployment commands must run as root" >&2
    exit 1
  fi
  if [ "$(readlink -f -- "$0")" != /opt/virtroid/deploy/vps/deploy.sh ]; then
    echo "run the installed deployment entrypoint: /opt/virtroid/deploy/vps/deploy.sh" >&2
    exit 1
  fi
  if [ ! -f "$0" ] || [ -L "$0" ] || [ "$(stat -c '%u' "$0")" -ne 0 ] ||
     (( (8#$(stat -c '%a' "$0") & 0022) != 0 )); then
    echo "installed deployment entrypoint is not a trusted root-owned file" >&2
    exit 1
  fi
  install -d -o root -g root -m 0700 /var/lib/virtroid-deploy
  case "${VIRTROID_MAINTENANCE_LOCK_HELD:-0}" in
    0)
      exec 8>/var/lib/virtroid-deploy/maintenance.lock
      if ! flock -n 8; then
        echo "another Virtroid maintenance operation is running" >&2
        exit 75
      fi
      ;;
    1)
      inherited_lock="$(readlink -f "/proc/$$/fd/8" 2>/dev/null || true)"
      if [ "${inherited_lock}" != /var/lib/virtroid-deploy/maintenance.lock ] || ! flock -n 8; then
        echo "inherited Virtroid maintenance lock is missing or invalid" >&2
        exit 1
      fi
      ;;
    *)
      echo "VIRTROID_MAINTENANCE_LOCK_HELD must be 0 or 1" >&2
      exit 1
      ;;
  esac
}

validate_host() {
  validate_environment
  if [ ! -S /var/run/docker.sock ]; then
    echo "missing Docker socket: /var/run/docker.sock" >&2
    exit 1
  fi
  validate_local_backend_image
  if [ ! -e /dev/binderfs/binder ] || [ ! -e /dev/binderfs/hwbinder ] || [ ! -e /dev/binderfs/vndbinder ]; then
    echo "binderfs is not ready. Run sudo ./prepare-redroid-host.sh first." >&2
    exit 1
  fi
  if profile_enabled edge; then
    if [ ! -r "${HAPROXY_CERT_PATH:-/srv/virtroid/tls/virtroid.pem}" ]; then
      echo "missing HAProxy PEM bundle: ${HAPROXY_CERT_PATH:-/srv/virtroid/tls/virtroid.pem}" >&2
      exit 1
    fi
  fi
}

wait_for_health() {
  local url="$1"
  shift
  local attempt response
  for attempt in $(seq 1 30); do
    if response="$(curl -fsS --connect-timeout 2 --max-time 5 "$@" "${url}" 2>/dev/null)" &&
       [[ "${response}" =~ \"ok\"[[:space:]]*:[[:space:]]*true ]]; then
      printf '%s\n' "${response}"
      return 0
    fi
    sleep 1
  done
  echo "health check did not return an explicit ok=true response within the retry window: ${url}" >&2
  response="$(curl -fsS --connect-timeout 2 --max-time 5 "$@" "${url}")" || return 1
  [[ "${response}" =~ \"ok\"[[:space:]]*:[[:space:]]*true ]] || {
    echo "health endpoint returned a non-ready response: ${url}" >&2
    return 1
  }
  printf '%s\n' "${response}"
}

health() {
  local public_authority public_hostname public_port
  wait_for_health http://127.0.0.1:8080/healthz
  if profile_enabled edge; then
    if [[ ! "${PUBLIC_BASE_URL}" =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:[0-9]{1,5})?$ ]]; then
      echo "PUBLIC_BASE_URL is not an approved HTTPS origin" >&2
      return 1
    fi
    public_authority="${PUBLIC_BASE_URL#https://}"
    public_hostname="${public_authority%%:*}"
    if [[ "${public_authority}" == *:* ]]; then
      public_port="${public_authority##*:}"
    else
      public_port=443
    fi
    if [ "$((10#${public_port}))" -lt 1 ] || [ "$((10#${public_port}))" -gt 65535 ]; then
      echo "PUBLIC_BASE_URL contains an invalid port" >&2
      return 1
    fi
    wait_for_health "${PUBLIC_BASE_URL%/}/healthz" \
      --noproxy '*' --proto '=https' --tlsv1.2 \
      --resolve "${public_hostname}:${public_port}:127.0.0.1"
  fi
}

cd "${script_dir}"

core_services=(postgres virtroidd virtnoded)
pull_services=(postgres)
if profile_enabled edge; then
  core_services+=(edge)
  pull_services+=(edge)
fi
if profile_enabled renterd; then
  core_services+=(renterd)
  pull_services+=(renterd)
fi
if profile_enabled falco; then
  core_services+=(falco-forwarder falco)
  pull_services+=(falco)
fi

case "${command_name}" in
  validate)
    validate_environment
    echo "deployment environment validation passed"
    ;;
  up)
    acquire_maintenance_lock
    validate_host
    compose pull "${pull_services[@]}"
    compose up -d --no-build --pull never "${core_services[@]}"
    health
    compose ps
    ;;
  pull)
    acquire_maintenance_lock
    validate_host
    compose pull "${pull_services[@]}"
    ;;
  restart)
    acquire_maintenance_lock
    validate_host
    restart_services=(virtroidd virtnoded)
    if profile_enabled edge; then
      restart_services+=(edge)
    fi
    if profile_enabled renterd; then
      restart_services+=(renterd)
    fi
    if profile_enabled falco; then
      restart_services+=(falco-forwarder falco)
    fi
    compose up -d --no-build --pull never --force-recreate "${restart_services[@]}"
    health
    ;;
  logs)
    compose logs -f --tail=200 "${@:2}"
    ;;
  ps)
    compose ps
    ;;
  health)
    validate_environment
    health
    ;;
  down)
    acquire_maintenance_lock
    compose down
    ;;
  *)
    usage
    exit 1
    ;;
esac
