#!/bin/bash
# Creates the payments service's secrets under /etc/virtroid/secrets
# (payments.md), root-owned, group <secret gid>, mode 0440:
#
#   payments-seed   the hot wallet's 24-word BIP39 seed phrase; accounts'
#                   USDT deposit addresses are its keys at m/44'/195'/0'/0/i
#   payments-key    the P-256 key the service signs its reports with
#
# The seed controls users' deposits. This script never prints it: read it
# once from payments-seed and copy it to two offline places. Existing secrets
# are kept, so running it again only fixes permissions and prints the public
# values virtroidd needs.
#
#   usage: configure-payments-secrets.sh <backend image> <secret gid>
set -euo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

die() {
  echo "$*" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || die "run as root"
[ "$#" -eq 2 ] || die "usage: $0 <backend image> <secret gid>"
image="$1"
gid="$2"
[[ "${image}" =~ ^[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+$ ]] || die "the backend image must be the local release tag"
[[ "${gid}" =~ ^[0-9]+$ ]] && [ "${gid}" -gt 0 ] && [ "${gid}" -lt 65534 ] || die "the secret gid must be a dedicated group id"
docker image inspect "${image}" >/dev/null 2>&1 || die "the backend image ${image} is not loaded"

dir=/etc/virtroid/secrets
[ -d "${dir}" ] || install -d -o root -g root -m 0700 "${dir}"
umask 077

key="${dir}/payments-key"
if [ ! -e "${key}" ]; then
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -outform DER | base64 -w0 > "${key}.new"
  printf '\n' >> "${key}.new"
  mv "${key}.new" "${key}"
fi

seed="${dir}/payments-seed"
if [ ! -e "${seed}" ]; then
  work="$(mktemp -d)"
  trap 'rm -rf "${work}"' EXIT
  docker run --rm --network none --read-only --user 0:0 --cap-drop ALL \
    --security-opt no-new-privileges -v "${work}:/out" \
    --entrypoint /usr/local/bin/virtroid-payments "${image}" seed /out/payments-seed >/dev/null
  [ "$(wc -w < "${work}/payments-seed")" -eq 24 ] || die "the payments service did not write a seed phrase"
  mv "${work}/payments-seed" "${seed}"
fi

for secret in "${key}" "${seed}"; do
  chown "root:${gid}" "${secret}"
  chmod 0440 "${secret}"
done

echo "Put these two lines in .env:"
docker run --rm --network none --read-only --user 0:0 --cap-drop ALL \
  --security-opt no-new-privileges \
  -e PAYMENTS_SEED_FILE=/run/secrets/payments-seed -e PAYMENTS_PRIVATE_KEY_FILE=/run/secrets/payments-key \
  -v "${seed}:/run/secrets/payments-seed:ro" -v "${key}:/run/secrets/payments-key:ro" \
  --entrypoint /usr/local/bin/virtroid-payments "${image}" public
echo "Back up the seed phrase: read it once from ${seed} and copy it to two offline places."
