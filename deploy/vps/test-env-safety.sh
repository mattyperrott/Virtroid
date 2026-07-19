#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

cp "${script_dir}/generate-env.sh" "${tmp_dir}/generate-env.sh"
cp "${script_dir}/deploy.sh" "${tmp_dir}/deploy.sh"

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
grep -q '^NODE_ALLOWED_ADVERTISE_ADDRS=virtnoded$' "${tmp_dir}/.env"
grep -q '^NODE_SIA_RENTERD_WORKER_URL=$' "${tmp_dir}/.env"
grep -Eq '^HAPROXY_CERT_GID=[0-9]+$' "${tmp_dir}/.env"

digest="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
printf '%s\n' \
  "POSTGRES_IMAGE=postgres:18@sha256:${digest}" \
  "GO_BUILD_IMAGE=golang:1.26.5-bookworm@sha256:${digest}" \
  "BACKEND_RUNTIME_IMAGE=debian:bookworm-slim@sha256:${digest}" \
  "NODE_RUNTIME_IMAGE=redroid/redroid:14.0.0_64only-latest@sha256:${digest}" \
  "HAPROXY_IMAGE=haproxy:lts@sha256:${digest}" \
  "RENTERD_IMAGE=ghcr.io/siafoundation/renterd:v2.9.1@sha256:${digest}" \
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
  "${tmp_dir}/deploy.sh" health >/dev/null
if [ "$(cat "${curl_count_file}")" -ne 3 ]; then
  echo "health check did not retry before checking both endpoints" >&2
  exit 1
fi

PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/.env" "${tmp_dir}/deploy.sh" validate >/dev/null
cp "${tmp_dir}/.env" "${tmp_dir}/renterd.env"
printf '%s\n' \
  'NODE_BLOB_STORE_KIND=sia-renterd' \
  'NODE_SIA_RENTERD_WORKER_URL=http://renterd:9980' >> "${tmp_dir}/renterd.env"
PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/renterd.env" VIRTROID_PROFILES=edge,renterd,falco "${tmp_dir}/deploy.sh" validate >/dev/null

cp "${tmp_dir}/.env" "${tmp_dir}/mutable-image.env"
printf '%s\n' 'POSTGRES_IMAGE=postgres:18' >> "${tmp_dir}/mutable-image.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/mutable-image.env" "${tmp_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "mutable image reference was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/shared-network.env"
printf '%s\n' 'NODE_DOCKER_NETWORK=virtroid_default' >> "${tmp_dir}/shared-network.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/shared-network.env" "${tmp_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "shared infrastructure network was accepted for guests" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/open-bootstrap.env"
printf '%s\n' 'BOOTSTRAP_ENABLED=true' >> "${tmp_dir}/open-bootstrap.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/open-bootstrap.env" "${tmp_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "production bootstrap was allowed without a durable gate" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/callback-ssrf.env"
printf '%s\n' 'NODE_ALLOWED_ADVERTISE_ADDRS=127.0.0.1' >> "${tmp_dir}/callback-ssrf.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/callback-ssrf.env" "${tmp_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "node callback address outside the deployment allowlist was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/unpinned-catalog.env"
printf '%s\n' 'APP_CATALOG_SYNC_ENABLED=true' 'APP_CATALOG_SYNC_SHA256=' >> "${tmp_dir}/unpinned-catalog.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/unpinned-catalog.env" "${tmp_dir}/deploy.sh" validate >/dev/null 2>&1; then
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

PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/malicious.env" "${tmp_dir}/deploy.sh" build >/dev/null 2>&1 || true
if [ -e "${marker}" ]; then
  echo "deploy.sh evaluated command substitution from .env" >&2
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
grep -q 'HAPROXY_CERT_GID:?set HAPROXY_CERT_GID' "${script_dir}/docker-compose.yml"
grep -q 'chmod 0640 "${temp_pem}"' "${script_dir}/certbot-deploy-hook.sh"
grep -q '^MaxAuthTries 3$' "${script_dir}/sshd-virtroid-hardening.conf"
grep -q '^PermitRootLogin no$' "${script_dir}/sshd-key-only.conf"
grep -q '^PasswordAuthentication no$' "${script_dir}/sshd-key-only.conf"
grep -q 'authorized key file must contain exactly one public key' "${script_dir}/create-deploy-user.sh"
grep -q 'sudo -u "${deploy_user}" -H docker version' "${script_dir}/create-deploy-user.sh"
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
if grep -q 'chmod 0644' "${script_dir}/certbot-deploy-hook.sh"; then
  echo "certificate deploy hook makes the private key world-readable" >&2
  exit 1
fi

echo "env safety checks passed"
