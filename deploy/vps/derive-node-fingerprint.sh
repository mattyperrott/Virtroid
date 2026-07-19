#!/bin/bash
set -euo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

test_mode="${VIRTROID_NODE_FINGERPRINT_TEST_MODE:-0}"
if [ "$(id -u)" -ne 0 ] && [ "${test_mode}" != 1 ]; then
  echo "run as root so the production environment remains unreadable" >&2
  exit 1
fi

env_file="${VIRTROID_ENV_FILE:-/opt/virtroid/deploy/vps/.env}"
expected_uid=0
if [ "${test_mode}" = 1 ]; then
  expected_uid="$(id -u)"
fi

file_uid() {
  stat -c '%u' -- "$1" 2>/dev/null || stat -f '%u' "$1"
}

file_mode() {
  stat -c '%a' -- "$1" 2>/dev/null || stat -f '%Lp' "$1"
}

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

[ "${env_file#/}" != "${env_file}" ] || die "deployment environment path must be absolute"
[ -f "${env_file}" ] && [ ! -L "${env_file}" ] || die "deployment environment must be a regular non-symlink file"
[ "$(file_uid "${env_file}")" = "${expected_uid}" ] || die "deployment environment has the wrong owner"
case "$(file_mode "${env_file}")" in
  400|600) ;;
  *) die "deployment environment mode must be 0400 or 0600" ;;
esac
parent="$(dirname "${env_file}")"
[ -d "${parent}" ] && [ ! -L "${parent}" ] || die "deployment environment parent is unsafe"
[ "$(file_uid "${parent}")" = "${expected_uid}" ] || die "deployment environment parent has the wrong owner"
parent_mode="$(file_mode "${parent}")"
parent_permissions="${parent_mode: -3}"
[[ "${parent_permissions}" =~ ^[0-7]{3}$ ]] || die "could not validate deployment environment parent mode"
(( (8#${parent_permissions} & 0022) == 0 )) || die "deployment environment parent is group/world writable"

read_env_value() {
  local wanted_key="$1"
  awk -v wanted="${wanted_key}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      separator = index($0, "=")
      if (separator == 0) next
      key = substr($0, 1, separator - 1)
      if (key == wanted) {
        found++
        value = substr($0, separator + 1)
      }
    }
    END {
      if (found != 1) exit 1
      print value
    }
  ' "${env_file}"
}

node_id="$(read_env_value NODE_ID)" || die "NODE_ID must be defined exactly once"
private_key_b64="$(read_env_value NODE_PRIVATE_KEY_B64)" || die "NODE_PRIVATE_KEY_B64 must be defined exactly once"
[[ "${node_id}" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ ]] || die "NODE_ID is invalid"
[[ "${private_key_b64}" =~ ^[A-Za-z0-9+/]+={0,2}$ ]] || die "NODE_PRIVATE_KEY_B64 is not canonical base64"

temporary="$(mktemp -d)"
cleanup() {
  private_key_b64=
  if [ -d "${temporary}" ]; then
    find "${temporary}" -depth -delete
  fi
}
trap cleanup EXIT
public_der="${temporary}/node-public.der"
if ! printf '%s' "${private_key_b64}" |
  openssl base64 -d -A |
  openssl pkey -inform DER -pubout -outform DER -out "${public_der}"; then
  die "NODE_PRIVATE_KEY_B64 is not a supported private key"
fi
public_description="$(openssl pkey -pubin -inform DER -in "${public_der}" -text_pub -noout 2>/dev/null)"
if [[ "${public_description}" != *"ASN1 OID: prime256v1"* ]] &&
   [[ "${public_description}" != *"NIST CURVE: P-256"* ]]; then
  die "node key must use the P-256 curve"
fi

public_key_b64="$(openssl base64 -A -in "${public_der}")"
fingerprint="$(openssl dgst -sha256 -r "${public_der}" | awk '{print $1}')"
[[ "${fingerprint}" =~ ^[0-9a-f]{64}$ ]] || die "could not derive the node fingerprint"

printf 'node_id=%s\npublic_key_b64=%s\nfingerprint_sha256=%s\n' \
  "${node_id}" "${public_key_b64}" "${fingerprint}"
