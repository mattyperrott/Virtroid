#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="${VIRTROID_ENV_FILE:-${script_dir}/.env}"
profiles="${VIRTROID_PROFILES:-edge}"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./deploy.sh [validate|up|build|restart|logs|ps|health|down]

Environment:
  VIRTROID_ENV_FILE   Path to .env file. Defaults to deploy/vps/.env.
  VIRTROID_PROFILES   Comma-separated compose profiles. Defaults to edge.

Typical first deploy:
  sudo ./prepare-redroid-host.sh
  ./generate-env.sh https://your-domain.example
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

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is not installed. Run sudo ./prepare-redroid-host.sh first." >&2
  exit 1
fi

compose() {
  local args=(compose --env-file "${env_file}")
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

load_env() {
  local line key value
  while IFS= read -r line || [ -n "${line}" ]; do
    line="${line%$'\r'}"
    if [[ -z "${line}" || "${line}" == \#* ]]; then
      continue
    fi
    if [[ "${line}" != *=* ]]; then
      echo "invalid env line in ${env_file}: ${line}" >&2
      exit 1
    fi
    key="${line%%=*}"
    value="${line#*=}"
    if [[ ! "${key}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "invalid env key in ${env_file}: ${key}" >&2
      exit 1
    fi
    export "${key}=${value}"
  done < "${env_file}"
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

validate_environment() {
  load_env
  require_env \
    POSTGRES_PASSWORD \
    NODE_SHARED_SECRET \
    NODE_PRIVATE_KEY_B64 \
    PUBLIC_BASE_URL \
    PUBLIC_RELAY_URL \
    NODE_ADVERTISE_ADDR \
    NODE_ALLOWED_ADVERTISE_ADDRS \
    BOOTSTRAP_ENABLED \
    DOCKER_GID \
    POSTGRES_IMAGE \
    GO_BUILD_IMAGE \
    BACKEND_RUNTIME_IMAGE \
    NODE_RUNTIME_IMAGE \
    NODE_DOCKER_NETWORK \
    NODE_RUNTIME_NETWORK_MODE \
    NODE_AGENT_CONTAINER_NAME

  local invalid_image=0
  local image_key
  for image_key in POSTGRES_IMAGE GO_BUILD_IMAGE BACKEND_RUNTIME_IMAGE NODE_RUNTIME_IMAGE; do
    if ! require_digest_image "${image_key}"; then
      invalid_image=1
    fi
  done
  if profile_enabled edge; then
    require_env HAPROXY_CERT_GID
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

validate_host() {
  validate_environment
  if [ ! -S /var/run/docker.sock ]; then
    echo "missing Docker socket: /var/run/docker.sock" >&2
    exit 1
  fi
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
    if response="$(curl -fsS "$@" "${url}" 2>/dev/null)"; then
      printf '%s\n' "${response}"
      return 0
    fi
    sleep 1
  done
  echo "health check did not become ready within 30 seconds: ${url}" >&2
  curl -fsS "$@" "${url}"
}

health() {
  wait_for_health http://127.0.0.1:8080/healthz
  if profile_enabled edge; then
    wait_for_health https://127.0.0.1/healthz -k
  fi
}

cd "${script_dir}"

core_services=(postgres virtroidd virtnoded)
if profile_enabled edge; then
  core_services+=(edge)
fi
if profile_enabled renterd; then
  core_services+=(renterd)
fi
if profile_enabled falco; then
  core_services+=(falco-forwarder falco)
fi

case "${command_name}" in
  validate)
    validate_environment
    echo "deployment environment validation passed"
    ;;
  up)
    validate_host
    compose up -d --build "${core_services[@]}"
    health
    compose ps
    ;;
  build)
    validate_host
    compose build virtroidd virtnoded
    ;;
  restart)
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
    compose up -d --force-recreate "${restart_services[@]}"
    health
    ;;
  logs)
    compose logs -f --tail=200 "${@:2}"
    ;;
  ps)
    compose ps
    ;;
  health)
    health
    ;;
  down)
    compose down
    ;;
  *)
    usage
    exit 1
    ;;
esac
