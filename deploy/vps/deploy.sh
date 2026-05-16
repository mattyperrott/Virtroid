#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="${VIRTROID_ENV_FILE:-${script_dir}/.env}"
profiles="${VIRTROID_PROFILES:-edge}"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./deploy.sh [up|build|restart|logs|ps|health|down]

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

validate_host() {
  load_env
  require_env \
    POSTGRES_PASSWORD \
    NODE_SHARED_SECRET \
    NODE_PRIVATE_KEY_B64 \
    PUBLIC_BASE_URL \
    PUBLIC_RELAY_URL \
    NODE_ADVERTISE_ADDR \
    DOCKER_GID

  if [ ! -S /var/run/docker.sock ]; then
    echo "missing Docker socket: /var/run/docker.sock" >&2
    exit 1
  fi
  if [ ! -e /dev/binderfs/binder ] || [ ! -e /dev/binderfs/hwbinder ] || [ ! -e /dev/binderfs/vndbinder ]; then
    echo "binderfs is not ready. Run sudo ./prepare-redroid-host.sh first." >&2
    exit 1
  fi
  if [[ "${profiles}" == *edge* ]]; then
    if [ ! -r "${HAPROXY_CERT_PATH:-/srv/virtroid/tls/virtroid.pem}" ]; then
      echo "missing HAProxy PEM bundle: ${HAPROXY_CERT_PATH:-/srv/virtroid/tls/virtroid.pem}" >&2
      exit 1
    fi
  fi
}

health() {
  curl -fsS http://127.0.0.1:8080/healthz
  printf '\n'
  if [[ "${profiles}" == *edge* ]]; then
    curl -k -fsS https://127.0.0.1/healthz
    printf '\n'
  fi
}

cd "${script_dir}"

core_services=(postgres virtroidd virtnoded)
if [[ "${profiles}" == *edge* ]]; then
  core_services+=(edge)
fi

case "${command_name}" in
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
    if [[ "${profiles}" == *edge* ]]; then
      restart_services+=(edge)
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
