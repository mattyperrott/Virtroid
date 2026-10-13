#!/bin/bash
# Creates the Sia indexer's secrets under /etc/virtroid/secrets (indexd.md):
#
#   indexd.yml                   indexd's config: the wallet recovery phrase,
#                                the admin and database passwords (root, 0600)
#   indexd-postgres-password     indexd's database password (root, 0600)
#   sia-indexer-admin-password   the admin password, for virtroidd (0440)
#   sia-key-secret               every account's Sia keys derive from it (0440)
#
# The recovery phrase controls the wallet that pays Sia hosts. This script
# never prints it: copy it from indexd.yml to two offline places yourself.
# Existing secrets are kept, so running it again only fixes permissions.
#
#   usage: configure-indexd-secrets.sh <indexd image> <app url> <secret gid>
set -euo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

die() {
  echo "$*" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || die "run as root"
[ "$#" -eq 3 ] || die "usage: $0 <indexd image> <app url> <secret gid>"
image="$1"
app_url="$2"
gid="$3"
[[ "${image}" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]] || die "the indexd image must be pinned by digest"
[[ "${app_url}" =~ ^https://[A-Za-z0-9.-]+:9982$ ]] || die "the app url must be https://<host>:9982"
[[ "${gid}" =~ ^[0-9]+$ ]] && [ "${gid}" -gt 0 ] && [ "${gid}" -lt 65534 ] || die "the secret gid must be a dedicated group id"

dir=/etc/virtroid/secrets
[ -d "${dir}" ] || install -d -o root -g root -m 0700 "${dir}"
umask 077

random_hex() {
  od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
}

# write_secret <name> <group> <mode> <value>, unless the file exists.
write_secret() {
  local path="${dir}/$1"
  if [ ! -e "${path}" ]; then
    printf '%s\n' "$4" > "${path}.new"
    mv "${path}.new" "${path}"
  fi
  chown "root:$2" "${path}"
  chmod "$3" "${path}"
}

write_secret indexd-postgres-password root 0600 "$(random_hex)"
write_secret sia-indexer-admin-password "${gid}" 0440 "$(random_hex)"
write_secret sia-key-secret "${gid}" 0440 "$(random_hex)"

config="${dir}/indexd.yml"
if [ ! -e "${config}" ]; then
  seed_output="$(docker run --rm --network none --read-only --cap-drop ALL \
    --security-opt no-new-privileges --entrypoint indexd "${image}" seed)"
  phrase="$(sed -n 's/^Recovery Phrase: //p' <<< "${seed_output}")"
  [ "$(wc -w <<< "${phrase}")" -eq 12 ] || die "indexd did not print a recovery phrase"
  admin_password="$(tr -d '\n' < "${dir}/sia-indexer-admin-password")"
  database_password="$(tr -d '\n' < "${dir}/indexd-postgres-password")"
  cat > "${config}.new" <<YAML
autoOpenWebUI: false
directory: /data
recoveryPhrase: "${phrase}"
adminAPI:
  address: :9980
  password: "${admin_password}"
applicationAPI:
  address: :9982
  advertiseURL: "${app_url}"
syncer:
  address: :9981
  bootstrap: true
  enableUPnP: false
consensus:
  network: mainnet
# The explorer (api.siascan.com) would learn the wallet address and this
# server's IP; without it indexd syncs the chain itself.
explorer:
  enabled: false
slabs:
  migrations: true
log:
  stdout:
    enabled: true
    level: info
    format: json
    enableANSI: false
  file:
    enabled: false
database:
  host: indexd-postgres
  port: 5432
  user: indexd
  password: "${database_password}"
  database: indexd
  sslmode: disable
YAML
  unset phrase seed_output admin_password database_password
  mv "${config}.new" "${config}"
fi
chown root:root "${config}"
chmod 0600 "${config}"
grep -Fxq "  advertiseURL: \"${app_url}\"" "${config}" || die "${config} advertises another URL"

echo "indexd secrets are in ${dir}."
echo "Back up the recovery phrase: read it once from ${config}"
echo "(the recoveryPhrase line) and copy it to two offline places."
