#!/usr/bin/env bash
set -euo pipefail

env_file="${VIRTROID_ENV_FILE:-/opt/virtroid/deploy/vps/.env}"
lineage="${RENEWED_LINEAGE:-/etc/letsencrypt/live/virtroid.network}"
pem_dir=/srv/virtroid/tls
pem_path="${pem_dir}/virtroid.pem"

if [ ! -r "${lineage}/fullchain.pem" ] || [ ! -r "${lineage}/privkey.pem" ]; then
  echo "renewed certificate lineage is incomplete: ${lineage}" >&2
  exit 1
fi
if [ ! -r "${env_file}" ]; then
  echo "deployment environment is not readable: ${env_file}" >&2
  exit 1
fi

mapfile -t configured_gids < <(
  sed -n 's/^HAPROXY_CERT_GID=\([0-9][0-9]*\)$/\1/p' "${env_file}"
)
if [ "${#configured_gids[@]}" -ne 1 ]; then
  echo "HAPROXY_CERT_GID must appear exactly once and be numeric in ${env_file}" >&2
  exit 1
fi
cert_gid="${configured_gids[0]}"

openssl x509 -in "${lineage}/fullchain.pem" -noout -checkend 86400
openssl pkey -in "${lineage}/privkey.pem" -check -noout
cert_public_key="$(
  openssl x509 -in "${lineage}/fullchain.pem" -pubkey -noout |
    openssl pkey -pubin -outform DER |
    openssl dgst -sha256
)"
private_public_key="$(
  openssl pkey -in "${lineage}/privkey.pem" -pubout -outform DER |
    openssl dgst -sha256
)"
if [ "${cert_public_key}" != "${private_public_key}" ]; then
  echo "renewed certificate and private key do not match" >&2
  exit 1
fi

install -d -o root -g "${cert_gid}" -m 0750 "${pem_dir}"
temp_pem="$(mktemp "${pem_dir}/.virtroid.pem.XXXXXX")"
cleanup() {
  if [ -e "${temp_pem}" ]; then
    unlink "${temp_pem}"
  fi
}
trap cleanup EXIT
cat "${lineage}/fullchain.pem" "${lineage}/privkey.pem" > "${temp_pem}"
chown root:"${cert_gid}" "${temp_pem}"
chmod 0640 "${temp_pem}"
mv -f "${temp_pem}" "${pem_path}"
trap - EXIT

docker exec virtroid-edge haproxy -c -f /usr/local/etc/haproxy/haproxy.cfg
docker restart virtroid-edge >/dev/null
for attempt in $(seq 1 30); do
  if curl -kfsS --max-time 2 https://127.0.0.1/healthz >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done

echo "HAProxy did not become healthy after certificate renewal" >&2
exit 1
