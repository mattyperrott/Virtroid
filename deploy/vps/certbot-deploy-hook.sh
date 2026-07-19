#!/bin/bash
set -euo pipefail

test_mode="${VIRTROID_CERT_TEST_MODE:-0}"
test_openssl_binary="${VIRTROID_CERT_TEST_OPENSSL:-}"
test_docker_binary="${VIRTROID_CERT_TEST_DOCKER:-}"
test_curl_binary="${VIRTROID_CERT_TEST_CURL:-}"
test_sleep_binary="${VIRTROID_CERT_TEST_SLEEP:-}"
export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

env_file="${VIRTROID_ENV_FILE:-/opt/virtroid/deploy/vps/.env}"
lineage="${RENEWED_LINEAGE:-/etc/letsencrypt/live/virtroid.network}"
state_root=/var/lib/virtroid-deploy
pem_dir=/srv/virtroid/tls
flock_binary=/usr/bin/flock
openssl_binary=/usr/bin/openssl
docker_binary=/usr/bin/docker
curl_binary=/usr/bin/curl
sleep_binary=/usr/bin/sleep
if [ "${test_mode}" = 1 ]; then
  state_root="${VIRTROID_CERT_TEST_STATE_ROOT:?set VIRTROID_CERT_TEST_STATE_ROOT}"
  pem_dir="${VIRTROID_CERT_TEST_PEM_DIR:?set VIRTROID_CERT_TEST_PEM_DIR}"
  flock_binary="${VIRTROID_CERT_TEST_FLOCK:?set VIRTROID_CERT_TEST_FLOCK}"
  openssl_binary="${test_openssl_binary:?set VIRTROID_CERT_TEST_OPENSSL}"
  docker_binary="${test_docker_binary:?set VIRTROID_CERT_TEST_DOCKER}"
  curl_binary="${test_curl_binary:?set VIRTROID_CERT_TEST_CURL}"
  sleep_binary="${test_sleep_binary:?set VIRTROID_CERT_TEST_SLEEP}"
elif [ "${test_mode}" != 0 ]; then
  echo "VIRTROID_CERT_TEST_MODE must be 0 or 1" >&2
  exit 1
fi
pem_path="${pem_dir}/virtroid.pem"
maintenance_lock="${state_root}/maintenance.lock"
temp_pem=""
old_pem=""
had_old_pem=0
edge_was_running=0
replacement_active=0

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

read_env_value() {
  local path="$1"
  local sought="$2"
  awk -F= -v sought="${sought}" '
    $1 == sought {
      found++
      value = substr($0, index($0, "=") + 1)
    }
    END {
      if (found != 1 || value == "") exit 1
      print value
    }
  ' "${path}"
}

cleanup() {
  local status=$?
  trap - EXIT

  if [ "${replacement_active}" -eq 1 ]; then
    echo "certificate deployment exited before commit; restoring the previous PEM" >&2
    if restore_previous_pem; then
      if [ "${edge_was_running}" -eq 1 ]; then
        "${docker_binary}" restart virtroid-edge >/dev/null 2>&1 ||
          echo "failed to restart HAProxy with the restored PEM" >&2
      fi
    else
      echo "failed to restore the previous HAProxy PEM" >&2
    fi
  fi
  if [ -n "${temp_pem}" ] && [ -e "${temp_pem}" ]; then
    unlink "${temp_pem}"
  fi
  if [ -n "${old_pem}" ] && [ -e "${old_pem}" ]; then
    unlink "${old_pem}"
  fi
  return "${status}"
}
trap cleanup EXIT

restore_previous_pem() {
  if [ "${had_old_pem}" -eq 1 ]; then
    [ -n "${old_pem}" ] && [ -f "${old_pem}" ] || return 1
    mv -f "${old_pem}" "${pem_path}"
    old_pem=""
  else
    unlink "${pem_path}" 2>/dev/null || true
  fi
  replacement_active=0
}

discard_previous_pem() {
  if [ -n "${old_pem}" ] && [ -e "${old_pem}" ]; then
    unlink "${old_pem}"
  fi
  old_pem=""
  replacement_active=0
}

fail_after_install() {
  local message="$1"
  local restore_failed=0

  printf '%s\n' "${message}" >&2
  if ! restore_previous_pem; then
    echo "failed to restore the previous HAProxy PEM" >&2
    restore_failed=1
  fi
  # A renewal hook must never turn on an edge that was intentionally stopped.
  # If it was running on entry, restart once more so the restored PEM is active.
  if [ "${edge_was_running}" -eq 1 ] && [ "${restore_failed}" -eq 0 ]; then
    if ! "${docker_binary}" restart virtroid-edge >/dev/null; then
      echo "failed to restart HAProxy with the restored PEM" >&2
    fi
  fi
  exit 1
}

if [ ! -r "${lineage}/fullchain.pem" ] || [ ! -r "${lineage}/privkey.pem" ]; then
  die "renewed certificate lineage is incomplete: ${lineage}"
fi
if [ ! -r "${env_file}" ]; then
  die "deployment environment is not readable: ${env_file}"
fi

cert_gid="$(read_env_value "${env_file}" HAPROXY_CERT_GID)" ||
  die "HAPROXY_CERT_GID must appear exactly once and be numeric in ${env_file}"
[[ "${cert_gid}" =~ ^[0-9]+$ ]] ||
  die "HAPROXY_CERT_GID must appear exactly once and be numeric in ${env_file}"

public_url="$(read_env_value "${env_file}" PUBLIC_BASE_URL)" ||
  die "PUBLIC_BASE_URL must appear exactly once in ${env_file}"
public_url_pattern='^https://[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:[0-9]{1,5})?$'
[[ "${public_url}" =~ ${public_url_pattern} ]] ||
  die "PUBLIC_BASE_URL is not an approved HTTPS origin"
public_authority="${public_url#https://}"
public_hostname="${public_authority%%:*}"
if [[ "${public_authority}" == *:* ]]; then
  public_port="${public_authority##*:}"
  [ "$((10#${public_port}))" -ge 1 ] && [ "$((10#${public_port}))" -le 65535 ] ||
    die "PUBLIC_BASE_URL contains an invalid port"
else
  public_port=443
fi

for required_binary in "${openssl_binary}" "${docker_binary}" "${curl_binary}" "${sleep_binary}"; do
  [ "${required_binary#/}" != "${required_binary}" ] ||
    die "certificate helper command paths must be absolute"
  [ -x "${required_binary}" ] || die "certificate helper command is unavailable: ${required_binary}"
done

"${openssl_binary}" x509 -in "${lineage}/fullchain.pem" -noout -checkend 86400 >/dev/null
"${openssl_binary}" x509 -in "${lineage}/fullchain.pem" -noout -checkhost "${public_hostname}" >/dev/null ||
  die "renewed certificate does not cover PUBLIC_BASE_URL hostname ${public_hostname}"
"${openssl_binary}" pkey -in "${lineage}/privkey.pem" -check -noout >/dev/null
cert_public_key="$(
  "${openssl_binary}" x509 -in "${lineage}/fullchain.pem" -pubkey -noout |
    "${openssl_binary}" pkey -pubin -outform DER |
    "${openssl_binary}" dgst -sha256
)"
private_public_key="$(
  "${openssl_binary}" pkey -in "${lineage}/privkey.pem" -pubout -outform DER |
    "${openssl_binary}" dgst -sha256
)"
if [ "${cert_public_key}" != "${private_public_key}" ]; then
  die "renewed certificate and private key do not match"
fi

[ -x "${flock_binary}" ] || die "flock is not installed"
if [ "${test_mode}" = 1 ]; then
  install -d -m 0700 "${state_root}"
else
  install -d -o root -g root -m 0700 "${state_root}"
fi
exec 8>"${maintenance_lock}"
chmod 0600 "${maintenance_lock}"
if ! "${flock_binary}" -n 8; then
  echo "another Virtroid maintenance operation is already running" >&2
  exit 75
fi
if [ "${test_mode}" != 1 ]; then
  [ -r "/proc/$$/fd/255" ] || die "cannot verify the executing certificate helper descriptor"
  executing_helper_digest="$(sha256sum "/proc/$$/fd/255" | awk '{print $1}')"
  installed_helper_digest="$(sha256sum /etc/letsencrypt/renewal-hooks/deploy/virtroid-haproxy.sh | awk '{print $1}')"
  [ "${executing_helper_digest}" = "${installed_helper_digest}" ] || {
    echo "certificate helper changed while this process was starting" >&2
    exit 75
  }
fi

edge_running="$("${docker_binary}" inspect --format '{{.State.Running}}' virtroid-edge 2>/dev/null)" ||
  die "virtroid-edge container is unavailable"
case "${edge_running}" in
  true) edge_was_running=1 ;;
  false) edge_was_running=0 ;;
  *) die "could not determine virtroid-edge state" ;;
esac

if [ "${test_mode}" = 1 ]; then
  install -d -m 0750 "${pem_dir}"
else
  install -d -o root -g "${cert_gid}" -m 0750 "${pem_dir}"
fi
if [ -e "${pem_path}" ] || [ -L "${pem_path}" ]; then
  [ -f "${pem_path}" ] && [ ! -L "${pem_path}" ] ||
    die "existing HAProxy PEM must be a regular non-symlink file"
  old_pem="$(mktemp "${pem_dir}/.virtroid.pem.previous.XXXXXX")"
  cp -p "${pem_path}" "${old_pem}"
  had_old_pem=1
fi

temp_pem="$(mktemp "${pem_dir}/.virtroid.pem.XXXXXX")"
cat "${lineage}/fullchain.pem" "${lineage}/privkey.pem" > "${temp_pem}"
if [ "${test_mode}" != 1 ]; then
  chown root:"${cert_gid}" "${temp_pem}"
fi
chmod 0640 "${temp_pem}"
mv -f "${temp_pem}" "${pem_path}"
temp_pem=""
replacement_active=1

if [ "${edge_was_running}" -eq 0 ]; then
  discard_previous_pem
  exit 0
fi

if ! "${docker_binary}" restart virtroid-edge >/dev/null; then
  fail_after_install "HAProxy failed to restart with the renewed certificate"
fi
if ! "${docker_binary}" exec virtroid-edge haproxy -c -f /usr/local/etc/haproxy/haproxy.cfg; then
  fail_after_install "HAProxy rejected its configuration after certificate renewal"
fi

for ((attempt = 1; attempt <= 30; attempt++)); do
  if "${curl_binary}" --noproxy '*' --proto '=https' --tlsv1.2 \
    --resolve "${public_hostname}:${public_port}:127.0.0.1" \
    -fsS --max-time 2 "${public_url}/healthz" >/dev/null 2>&1; then
    discard_previous_pem
    exit 0
  fi
  "${sleep_binary}" 1
done

fail_after_install "HAProxy did not become healthy after certificate renewal"
