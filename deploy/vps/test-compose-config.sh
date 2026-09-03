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
deployment_tree_digest="$("${script_dir}/deployment-tree-digest.sh" "${script_dir}")"
env_file="${tmp_dir}/compose.env"
release_env_file="${tmp_dir}/release.env"
sed \
  -e "s/REPLACE_WITH_64_HEX/${digest}/g" \
  -e 's|PUBLIC_BASE_URL=https://your-domain.example|PUBLIC_BASE_URL=https://virtroid.example|' \
  -e 's|PUBLIC_RELAY_URL=https://your-domain.example|PUBLIC_RELAY_URL=https://virtroid.example|' \
  "${script_dir}/.env.example" > "${env_file}"
printf '%s\n' \
  'VIRTROID_BACKEND_IMAGE=virtroid-backend:release-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
  "VIRTROID_BACKEND_IMAGE_ID=sha256:${digest}" \
  "VIRTROID_BACKEND_MANIFEST_DIGEST=sha256:${digest}" \
  'VIRTROID_SOURCE_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
  'VIRTROID_SCHEMA_VERSION=2026071903' \
  "VIRTROID_DEPLOYMENT_TREE_SHA256=${deployment_tree_digest}" \
  > "${release_env_file}"

compose_args=(
  compose
  --env-file "${env_file}"
  --env-file "${release_env_file}"
  -f "${script_dir}/docker-compose.yml"
  --profile edge
  --profile renterd
  --profile falco
  --profile monitoring
)

docker "${compose_args[@]}" config --quiet
docker "${compose_args[@]}" config > "${tmp_dir}/rendered-compose.yml"

core_env_file="${tmp_dir}/core-compose.env"
sed \
  -e '/^HAPROXY_IMAGE=/d' \
  -e '/^RENTERD_IMAGE=/d' \
  -e '/^FALCO_IMAGE=/d' \
  -e '/^PROMETHEUS_IMAGE=/d' \
  -e '/^ALERTMANAGER_IMAGE=/d' \
  "${env_file}" > "${core_env_file}"
docker compose \
  --env-file "${core_env_file}" \
  --env-file "${release_env_file}" \
  -f "${script_dir}/docker-compose.yml" \
  config --quiet

grep -q 'BOOTSTRAP_ENABLED: "true"' "${tmp_dir}/rendered-compose.yml"
grep -q 'BOOTSTRAP_REQUIRE_INVITE: "true"' "${tmp_dir}/rendered-compose.yml"
grep -q 'BOOTSTRAP_AUTO_ISSUE_INVITE: "true"' "${tmp_dir}/rendered-compose.yml"
grep -q 'RUNTIME_LOG_RETENTION: 720h' "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_ALLOWED_ADVERTISE_ADDRS: virtnoded' "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_RUNTIME_NETWORK_MODE: per-runtime' "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_MIN_FREE_DISK_BYTES: "10737418240"' "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_MIN_FREE_DISK_PERCENT: "5"' "${tmp_dir}/rendered-compose.yml"
grep -q 'RUNTIME_NOTIFICATION_RATE_LIMIT_PER_MINUTE: "120"' "${tmp_dir}/rendered-compose.yml"
grep -q "NODE_NOTIFICATION_AGENT_APK_SHA256: ${digest}" "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_PUBLIC_CONTROL_PLANE_URL: https://virtroid.example' "${tmp_dir}/rendered-compose.yml"
grep -q 'container_name: virtroid-prometheus' "${tmp_dir}/rendered-compose.yml"
grep -q 'container_name: virtroid-alertmanager' "${tmp_dir}/rendered-compose.yml"
grep -q 'monitoring-egress:' "${tmp_dir}/rendered-compose.yml"
if grep -q -- '--web.enable-lifecycle=false' "${tmp_dir}/rendered-compose.yml"; then
  echo "Prometheus must rely on its default-disabled lifecycle API" >&2
  exit 1
fi
grep -q 'image: virtroid-backend:release-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' "${tmp_dir}/rendered-compose.yml"
grep -q 'com.virtroid.source-sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' "${tmp_dir}/rendered-compose.yml"
grep -q 'com.virtroid.schema-version: "2026071903"' "${tmp_dir}/rendered-compose.yml"
grep -q 'virtroid-admin:' "${tmp_dir}/rendered-compose.yml"
grep -q 'entrypoint:' "${tmp_dir}/rendered-compose.yml"
grep -q 'no-new-privileges:true' "${tmp_dir}/rendered-compose.yml"
grep -q 'RENTERD_CONFIG_FILE: /run/secrets/renterd.yml' "${tmp_dir}/rendered-compose.yml"
grep -q 'NODE_SIA_RENTERD_PASSWORD_FILE: /run/secrets/renterd-api-password' "${tmp_dir}/rendered-compose.yml"
grep -q 'MYSQL_ROOT_PASSWORD_FILE: /run/secrets/renterd-mysql-root-password' "${tmp_dir}/rendered-compose.yml"
grep -q 'renterd-database:' "${tmp_dir}/rendered-compose.yml"
if grep -Eq 'RENTERD_(SEED|API_PASSWORD):|NODE_SIA_RENTERD_PASSWORD:' "${tmp_dir}/rendered-compose.yml"; then
  echo "rendered Compose contains renterd secrets in environment metadata" >&2
  exit 1
fi
if grep -q '^    build:' "${tmp_dir}/rendered-compose.yml"; then
  echo "production Compose still contains an on-host image build" >&2
  exit 1
fi
if [ "$(grep -c 'driver: local' "${tmp_dir}/rendered-compose.yml")" -lt 7 ]; then
  echo "one or more services are missing bounded local logging" >&2
  exit 1
fi
if grep -Eq '^[[:space:]]+max-file:[[:space:]]+"?1"?$' "${tmp_dir}/rendered-compose.yml"; then
  echo "the local logging driver cannot combine compression with max-file=1" >&2
  exit 1
fi

echo "compose configuration checks passed"
