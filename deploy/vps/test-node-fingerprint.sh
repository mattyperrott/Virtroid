#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'find "${tmp_dir}" -depth -delete' EXIT
umask 0077

openssl ecparam -name prime256v1 -genkey -noout |
  openssl pkcs8 -topk8 -nocrypt -outform DER -out "${tmp_dir}/node-private.der"
private_key_b64="$(openssl base64 -A -in "${tmp_dir}/node-private.der")"
env_file="${tmp_dir}/.env"
printf 'NODE_ID=production-node-1\nNODE_PRIVATE_KEY_B64=%s\n' \
  "${private_key_b64}" > "${env_file}"
chmod 0600 "${env_file}"

output="$(
  VIRTROID_NODE_FINGERPRINT_TEST_MODE=1 \
  VIRTROID_ENV_FILE="${env_file}" \
    bash "${script_dir}/derive-node-fingerprint.sh"
)"
grep -q '^node_id=production-node-1$' <<< "${output}"
grep -Eq '^public_key_b64=[A-Za-z0-9+/]+={0,2}$' <<< "${output}"
grep -Eq '^fingerprint_sha256=[0-9a-f]{64}$' <<< "${output}"
if grep -Fq "${private_key_b64}" <<< "${output}"; then
  echo "fingerprint helper exposed private key material" >&2
  exit 1
fi

chmod 0644 "${env_file}"
if VIRTROID_NODE_FINGERPRINT_TEST_MODE=1 \
  VIRTROID_ENV_FILE="${env_file}" \
  bash "${script_dir}/derive-node-fingerprint.sh" >/dev/null 2>&1; then
  echo "fingerprint helper accepted a world-readable deployment environment" >&2
  exit 1
fi

echo "node fingerprint checks passed"
