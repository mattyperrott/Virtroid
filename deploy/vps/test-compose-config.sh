#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

if ! docker compose version >/dev/null 2>&1; then
  echo "docker compose is required for deployment configuration validation" >&2
  exit 1
fi

digest="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
env_file="${tmp_dir}/compose.env"
sed \
  -e "s/REPLACE_WITH_64_HEX/${digest}/g" \
  -e 's|PUBLIC_BASE_URL=https://your-domain.example|PUBLIC_BASE_URL=https://virtroid.example|' \
  -e 's|PUBLIC_RELAY_URL=https://your-domain.example|PUBLIC_RELAY_URL=https://virtroid.example|' \
  "${script_dir}/.env.example" > "${env_file}"

compose_args=(
  compose
  --env-file "${env_file}"
  -f "${script_dir}/docker-compose.yml"
  --profile edge
  --profile renterd
  --profile falco
)

docker "${compose_args[@]}" config --quiet
docker "${compose_args[@]}" config > "${tmp_dir}/rendered-compose.yml"

core_env_file="${tmp_dir}/core-compose.env"
sed \
  -e '/^HAPROXY_IMAGE=/d' \
  -e '/^RENTERD_IMAGE=/d' \
  -e '/^FALCO_IMAGE=/d' \
  "${env_file}" > "${core_env_file}"
docker compose \
  --env-file "${core_env_file}" \
  -f "${script_dir}/docker-compose.yml" \
  config --quiet

grep -q 'BOOTSTRAP_ENABLED: "false"' "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_ALLOWED_ADVERTISE_ADDRS: virtnoded' "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_RUNTIME_NETWORK_MODE: per-runtime' "${tmp_dir}/rendered-compose.yml"
if [ "$(grep -c 'driver: local' "${tmp_dir}/rendered-compose.yml")" -lt 7 ]; then
  echo "one or more services are missing bounded local logging" >&2
  exit 1
fi

echo "compose configuration checks passed"
