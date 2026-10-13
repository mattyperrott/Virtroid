#!/bin/bash
set -euo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

for command_name in comm sort ufw; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "missing firewall command: ${command_name}" >&2
    exit 1
  }
done

allow_http_acme="${VIRTROID_ALLOW_HTTP_ACME:-true}"
# Runtime VMs reach the Sia indexer's application API on 9982 (indexd.md);
# by default while the production env publishes it.
allow_sia_app="${VIRTROID_ALLOW_SIA_APP:-false}"
if [ -z "${VIRTROID_ALLOW_SIA_APP:-}" ] &&
   grep -Eq '^SIA_APP_PUBLISH=9982$' /opt/virtroid/deploy/vps/.env 2>/dev/null; then
  allow_sia_app=true
fi
reset_firewall="${VIRTROID_FIREWALL_RESET:-false}"
case "${allow_http_acme}" in
  true|false) ;;
  *) echo "VIRTROID_ALLOW_HTTP_ACME must be true or false" >&2; exit 64 ;;
esac
case "${allow_sia_app}" in
  true|false) ;;
  *) echo "VIRTROID_ALLOW_SIA_APP must be true or false" >&2; exit 64 ;;
esac
case "${reset_firewall}" in
  true|false) ;;
  *) echo "VIRTROID_FIREWALL_RESET must be true or false" >&2; exit 64 ;;
esac

expected_rules="$(printf '%s\n' \
  'ufw allow 443/tcp' \
  'ufw allow OpenSSH'
)"
if [ "${allow_http_acme}" = true ]; then
  expected_rules="$(printf '%s\n%s\n' "${expected_rules}" 'ufw allow 80/tcp')"
fi
if [ "${allow_sia_app}" = true ]; then
  expected_rules="$(printf '%s\n%s\n' "${expected_rules}" 'ufw allow 9982/tcp')"
fi
expected_rules="$(printf '%s\n' "${expected_rules}" | sort -u)"
actual_rules="$(ufw show added | sed -n '/^ufw /p' | sort -u)"
unexpected_rules="$(comm -23 \
  <(printf '%s\n' "${actual_rules}") \
  <(printf '%s\n' "${expected_rules}"))"

if [ -n "${unexpected_rules}" ] && [ "${reset_firewall}" != true ]; then
  echo "refusing to replace existing UFW rules without VIRTROID_FIREWALL_RESET=true:" >&2
  printf '%s\n' "${unexpected_rules}" >&2
  exit 1
fi

if [ "${reset_firewall}" = true ]; then
  ufw --force disable >/dev/null
  ufw --force reset >/dev/null
fi

ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw default deny routed >/dev/null
ufw allow OpenSSH >/dev/null
if [ "${allow_http_acme}" = true ]; then
  ufw allow 80/tcp >/dev/null
fi
ufw allow 443/tcp >/dev/null
if [ "${allow_sia_app}" = true ]; then
  ufw allow 9982/tcp >/dev/null
fi
ufw logging low >/dev/null
ufw --force enable >/dev/null

actual_rules="$(ufw show added | sed -n '/^ufw /p' | sort -u)"
if [ "${actual_rules}" != "${expected_rules}" ]; then
  echo "effective UFW rules do not match the reviewed Virtroid policy" >&2
  printf 'expected:\n%s\nactual:\n%s\n' "${expected_rules}" "${actual_rules}" >&2
  exit 1
fi

status="$(ufw status verbose)"
grep -q '^Status: active$' <<< "${status}"
grep -q '^Default: deny (incoming), allow (outgoing), deny (routed)$' <<< "${status}"
grep -q '^Logging: on (low)$' <<< "${status}"

echo "Virtroid host firewall configured"
