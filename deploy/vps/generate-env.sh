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
if [[ "${public_url}" != https://* ]]; then
  echo "PUBLIC_BASE_URL must be HTTPS, got: ${public_url}" >&2
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

node_private_key_b64() {
  tmp_key="$(mktemp)"
  trap 'rm -f "${tmp_key}"' RETURN
  openssl ecparam -name prime256v1 -genkey -noout -out "${tmp_key}"
  openssl pkcs8 -topk8 -nocrypt -in "${tmp_key}" -outform DER | base64 | tr -d '\n'
}

default_node_id="$(hostname -s | tr -c '[:alnum:]-' '-' | sed -E 's/^-+|-+$//g')"
node_id="${2:-${default_node_id:-virtroid-node}}"
docker_gid="$(getent group docker 2>/dev/null | cut -d: -f3 || true)"
docker_gid="${docker_gid:-998}"

cat > "${env_file}" <<EOF
COMPOSE_PROJECT_NAME=virtroid
POSTGRES_DB=virtroid
POSTGRES_USER=virtroid
POSTGRES_PASSWORD=$(random_hex)
NODE_SHARED_SECRET=$(random_hex)
NODE_REGISTRATION_SECRET=$(random_hex)
NODE_PRIVATE_KEY_B64=$(node_private_key_b64)
BOOTSTRAP_RATE_LIMIT_PER_MINUTE=5
BOOTSTRAP_MAX_BODY_BYTES=32768
PUBLIC_BASE_URL=${public_url}
PUBLIC_RELAY_URL=${public_url}
NODE_ADVERTISE_ADDR=virtnoded
HOST_API_BIND=127.0.0.1
HOST_RELAY_BIND=127.0.0.1
NODE_ID=${node_id}
NODE_NAME=${node_id}
HAPROXY_CERT_PATH=/srv/virtroid/tls/virtroid.pem
DOCKER_GID=${docker_gid}
NODE_DOCKER_NETWORK=virtroid_default

NODE_BLOB_STORE_KIND=local-disk
NODE_SIA_RENTERD_WORKER_URL=http://renterd:9980
NODE_SIA_RENTERD_PASSWORD=
NODE_SIA_RENTERD_BUCKET=virtroid
NODE_SIA_RENTERD_MIN_SHARDS=
NODE_SIA_RENTERD_TOTAL_SHARDS=
NODE_SIA_RENTERD_CONTRACT_SET=

RENTERD_API_PASSWORD=
RENTERD_SEED=
RENTERD_LOG_LEVEL=info
EOF

chmod 600 "${env_file}"
echo "Wrote ${env_file}"
echo "Next: put a PEM bundle at /srv/virtroid/tls/virtroid.pem, then run ./deploy.sh"
