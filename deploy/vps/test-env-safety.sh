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
grep -q '^NODE_MIN_FREE_DISK_BYTES=10737418240$' "${tmp_dir}/.env"
grep -q '^NODE_MIN_FREE_DISK_PERCENT=5$' "${tmp_dir}/.env"
grep -q '^BOOTSTRAP_ENABLED=true$' "${tmp_dir}/.env"
grep -q '^BOOTSTRAP_REQUIRE_INVITE=true$' "${tmp_dir}/.env"
grep -q '^BOOTSTRAP_AUTO_ISSUE_INVITE=true$' "${tmp_dir}/.env"
grep -q '^RUNTIME_LOG_RETENTION=720h$' "${tmp_dir}/.env"
grep -q '^NODE_DEVELOPMENT_ENROLLMENT_ENABLED=false$' "${tmp_dir}/.env"
grep -Eq '^CONTROL_PLANE_CALLBACK_PRIVATE_KEY_B64=[A-Za-z0-9+/]+={0,2}$' "${tmp_dir}/.env"
grep -Eq '^CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64=[A-Za-z0-9+/]+={0,2}$' "${tmp_dir}/.env"
grep -q '^NODE_ALLOWED_ADVERTISE_ADDRS=virtnoded$' "${tmp_dir}/.env"
grep -q '^NODE_SIA_RENTERD_WORKER_URL=$' "${tmp_dir}/.env"
grep -q '^NODE_SIA_RENTERD_MIN_SHARDS=10$' "${tmp_dir}/.env"
grep -q '^NODE_SIA_RENTERD_TOTAL_SHARDS=30$' "${tmp_dir}/.env"
grep -q '^RENTERD_CONFIG_FILE=/etc/virtroid/secrets/renterd.yml$' "${tmp_dir}/.env"
grep -q '^RENTERD_API_PASSWORD_FILE=/etc/virtroid/secrets/renterd-api-password$' "${tmp_dir}/.env"
grep -q '^RENTERD_MYSQL_PASSWORD_FILE=/etc/virtroid/secrets/renterd-mysql-password$' "${tmp_dir}/.env"
grep -q '^RENTERD_MYSQL_ROOT_PASSWORD_FILE=/etc/virtroid/secrets/renterd-mysql-root-password$' "${tmp_dir}/.env"
if grep -Eq '^(RENTERD_SEED|RENTERD_API_PASSWORD|NODE_SIA_RENTERD_PASSWORD)=' "${tmp_dir}/.env"; then
  echo "generated deployment environment still contains renterd secret values" >&2
  exit 1
fi
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
  "GO_BUILD_IMAGE=golang:1.26.6-bookworm@sha256:${digest}" \
  "BACKEND_RUNTIME_IMAGE=alpine:3.23@sha256:${digest}" \
  "NODE_RUNTIME_IMAGE=redroid/redroid:14.0.0_64only-latest@sha256:${digest}" \
  "HAPROXY_IMAGE=haproxy:lts@sha256:${digest}" \
  "RENTERD_IMAGE=ghcr.io/siafoundation/renterd:2.9.3@sha256:${digest}" \
  "RENTERD_MYSQL_IMAGE=mysql:8.4@sha256:${digest}" \
  "FALCO_IMAGE=falcosecurity/falco:0.43.0@sha256:${digest}" >> "${tmp_dir}/.env"

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
if [ "$(cat "${curl_count_file}")" -ne 5 ]; then
  echo "health check did not retry before checking liveness, node readiness, control-plane readiness, and the public endpoint" >&2
  exit 1
fi

PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/.env" "${script_dir}/deploy.sh" validate >/dev/null

non_p256_key="$(openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-384 -outform DER 2>/dev/null | openssl base64 -A)"
cp "${tmp_dir}/.env" "${tmp_dir}/wrong-curve.env"
printf 'NODE_PRIVATE_KEY_B64=%s\n' "${non_p256_key}" >> "${tmp_dir}/wrong-curve.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/wrong-curve.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "non-P-256 production node key was accepted" >&2
  exit 1
fi

mismatched_private="$(openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:prime256v1 -outform DER 2>/dev/null | openssl base64 -A)"
mismatched_public="$(printf '%s' "${mismatched_private}" | openssl base64 -d -A | openssl pkey -inform DER -pubout -outform DER 2>/dev/null | openssl base64 -A)"
cp "${tmp_dir}/.env" "${tmp_dir}/mismatched-callback-key.env"
printf 'CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64=%s\n' "${mismatched_public}" >> "${tmp_dir}/mismatched-callback-key.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/mismatched-callback-key.env" \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "mismatched control-plane callback keypair was accepted" >&2
  exit 1
fi
cp "${tmp_dir}/.env" "${tmp_dir}/renterd.env"
printf '%s\n' \
  'NODE_BLOB_STORE_KIND=sia-renterd' \
  'NODE_SIA_RENTERD_WORKER_URL=http://renterd:9980' \
  'NODE_SIA_RENTERD_WALLET_ADDRESS=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' >> "${tmp_dir}/renterd.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/renterd.env" VIRTROID_PROFILES=edge,renterd,falco \
  "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "renterd profile was accepted without the installed offline-backup and funding gate" >&2
  exit 1
fi

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

cp "${tmp_dir}/.env" "${tmp_dir}/invalid-bootstrap.env"
printf '%s\n' 'BOOTSTRAP_ENABLED=invalid' >> "${tmp_dir}/invalid-bootstrap.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/invalid-bootstrap.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "invalid bootstrap setting was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/public-bootstrap.env"
printf '%s\n' 'BOOTSTRAP_REQUIRE_INVITE=false' >> "${tmp_dir}/public-bootstrap.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/public-bootstrap.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "production deployment accepted anonymous public bootstrap" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/manual-bootstrap.env"
printf '%s\n' 'BOOTSTRAP_AUTO_ISSUE_INVITE=false' >> "${tmp_dir}/manual-bootstrap.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/manual-bootstrap.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "production deployment accepted manual invitation entry required by the client" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/unsafe-disk-bytes.env"
printf '%s\n' 'NODE_MIN_FREE_DISK_BYTES=1048576' >> "${tmp_dir}/unsafe-disk-bytes.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/unsafe-disk-bytes.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "unsafe minimum free-disk byte floor was accepted" >&2
  exit 1
fi

cp "${tmp_dir}/.env" "${tmp_dir}/unsafe-disk-percent.env"
printf '%s\n' 'NODE_MIN_FREE_DISK_PERCENT=0' >> "${tmp_dir}/unsafe-disk-percent.env"
if PATH="${fake_bin}:${PATH}" VIRTROID_ENV_FILE="${tmp_dir}/unsafe-disk-percent.env" "${script_dir}/deploy.sh" validate >/dev/null 2>&1; then
  echo "unsafe minimum free-disk percentage floor was accepted" >&2
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
grep -q 'core_services+=(renterd-mysql renterd)' "${script_dir}/deploy.sh"
grep -q 'virtroid-configure-renterd-secrets verify-activation' "${script_dir}/deploy.sh"
grep -q 'approved mainnet 10-of-30 shard policy' "${script_dir}/deploy.sh"
grep -q 'RENTERD_CONFIG_FILE: /run/secrets/renterd.yml' "${script_dir}/docker-compose.yml"
grep -q 'NODE_SIA_RENTERD_PASSWORD_FILE: /run/secrets/renterd-api-password' "${script_dir}/docker-compose.yml"
if grep -Eq 'RENTERD_(SEED|API_PASSWORD):|NODE_SIA_RENTERD_PASSWORD:' "${script_dir}/docker-compose.yml"; then
  echo "production Compose exposes renterd secrets in container metadata" >&2
  exit 1
fi
grep -q 'virtroid-configure-renterd-secrets' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'virtroid-renterd-admin' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'virtroid-renterd-smoke-test' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'OFFLINE COPIES VERIFIED' "${script_dir}/configure-renterd-secrets.sh"
grep -q 'wallet_address_match' "${script_dir}/renterd-admin.sh"
grep -q 'NODE_BLOB_SMOKE_TEST=1' "${script_dir}/renterd-smoke-test.sh"
grep -q 'if profile_enabled falco || profile_enabled nids; then' "${script_dir}/deploy.sh"
grep -q 'core_services+=(falco-forwarder)' "${script_dir}/deploy.sh"
grep -q 'core_services+=(falco)' "${script_dir}/deploy.sh"
grep -q 'core_services+=(suricata)' "${script_dir}/deploy.sh"
grep -q 'wait_for_healthy_container virtroid-suricata' "${script_dir}/deploy.sh"
grep -q -- '- DAC_OVERRIDE' "${script_dir}/docker-compose.yml"
grep -q 'test -f /var/log/suricata/eve.json && kill -0 1' "${script_dir}/docker-compose.yml"
grep -q 'name: ${NODE_DOCKER_NETWORK:' "${script_dir}/docker-compose.yml"
grep -q 'NODE_RUNTIME_NETWORK_MODE: ${NODE_RUNTIME_NETWORK_MODE:-per-runtime}' "${script_dir}/docker-compose.yml"
grep -q 'NODE_MIN_FREE_DISK_BYTES: ${NODE_MIN_FREE_DISK_BYTES:-10737418240}' "${script_dir}/docker-compose.yml"
grep -q 'NODE_MIN_FREE_DISK_PERCENT: ${NODE_MIN_FREE_DISK_PERCENT:-5}' "${script_dir}/docker-compose.yml"
grep -q 'NODE_ALLOWED_ADVERTISE_ADDRS: ${NODE_ALLOWED_ADVERTISE_ADDRS:' "${script_dir}/docker-compose.yml"
grep -q 'NODE_DEVELOPMENT_ENROLLMENT_ENABLED: "false"' "${script_dir}/docker-compose.yml"
if grep -q 'NODE_REGISTRATION_SECRET:' "${script_dir}/docker-compose.yml"; then
  echo "production Compose still exposes the development registration secret" >&2
  exit 1
fi
if grep -q 'NODE_SHARED_SECRET:' "${script_dir}/docker-compose.yml"; then
  echo "production Compose still exposes the retired callback shared secret" >&2
  exit 1
fi
grep -q 'HAPROXY_CERT_GID:?set HAPROXY_CERT_GID' "${script_dir}/docker-compose.yml"
grep -q 'chmod 0640 "${temp_pem}"' "${script_dir}/certbot-deploy-hook.sh"
grep -q '^MaxAuthTries 3$' "${script_dir}/sshd-virtroid-hardening.conf"
grep -q '^AllowTcpForwarding no$' "${script_dir}/sshd-virtroid-hardening.conf"
grep -q '^AllowStreamLocalForwarding no$' "${script_dir}/sshd-virtroid-hardening.conf"
grep -q '^ClientAliveInterval 300$' "${script_dir}/sshd-virtroid-hardening.conf"
grep -q '^PermitRootLogin no$' "${script_dir}/sshd-key-only.conf"
grep -q '^PasswordAuthentication no$' "${script_dir}/sshd-key-only.conf"
grep -q '^kernel.kptr_restrict = 2$' "${script_dir}/sysctl-virtroid-hardening.conf"
grep -q '^kernel.yama.ptrace_scope = 2$' "${script_dir}/sysctl-virtroid-hardening.conf"
grep -q '^kernel.unprivileged_bpf_disabled = 0$' "${script_dir}/sysctl-virtroid-hardening.conf"
grep -q '^net.ipv4.conf.all.send_redirects = 0$' "${script_dir}/sysctl-virtroid-hardening.conf"
grep -q 'sysctl-virtroid-hardening.conf.*99-virtroid-hardening.conf' "${script_dir}/prepare-redroid-host.sh"
grep -q 'audit-virtroid.rules.*99-virtroid-hardening.rules' "${script_dir}/prepare-redroid-host.sh"
grep -q '^  auditd$' "${script_dir}/prepare-redroid-host.sh"
grep -q '^  audispd-plugins$' "${script_dir}/prepare-redroid-host.sh"
grep -q '^  apparmor$' "${script_dir}/prepare-redroid-host.sh"
grep -q '^  apparmor-utils$' "${script_dir}/prepare-redroid-host.sh"
grep -q '^  ufw$' "${script_dir}/prepare-redroid-host.sh"
grep -q '^  unattended-upgrades$' "${script_dir}/prepare-redroid-host.sh"
grep -q 'systemctl enable --now auditd' "${script_dir}/prepare-redroid-host.sh"
grep -q 'augenrules --load' "${script_dir}/prepare-redroid-host.sh"
grep -q 'systemctl enable --now apparmor' "${script_dir}/prepare-redroid-host.sh"
grep -q 'systemctl enable --now unattended-upgrades' "${script_dir}/prepare-redroid-host.sh"
grep -q '/usr/local/sbin/virtroid-configure-host-firewall' "${script_dir}/prepare-redroid-host.sh"
grep -q '/usr/local/sbin/virtroid-verify-host-hardening' "${script_dir}/prepare-redroid-host.sh"
grep -q '/etc/virtroid/ -p wa -k virtroid-secrets' "${script_dir}/audit-virtroid.rules"
grep -q '/var/lib/virtroid-deploy/ -p wa -k virtroid-release' "${script_dir}/audit-virtroid.rules"
grep -q 'audit-virtroid.rules' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q '/etc/audit/rules.d/99-virtroid-hardening.rules' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'systemctl enable --now auditd' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'augenrules --load' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q '/etc/apt/apt.conf.d/52virtroid-unattended-upgrades' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'verify-host-hardening.sh' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'systemctl enable --now unattended-upgrades' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q 'read_only: true' "${script_dir}/docker-compose.yml"
grep -q 'no-new-privileges:true' "${script_dir}/docker-compose.yml"
grep -q 'use_backend virtnoded_relay if relay_connect relay_path' "${script_dir}/haproxy.cfg"
if grep -q 'use_backend virtnoded_relay if relay_connect$' "${script_dir}/haproxy.cfg"; then
  echo "HAProxy routes CONNECT requests outside the relay path to the node agent" >&2
  exit 1
fi
grep -q 'authorized key file must contain exactly one public key' "${script_dir}/create-deploy-user.sh"
grep -q 'sudo -u "${deploy_user}" -H docker version' "${script_dir}/create-deploy-user.sh"
grep -q 'if \[ -s "${deploy_home}/.ssh/authorized_keys" \]; then' "${script_dir}/create-deploy-user.sh"
grep -q 'passwd --lock root' "${script_dir}/create-deploy-user.sh"
grep -q 'VIRTROID_REQUIRE_ROOT_LOCKED=true' "${script_dir}/create-deploy-user.sh"
if grep -q 'chown -R "${deploy_user}' "${script_dir}/create-deploy-user.sh"; then
  echo "deploy-user setup makes the reviewed deployment tree user-writable" >&2
  exit 1
fi
grep -q 'chown -R root:root "${project_root}/deploy/vps"' "${script_dir}/create-deploy-user.sh"
grep -q 'chmod 0600 "${project_root}/deploy/vps/.env"' "${script_dir}/create-deploy-user.sh"
grep -q 'effective SSH configuration is missing' "${script_dir}/enable-key-only-ssh.sh"
grep -q 'target_config=/etc/ssh/sshd_config.d/00-virtroid-key-only.conf' "${script_dir}/enable-key-only-ssh.sh"
grep -q 'VIRTROID_REQUIRE_KEY_ONLY_SSH=true' "${script_dir}/enable-key-only-ssh.sh"
if grep -q 'chown -R .* /srv/virtroid' "${script_dir}/prepare-redroid-host.sh"; then
  echo "host preparation recursively changes runtime userdata ownership" >&2
  exit 1
fi
grep -q '^enabled = true$' "${script_dir}/fail2ban-virtroid.conf"
grep -q '^ufw default deny incoming' "${script_dir}/configure-host-firewall.sh"
grep -q '^ufw default deny routed' "${script_dir}/configure-host-firewall.sh"
grep -q 'VIRTROID_FIREWALL_RESET=true' "${script_dir}/configure-host-firewall.sh"
grep -q '^APT::Periodic::Unattended-Upgrade "1";$' "${script_dir}/unattended-upgrades-virtroid.conf"
grep -q '^Unattended-Upgrade::Automatic-Reboot "false";$' "${script_dir}/unattended-upgrades-virtroid.conf"
grep -q 'kernel.unprivileged_bpf_disabled 0' "${script_dir}/verify-host-hardening.sh"
grep -q 'lost 0' "${script_dir}/verify-host-hardening.sh"
grep -q "docker inspect virtroidd --format '{{.HostConfig.ReadonlyRootfs}}'" "${script_dir}/verify-host-hardening.sh"
grep -q 'UFW rules differ from the reviewed policy' "${script_dir}/verify-host-hardening.sh"
for retired_backup_source in \
  virtroid-backup.sh \
  virtroid-backup.service \
  virtroid-backup.timer; do
  if [ -e "${script_dir}/${retired_backup_source}" ]; then
    echo "retired backup source still exists: ${retired_backup_source}" >&2
    exit 1
  fi
done
if [ -d "${script_dir}/monitoring" ]; then
  echo "retired operational monitoring configuration still exists" >&2
  exit 1
fi
if grep -q 'virtroid-backup' "${script_dir}/apply-local-release.sh"; then
  echo "release cutover still invokes the retired backup feature" >&2
  exit 1
fi
grep -q 'remove_retired_operational_monitoring' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q '/var/backups/virtroid /var/backups/virtroid-deploy-tree' "${script_dir}/install-reviewed-deployment-tree.sh"
grep -q '/usr/local/sbin/virtroid-release-on-vps' "${script_dir}/release-on-vps.sh"
grep -q 'production VPS source repository must not have a Git remote' "${script_dir}/release-on-vps.sh"
grep -q 'GitHub automation must not exist on the production VPS' "${script_dir}/release-on-vps.sh"
grep -q 'installer_digest_before=' "${script_dir}/release-on-vps.sh"
grep -q 'installer_digest_after=' "${script_dir}/release-on-vps.sh"
grep -q 'installer_digest_after.*installer_digest_before' "${script_dir}/release-on-vps.sh"
grep -q 'VIRTROID_PROFILES=edge,falco,nids' "${script_dir}/release-on-vps.sh"
if grep -RniE 'restic|offsite|off-site' "${script_dir}" \
    --exclude=README.md \
    --exclude=test-env-safety.sh \
    --exclude=deployment-tree-digest.sh \
    --exclude=build-local-release.sh \
    --exclude=install-reviewed-deployment-tree.sh >/dev/null; then
  echo "external backup integration remains in the isolated VPS deployment tree" >&2
  exit 1
fi
if grep -q 'chmod 0644' "${script_dir}/certbot-deploy-hook.sh"; then
  echo "certificate deploy hook makes the private key world-readable" >&2
  exit 1
fi

bash "${script_dir}/test-node-fingerprint.sh" >/dev/null

echo "env safety checks passed"
