#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  ./generate-env.sh https://your-domain.example [node-id]

Creates deploy/vps/.env with generated passwords, node signing key material,
Docker group id, and sane single-node defaults. Existing .env files are not
overwritten unless VIRTROID_FORCE_ENV=1 is set.
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

public_url="${1:-}"
if [ -z "${public_url}" ]; then
  usage
  exit 1
fi

public_url="${public_url%/}"
public_url_pattern='^https://[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:[0-9]{1,5})?$'
if [[ ! "${public_url}" =~ ${public_url_pattern} ]]; then
  echo "PUBLIC_BASE_URL must be an HTTPS origin such as https://virtroid.example" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="${script_dir}/.env"

if [ -e "${env_file}" ] && [ "${VIRTROID_FORCE_ENV:-0}" != "1" ]; then
  echo "${env_file} already exists. Set VIRTROID_FORCE_ENV=1 to overwrite." >&2
  exit 1
fi

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require_cmd openssl
require_cmd base64
require_cmd tr
require_cmd hostname

random_hex() {
  openssl rand -hex 32
}

p256_private_key_b64() {
  tmp_key="$(mktemp)"
  trap 'rm -f "${tmp_key}"' RETURN
  openssl ecparam -name prime256v1 -genkey -noout -out "${tmp_key}"
  openssl pkcs8 -topk8 -nocrypt -in "${tmp_key}" -outform DER | base64 | tr -d '\n'
}

p256_public_key_b64_from_private() {
  local encoded_private="$1"
  local tmp_key
  tmp_key="$(mktemp)"
  trap 'rm -f "${tmp_key}"' RETURN
  printf '%s' "${encoded_private}" | openssl base64 -d -A > "${tmp_key}"
  openssl pkey -inform DER -in "${tmp_key}" -pubout -outform DER | base64 | tr -d '\n'
}

default_node_id="$(hostname -s | tr -c '[:alnum:]-' '-' | sed -E 's/^-+|-+$//g')"
node_id="${2:-${default_node_id:-virtroid-node}}"
node_id_pattern='^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$'
if [[ ! "${node_id}" =~ ${node_id_pattern} ]]; then
  echo "node-id must be 1-63 characters using only letters, numbers, dot, underscore, or hyphen" >&2
  exit 1
fi
docker_gid="$(getent group docker 2>/dev/null | cut -d: -f3 || true)"
docker_gid="${docker_gid:-998}"
if [[ ! "${docker_gid}" =~ ^[0-9]+$ ]]; then
  echo "DOCKER_GID must be numeric, got: ${docker_gid}" >&2
  exit 1
fi
haproxy_cert_gid="$(getent group virtroid-cert 2>/dev/null | cut -d: -f3 || true)"
haproxy_cert_gid="${HAPROXY_CERT_GID:-${haproxy_cert_gid:-99}}"
if [[ ! "${haproxy_cert_gid}" =~ ^[0-9]+$ ]]; then
  echo "HAPROXY_CERT_GID must be numeric, got: ${haproxy_cert_gid}" >&2
  exit 1
fi
node_private_key="$(p256_private_key_b64)"
control_plane_callback_private_key="$(p256_private_key_b64)"
control_plane_callback_public_key="$(p256_public_key_b64_from_private "${control_plane_callback_private_key}")"

write_env_var() {
  printf '%s=%s\n' "$1" "$2"
}

{
  write_env_var COMPOSE_PROJECT_NAME virtroid
  write_env_var POSTGRES_IMAGE "${POSTGRES_IMAGE:-postgres:18@sha256:REPLACE_WITH_64_HEX}"
  write_env_var GO_BUILD_IMAGE "${GO_BUILD_IMAGE:-golang:1.26.5-bookworm@sha256:REPLACE_WITH_64_HEX}"
  write_env_var BACKEND_RUNTIME_IMAGE "${BACKEND_RUNTIME_IMAGE:-alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40}"
  write_env_var NODE_RUNTIME_IMAGE "${NODE_RUNTIME_IMAGE:-redroid/redroid:14.0.0_64only-latest@sha256:REPLACE_WITH_64_HEX}"
  write_env_var HAPROXY_IMAGE "${HAPROXY_IMAGE:-haproxy:lts@sha256:REPLACE_WITH_64_HEX}"
  write_env_var RENTERD_IMAGE "${RENTERD_IMAGE:-ghcr.io/siafoundation/renterd:2.9.3@sha256:72c589044aa47a09ab8349aad8b639a9cde2dd44bf27df73255d0f7bb29a2316}"
  write_env_var RENTERD_MYSQL_IMAGE "${RENTERD_MYSQL_IMAGE:-mysql:8.4@sha256:c592c15aaf4a1961e15d82eb31ea5987dda862d1c4b1e93424438c0e91dc1f8d}"
  write_env_var FALCO_IMAGE "${FALCO_IMAGE:-falcosecurity/falco:0.43.0@sha256:REPLACE_WITH_64_HEX}"
  write_env_var PROMETHEUS_IMAGE "${PROMETHEUS_IMAGE:-prom/prometheus:v3.13.0@sha256:c6b27ea434f8389bfe233fbc7be381cf50587c286e871bc842008f5a1b1908a7}"
  write_env_var ALERTMANAGER_IMAGE "${ALERTMANAGER_IMAGE:-prom/alertmanager:v0.32.1@sha256:51a825c2a40acc3e338fdd00d622e01ec090f72be2b3ea46be0839cd47a4d286}"
  write_env_var POSTGRES_DB virtroid
  write_env_var POSTGRES_USER virtroid
  write_env_var POSTGRES_PASSWORD "$(random_hex)"
  write_env_var NODE_DEVELOPMENT_ENROLLMENT_ENABLED false
  write_env_var NODE_PRIVATE_KEY_B64 "${node_private_key}"
  write_env_var CONTROL_PLANE_CALLBACK_PRIVATE_KEY_B64 "${control_plane_callback_private_key}"
  write_env_var CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64 "${control_plane_callback_public_key}"
  write_env_var BOOTSTRAP_ENABLED true
  write_env_var BOOTSTRAP_REQUIRE_INVITE true
  write_env_var BOOTSTRAP_RATE_LIMIT_PER_MINUTE 5
  write_env_var BOOTSTRAP_MAX_BODY_BYTES 32768
  write_env_var RUNTIME_LOG_RETENTION 720h
  write_env_var TRUST_PROXY_HEADERS true
  write_env_var PUBLIC_BASE_URL "${public_url}"
  write_env_var PUBLIC_RELAY_URL "${public_url}"
  write_env_var NODE_ADVERTISE_ADDR virtnoded
  write_env_var NODE_ALLOWED_ADVERTISE_ADDRS virtnoded
  write_env_var HOST_API_BIND 127.0.0.1
  write_env_var HOST_RELAY_BIND 127.0.0.1
  write_env_var NODE_ID "${node_id}"
  write_env_var NODE_NAME "${node_id}"
  write_env_var HAPROXY_CERT_PATH /srv/virtroid/tls/virtroid.pem
  write_env_var HAPROXY_CERT_GID "${haproxy_cert_gid}"
  write_env_var DOCKER_GID "${docker_gid}"
  write_env_var NODE_DOCKER_NETWORK virtroid-guests
  write_env_var NODE_RUNTIME_NETWORK_MODE per-runtime
  write_env_var NODE_AGENT_CONTAINER_NAME virtnoded
  write_env_var NODE_RUNTIME_MEMORY_BYTES 4294967296
  write_env_var NODE_RUNTIME_NANO_CPUS 2000000000
  write_env_var NODE_RUNTIME_PIDS_LIMIT 4096
  write_env_var NODE_RUNTIME_SHM_BYTES 268435456
  write_env_var NODE_MIN_FREE_DISK_BYTES 10737418240
  write_env_var NODE_MIN_FREE_DISK_PERCENT 5

  write_env_var APP_CATALOG_SYNC_ENABLED false
  write_env_var APP_CATALOG_SYNC_URL https://f-droid.org/repo/index-v2.json
  write_env_var APP_CATALOG_SYNC_SHA256 ""
  write_env_var APP_CATALOG_SYNC_INTERVAL 12h
  write_env_var APP_CATALOG_SYNC_MAX_APPS 1500

  write_env_var NODE_BLOB_STORE_KIND local-disk
  write_env_var NODE_SIA_RENTERD_WORKER_URL ""
  write_env_var NODE_SIA_RENTERD_BUCKET virtroid
  write_env_var NODE_SIA_RENTERD_WALLET_ADDRESS ""
  write_env_var NODE_SIA_RENTERD_MIN_SHARDS 10
  write_env_var NODE_SIA_RENTERD_TOTAL_SHARDS 30
  write_env_var NODE_SIA_RENTERD_CONTRACT_SET ""
  write_env_var NODE_BLOB_PREFLIGHT_INTERVAL 5m

  write_env_var RENTERD_CONFIG_FILE /etc/virtroid/secrets/renterd.yml
  write_env_var RENTERD_API_PASSWORD_FILE /etc/virtroid/secrets/renterd-api-password
  write_env_var RENTERD_MYSQL_PASSWORD_FILE /etc/virtroid/secrets/renterd-mysql-password
  write_env_var RENTERD_MYSQL_ROOT_PASSWORD_FILE /etc/virtroid/secrets/renterd-mysql-root-password
  write_env_var RENTERD_LOG_LEVEL info
} > "${env_file}"

chmod 600 "${env_file}"
echo "Wrote ${env_file}"
echo "Next: replace every REPLACE_WITH_64_HEX image digest, install the TLS PEM bundle, then run ./deploy.sh"
