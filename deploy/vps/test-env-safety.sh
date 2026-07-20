#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

cp "${script_dir}/generate-env.sh" "${tmp_dir}/generate-env.sh"

if VIRTROID_FORCE_ENV=1 "${tmp_dir}/generate-env.sh" 'https://$(touch /tmp/virtroid-env-pwned)' >/dev/null 2>&1; then
  echo "malicious PUBLIC_BASE_URL was accepted" >&2
  exit 1
fi

if VIRTROID_FORCE_ENV=1 "${tmp_dir}/generate-env.sh" https://virtroid.example $'node-1\nid' >/dev/null 2>&1; then
  echo "malicious node-id was accepted" >&2
  exit 1
fi

VIRTROID_FORCE_ENV=1 "${tmp_dir}/generate-env.sh" https://virtroid.example node-1 >/dev/null
grep -q '^PUBLIC_BASE_URL=https://virtroid.example$' "${tmp_dir}/.env"
grep -q '^NODE_ID=node-1$' "${tmp_dir}/.env"
grep -q '^NODE_RUNTIME_NETWORK_MODE=per-runtime$' "${tmp_dir}/.env"
grep -q '^NODE_AGENT_CONTAINER_NAME=virtnoded$' "${tmp_dir}/.env"
grep -q '^BOOTSTRAP_ENABLED=false$' "${tmp_dir}/.env"
grep -q '^NODE_DEVELOPMENT_ENROLLMENT_ENABLED=false$' "${tmp_dir}/.env"
grep -q '^NODE_ALLOWED_ADVERTISE_ADDRS=virtnoded$' "${tmp_dir}/.env"
grep -q '^NODE_SIA_RENTERD_WORKER_URL=$' "${tmp_dir}/.env"
grep -Eq '^HAPROXY_CERT_GID=[0-9]+$' "${tmp_dir}/.env"

digest="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
source_sha="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
deployment_tree_digest="$("${script_dir}/deployment-tree-digest.sh" "${script_dir}")"
release_env_file="${tmp_dir}/release.env"
printf '%s\n' \
  "VIRTROID_BACKEND_IMAGE=virtroid-backend:release-${source_sha}" \
  "VIRTROID_BACKEND_IMAGE_ID=sha256:${digest}" \
  "VIRTROID_BACKEND_MANIFEST_DIGEST=sha256:${digest}" \
  "VIRTROID_SOURCE_SHA=${source_sha}" \
  'VIRTROID_SCHEMA_VERSION=2026071903' \
  "VIRTROID_DEPLOYMENT_TREE_SHA256=${deployment_tree_digest}" > "${release_env_file}"
export VIRTROID_RELEASE_ENV_FILE="${release_env_file}"
printf '%s\n' \
  "POSTGRES_IMAGE=postgres:18@sha256:${digest}" \
  "GO_BUILD_IMAGE=golang:1.26.5-bookworm@sha256:${digest}" \
  "BACKEND_RUNTIME_IMAGE=debian:bookworm-slim@sha256:${digest}" \
  "NODE_RUNTIME_IMAGE=redroid/redroid:14.0.0_64only-latest@sha256:${digest}" \
  "HAPROXY_IMAGE=haproxy:lts@sha256:${digest}" \
  "RENTERD_IMAGE=ghcr.io/siafoundation/renterd:2.9.3@sha256:${digest}" \
  "FALCO_IMAGE=falcosecurity/falco:0.43.0@sha256:${digest}" \
  "RENTERD_API_PASSWORD=test-password" \
  "RENTERD_SEED=test-seed" \
  "NODE_SIA_RENTERD_PASSWORD=test-password" >> "${tmp_dir}/.env"

marker="${tmp_dir}/pwned"
fake_bin="${tmp_dir}/bin"
mkdir -p "${fake_bin}"
cat > "${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${fake_bin}/docker"

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
count_file="${VIRTROID_TEST_CURL_COUNT:?}"
count=0
if [ -f "${count_file}" ]; then
  count="$(cat "${count_file}")"
fi
count=$((count + 1))
printf '%s\n' "${count}" > "${count_file}"
if [ "${count}" -eq 1 ]; then
  exit 7
fi
printf '{"ok":true}\n'
EOF
chmod +x "${fake_bin}/curl"

curl_count_file="${tmp_dir}/curl-count"
PATH="${fake_bin}:${PATH}" \
  VIRTROID_ENV_FILE="${tmp_dir}/.env" \
  VIRTROID_TEST_CURL_COUNT="${curl_count_file}" \
  "${script_dir}/deploy.sh" health >/dev/null
if [ "$(cat "${curl_count_file}")" -ne 3 ]; then
  echo "health check did not retry before checking both endpoints" >&2
  exit 1
fi

PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/.env" "${script_dir}/deploy.sh" validate >/dev/null

cp "${tmp_dir}/.env" "${tmp_dir}/weak-secret.env"
printf '%s\n' 'NODE_SHARED_SECRET=x' >> "${tmp_dir}/weak-secret.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/weak-secret.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "weak production shared secret was accepted" >&2
  exit 1
fi

non_p256_key="$(openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-384 -outform DER 2>/dev/null | openssl base64 -A)"
cp "${tmp_dir}/.env" "${tmp_dir}/wrong-curve.env"
printf 'NODE_PRIVATE_KEY_B64=%s\n' "${non_p256_key}" >> "${tmp_dir}/wrong-curve.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/wrong-curve.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "non-P-256 production node key was accepted" >&2
  exit 1
fi
cp "${tmp_dir}/.env" "${tmp_dir}/renterd.env"
printf '%s\n' \
  'NODE_BLOB_STORE_KIND=sia-renterd' \
  'NODE_SIA_RENTERD_WORKER_URL=http://renterd:9980' \
  'NODE_SIA_RENTERD_MIN_SHARDS=2' \
  'NODE_SIA_RENTERD_TOTAL_SHARDS=3' >> "${tmp_dir}/renterd.env"
PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/renterd.env" VIRTROID_PROFILES=edge,renterd,falco "${script_dir}/deploy.sh" validate >/dev/null

cp "${tmp_dir}/.env" "${tmp_dir}/mutable-image.env"
printf '%s\n' 'POSTGRES_IMAGE=postgres:18' >> "${tmp_dir}/mutable-image.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/mutable-image.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "mutable image reference was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/shared-network.env"
printf '%s\n' 'NODE_DOCKER_NETWORK=virtroid_default' >> "${tmp_dir}/shared-network.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/shared-network.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "shared infrastructure network was accepted for guests" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/open-bootstrap.env"
printf '%s\n' 'BOOTSTRAP_ENABLED=true' >> "${tmp_dir}/open-bootstrap.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/open-bootstrap.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "production bootstrap was allowed without a durable gate" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/open-node-enrollment.env"
printf '%s\n' 'NODE_DEVELOPMENT_ENROLLMENT_ENABLED=true' >> "${tmp_dir}/open-node-enrollment.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/open-node-enrollment.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "development node enrollment was accepted in production" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/callback-ssrf.env"
printf '%s\n' 'NODE_ALLOWED_ADVERTISE_ADDRS=127.0.0.1' >> "${tmp_dir}/callback-ssrf.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/callback-ssrf.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "node callback address outside the deployment allowlist was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/public-api-bind.env"
printf '%s\n' 'HOST_API_BIND=0.0.0.0' >> "${tmp_dir}/public-api-bind.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/public-api-bind.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "public cleartext control-plane bind was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/public-relay-bind.env"
printf '%s\n' 'HOST_RELAY_BIND=0.0.0.0' >> "${tmp_dir}/public-relay-bind.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/public-relay-bind.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "public cleartext relay bind was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/unpinned-catalog.env"
printf '%s\n' 'APP_CATALOG_SYNC_ENABLED=true' 'APP_CATALOG_SYNC_SHA256=' >> "${tmp_dir}/unpinned-catalog.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/unpinned-catalog.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "unpinned catalog sync was accepted" >&2
  exit 1
fi

cat > "${tmp_dir}/malicious.env" <<EOF
POSTGRES_PASSWORD=x
NODE_SHARED_SECRET=x
NODE_PRIVATE_KEY_B64=x
PUBLIC_BASE_URL=https://\$(touch "${marker}")
PUBLIC_RELAY_URL=https://virtroid.example
NODE_ADVERTISE_ADDR=virtnoded
DOCKER_GID=998
EOF

PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/malicious.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1 || true
if [ -e "${marker}" ]; then
  echo "deploy.sh evaluated command substitution from .env" >&2
  exit 1
fi

cp "${release_env_file}" "${tmp_dir}/mutable-release.env"
printf '%s\n' \
  'VIRTROID_BACKEND_IMAGE=virtroid-backend:latest' \
  "VIRTROID_BACKEND_IMAGE_ID=sha256:${digest}" \
  "VIRTROID_BACKEND_MANIFEST_DIGEST=sha256:${digest}" \
  "VIRTROID_SOURCE_SHA=${source_sha}" \
  'VIRTROID_SCHEMA_VERSION=2026071903' \
  "VIRTROID_DEPLOYMENT_TREE_SHA256=${deployment_tree_digest}" > "${tmp_dir}/mutable-release.env"
if PATH="${fake_bin}:${PATH}" \
  VIRTROID_ENV_FILE="${tmp_dir}/.env" \
  VIRTROID_RELEASE_ENV_FILE="${tmp_dir}/mutable-release.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "mutable Virtroid release image was accepted" >&2
  exit 1
fi

printf '%s\n' \
  "VIRTROID_BACKEND_IMAGE=virtroid-backend:release-${source_sha}" \
  "VIRTROID_BACKEND_IMAGE_ID=sha256:${digest}" \
  "VIRTROID_BACKEND_MANIFEST_DIGEST=sha256:${digest}" \
  'VIRTROID_SOURCE_SHA=not-a-commit' \
  'VIRTROID_SCHEMA_VERSION=2026071903' \
  "VIRTROID_DEPLOYMENT_TREE_SHA256=${deployment_tree_digest}" > "${tmp_dir}/invalid-source-release.env"
if PATH="${fake_bin}:${PATH}" \
  VIRTROID_ENV_FILE="${tmp_dir}/.env" \
  VIRTROID_RELEASE_ENV_FILE="${tmp_dir}/invalid-source-release.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "invalid Virtroid source commit was accepted" >&2
  exit 1
fi

grep -q 'ssl-min-ver TLSv1.2' "${script_dir}/haproxy.cfg"
grep -q '^    maxconn 4096$' "${script_dir}/haproxy.cfg"
grep -q '^    log global$' "${script_dir}/haproxy.cfg"
grep -q 'Strict-Transport-Security' "${script_dir}/haproxy.cfg"
grep -q 'set-header X-Forwarded-For %\[src\]' "${script_dir}/haproxy.cfg"
grep -q 'timeout http-request 10s' "${script_dir}/haproxy.cfg"
grep -q 'core_services+=(renterd)' "${script_dir}/deploy.sh"
grep -q 'core_services+=(falco-forwarder falco)' "${script_dir}/deploy.sh"
grep -q 'name: ${NODE_DOCKER_NETWORK:' "${script_dir}/docker-compose.yml"
grep -q 'NODE_RUNTIME_NETWORK_MODE: ${NODE_RUNTIME_NETWORK_MODE:-per-runtime}' "${script_dir}/docker-compose.yml"
grep -q 'NODE_ALLOWED_ADVERTISE_ADDRS: ${NODE_ALLOWED_ADVERTISE_ADDRS:' "${script_dir}/docker-compose.yml"
grep -q 'NODE_DEVELOPMENT_ENROLLMENT_ENABLED: "false"' "${script_dir}/docker-compose.yml"
if grep -q 'NODE_REGISTRATION_SECRET:' "${script_dir}/docker-compose.yml"; then
  echo "production Compose still exposes the development registration secret" >&2
  exit 1
fi
grep -q 'HAPROXY_CERT_GID:?set HAPROXY_CERT_GID' "${script_dir}/docker-compose.yml"
grep -q 'chmod 0640 "${temp_pem}"' "${script_dir}/certbot-deploy-hook.sh"
grep -q '^MaxAuthTries 3$' "${script_dir}/sshd-virtroid-hardening.conf"
grep -q '^PermitRootLogin no$' "${script_dir}/sshd-key-only.conf"
grep -q '^PasswordAuthentication no$' "${script_dir}/sshd-key-only.conf"
grep -q 'authorized key file must contain exactly one public key' "${script_dir}/create-deploy-user.sh"
grep -q 'sudo -u "${deploy_user}" -H docker version' "${script_dir}/create-deploy-user.sh"
if grep -q 'chown -R "${deploy_user}' "${script_dir}/create-deploy-user.sh"; then
  echo "deploy-user setup makes the reviewed deployment tree user-writable" >&2
  exit 1
fi
grep -q 'chown -R root:root "${project_root}/deploy/vps"' "${script_dir}/create-deploy-user.sh"
grep -q 'chmod 0600 "${project_root}/deploy/vps/.env"' "${script_dir}/create-deploy-user.sh"
grep -q 'effective SSH configuration is missing' "${script_dir}/enable-key-only-ssh.sh"
grep -q 'target_config=/etc/ssh/sshd_config.d/00-virtroid-key-only.conf' "${script_dir}/enable-key-only-ssh.sh"
if grep -q 'chown -R .* /srv/virtroid' "${script_dir}/prepare-redroid-host.sh"; then
  echo "host preparation recursively changes runtime userdata ownership" >&2
  exit 1
fi
grep -q '^enabled = true$' "${script_dir}/fail2ban-virtroid.conf"
grep -q '^OnCalendar=\*-\*-\* 02:30:00 UTC$' "${script_dir}/virtroid-backup.timer"
grep -q '/var/backups/virtroid' "${script_dir}/virtroid-backup.sh"
grep -q 'cd "${partial_dir}"' "${script_dir}/virtroid-backup.sh"
grep -q 'mapfile -t completed_backups' "${script_dir}/virtroid-backup.sh"
grep -q 'index = retention_days' "${script_dir}/virtroid-backup.sh"
if grep -q '"${partial_dir}/deploy.env".*SHA256SUMS' "${script_dir}/virtroid-backup.sh"; then
  echo "backup checksum manifest contains temporary absolute paths" >&2
  exit 1
fi
grep -q '/usr/local/sbin/virtroid-release-on-vps' "${script_dir}/release-on-vps.sh"
grep -q 'production VPS source repository must not have a Git remote' "${script_dir}/release-on-vps.sh"
grep -q 'GitHub automation must not exist on the production VPS' "${script_dir}/release-on-vps.sh"
if grep -RniE 'restic|offsite|off-site' "${script_dir}" \
    --exclude=README.md \
    --exclude=test-env-safety.sh \
    --exclude=deployment-tree-digest.sh \
    --exclude=build-local-release.sh \
    --exclude=install-reviewed-deployment-tree.sh \
    --exclude=virtroid-backup.sh >/dev/null; then
  echo "external backup integration remains in the isolated VPS deployment tree" >&2
  exit 1
fi
if grep -q 'chmod 0644' "${script_dir}/certbot-deploy-hook.sh"; then
  echo "certificate deploy hook makes the private key world-readable" >&2
  exit 1
fi

bash "${script_dir}/test-node-fingerprint.sh" >/dev/null

echo "env safety checks passed"
