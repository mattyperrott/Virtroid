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

marker="${tmp_dir}/pwned"
fake_bin="${tmp_dir}/bin"
mkdir -p "${fake_bin}"
cat > "${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${fake_bin}/docker"

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

echo "env safety checks passed"
