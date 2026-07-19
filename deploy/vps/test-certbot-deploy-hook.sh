#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
openssl_binary="$(command -v openssl)"
[ -x "${openssl_binary}" ] || {
  echo "OpenSSL is required for the certificate deployment test" >&2
  exit 1
}
tmp_dir="$(mktemp -d)"
trap 'find "${tmp_dir}" -depth -delete' EXIT
umask 0077

lineage="${tmp_dir}/lineage"
wrong_lineage="${tmp_dir}/wrong-lineage"
pem_dir="${tmp_dir}/tls"
state_root="${tmp_dir}/state"
fake_bin="${tmp_dir}/bin"
fake_state="${tmp_dir}/fake-state"
env_file="${tmp_dir}/.env"
mkdir -p "${lineage}" "${wrong_lineage}" "${pem_dir}" "${state_root}" "${fake_bin}" "${fake_state}"

"${openssl_binary}" req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "${lineage}/privkey.pem" \
  -out "${lineage}/fullchain.pem" \
  -subj /CN=virtroid.example \
  -addext subjectAltName=DNS:virtroid.example >/dev/null 2>&1
"${openssl_binary}" req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "${wrong_lineage}/privkey.pem" \
  -out "${wrong_lineage}/fullchain.pem" \
  -subj /CN=wrong.example \
  -addext subjectAltName=DNS:wrong.example >/dev/null 2>&1
printf 'HAPROXY_CERT_GID=99\nPUBLIC_BASE_URL=https://virtroid.example\n' > "${env_file}"
cat "${lineage}/fullchain.pem" "${lineage}/privkey.pem" > "${tmp_dir}/expected.pem"

cat > "${fake_bin}/flock" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${VIRTROID_CERT_FAKE_STATE:?}/flock.log"
[ "$#" -eq 2 ]
[ "$1" = -n ]
[ "$2" = 8 ]
[ "${VIRTROID_CERT_FAKE_FLOCK_FAIL:-0}" != 1 ]
EOF

cat > "${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
fake_state="${VIRTROID_CERT_FAKE_STATE:?}"
printf '%s\n' "$*" >> "${fake_state}/docker.log"
case "${1:-}" in
  inspect)
    cat "${fake_state}/edge-state"
    ;;
  restart)
    count="$(cat "${fake_state}/restart-count")"
    count=$((count + 1))
    printf '%s\n' "${count}" > "${fake_state}/restart-count"
    if [ "${VIRTROID_CERT_FAKE_RESTART_FAIL_ONCE:-0}" = 1 ] && [ "${count}" -eq 1 ]; then
      printf 'false\n' > "${fake_state}/edge-state"
      exit 1
    fi
    printf 'true\n' > "${fake_state}/edge-state"
    ;;
  exec)
    [ "${VIRTROID_CERT_FAKE_EXEC_FAIL:-0}" != 1 ]
    ;;
  *)
    exit 64
    ;;
esac
EOF

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${VIRTROID_CERT_FAKE_STATE:?}/curl.log"
for argument in "$@"; do
  case "${argument}" in
    -*k*)
      echo "curl TLS verification was disabled" >&2
      exit 92
      ;;
  esac
done
[ "${VIRTROID_CERT_FAKE_HEALTH_FAIL:-0}" != 1 ]
EOF

cat > "${fake_bin}/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod 0700 "${fake_bin}/flock" "${fake_bin}/docker" "${fake_bin}/curl" "${fake_bin}/sleep"

reset_fixture() {
  local running="$1"
  printf '%s\n' "${running}" > "${fake_state}/edge-state"
  printf '0\n' > "${fake_state}/restart-count"
  : > "${fake_state}/docker.log"
  : > "${fake_state}/curl.log"
  : > "${fake_state}/flock.log"
  printf 'old certificate bundle\n' > "${pem_dir}/virtroid.pem"
  cp "${pem_dir}/virtroid.pem" "${tmp_dir}/old.pem"
}

run_hook() {
  PATH="${fake_bin}:${PATH}" \
    VIRTROID_ENV_FILE="${env_file}" \
    RENEWED_LINEAGE="${RENEWED_LINEAGE_OVERRIDE:-${lineage}}" \
    VIRTROID_CERT_TEST_MODE=1 \
    VIRTROID_CERT_TEST_STATE_ROOT="${state_root}" \
    VIRTROID_CERT_TEST_PEM_DIR="${pem_dir}" \
    VIRTROID_CERT_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_CERT_TEST_OPENSSL="${openssl_binary}" \
    VIRTROID_CERT_TEST_DOCKER="${fake_bin}/docker" \
    VIRTROID_CERT_TEST_CURL="${fake_bin}/curl" \
    VIRTROID_CERT_TEST_SLEEP="${fake_bin}/sleep" \
    VIRTROID_CERT_FAKE_STATE="${fake_state}" \
    VIRTROID_CERT_FAKE_FLOCK_FAIL="${VIRTROID_CERT_FAKE_FLOCK_FAIL:-0}" \
    VIRTROID_CERT_FAKE_RESTART_FAIL_ONCE="${VIRTROID_CERT_FAKE_RESTART_FAIL_ONCE:-0}" \
    VIRTROID_CERT_FAKE_EXEC_FAIL="${VIRTROID_CERT_FAKE_EXEC_FAIL:-0}" \
    VIRTROID_CERT_FAKE_HEALTH_FAIL="${VIRTROID_CERT_FAKE_HEALTH_FAIL:-0}" \
    bash "${script_dir}/certbot-deploy-hook.sh"
}

reset_fixture true
run_hook >/dev/null
cmp -s "${tmp_dir}/expected.pem" "${pem_dir}/virtroid.pem"
[ "$(cat "${fake_state}/restart-count")" -eq 1 ]
grep -q '^exec virtroid-edge haproxy -c -f /usr/local/etc/haproxy/haproxy.cfg$' "${fake_state}/docker.log"
grep -Fq -- '--resolve virtroid.example:443:127.0.0.1' "${fake_state}/curl.log"
grep -Fq 'https://virtroid.example/healthz' "${fake_state}/curl.log"
grep -q '^-n 8$' "${fake_state}/flock.log"

reset_fixture false
run_hook >/dev/null
cmp -s "${tmp_dir}/expected.pem" "${pem_dir}/virtroid.pem"
[ "$(cat "${fake_state}/restart-count")" -eq 0 ]
[ "$(cat "${fake_state}/edge-state")" = false ]
[ ! -s "${fake_state}/curl.log" ]
if grep -Eq '^(restart|exec) ' "${fake_state}/docker.log"; then
  echo "certificate hook changed an edge that was stopped" >&2
  exit 1
fi

reset_fixture true
if RENEWED_LINEAGE_OVERRIDE="${wrong_lineage}" run_hook >/dev/null 2>&1; then
  echo "certificate hook accepted a certificate for the wrong hostname" >&2
  exit 1
fi
cmp -s "${tmp_dir}/old.pem" "${pem_dir}/virtroid.pem"
[ ! -s "${fake_state}/docker.log" ]

reset_fixture true
if VIRTROID_CERT_FAKE_FLOCK_FAIL=1 run_hook >/dev/null 2>&1; then
  echo "certificate hook continued without the maintenance lock" >&2
  exit 1
fi
cmp -s "${tmp_dir}/old.pem" "${pem_dir}/virtroid.pem"
[ ! -s "${fake_state}/docker.log" ]

reset_fixture true
if VIRTROID_CERT_FAKE_RESTART_FAIL_ONCE=1 run_hook >/dev/null 2>&1; then
  echo "certificate hook accepted a failed HAProxy restart" >&2
  exit 1
fi
cmp -s "${tmp_dir}/old.pem" "${pem_dir}/virtroid.pem"
[ "$(cat "${fake_state}/restart-count")" -eq 2 ]
[ "$(cat "${fake_state}/edge-state")" = true ]

reset_fixture true
if VIRTROID_CERT_FAKE_EXEC_FAIL=1 run_hook >/dev/null 2>&1; then
  echo "certificate hook accepted failed HAProxy validation" >&2
  exit 1
fi
cmp -s "${tmp_dir}/old.pem" "${pem_dir}/virtroid.pem"
[ "$(cat "${fake_state}/restart-count")" -eq 2 ]

reset_fixture true
if VIRTROID_CERT_FAKE_HEALTH_FAIL=1 run_hook >/dev/null 2>&1; then
  echo "certificate hook accepted failed public HTTPS health checks" >&2
  exit 1
fi
cmp -s "${tmp_dir}/old.pem" "${pem_dir}/virtroid.pem"
[ "$(cat "${fake_state}/restart-count")" -eq 2 ]
[ "$(wc -l < "${fake_state}/curl.log" | tr -d ' ')" -eq 30 ]

echo "certificate deployment safety checks passed"
