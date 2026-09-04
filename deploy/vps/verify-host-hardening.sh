#!/bin/bash
set -euo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

require_root_locked="${VIRTROID_REQUIRE_ROOT_LOCKED:-false}"
require_key_only_ssh="${VIRTROID_REQUIRE_KEY_ONLY_SSH:-false}"
allow_http_acme="${VIRTROID_ALLOW_HTTP_ACME:-true}"
for value_name in require_root_locked require_key_only_ssh allow_http_acme; do
  value="${!value_name}"
  case "${value}" in
    true|false) ;;
    *) echo "${value_name} must be true or false" >&2; exit 64 ;;
  esac
done

fail() {
  echo "host hardening verification failed: $*" >&2
  exit 1
}

for command_name in aa-enabled auditctl docker fail2ban-client mountpoint passwd sshd sysctl systemctl ufw; do
  command -v "${command_name}" >/dev/null 2>&1 ||
    fail "missing command: ${command_name}"
done

for unit_name in \
  apparmor.service \
  auditd.service \
  docker.service \
  fail2ban.service \
  ufw.service \
  unattended-upgrades.service \
  apt-daily.timer \
  apt-daily-upgrade.timer \
  redroid-binderfs.service; do
  systemctl is-enabled --quiet "${unit_name}" ||
    fail "${unit_name} is not enabled"
  systemctl is-active --quiet "${unit_name}" ||
    fail "${unit_name} is not active"
done

aa-enabled >/dev/null || fail "AppArmor is not enabled"
mountpoint -q /dev/binderfs || fail "binderfs is not mounted"

expected_rules="$(printf '%s\n' \
  'ufw allow 443/tcp' \
  'ufw allow OpenSSH'
)"
if [ "${allow_http_acme}" = true ]; then
  expected_rules="$(printf '%s\n%s\n' "${expected_rules}" 'ufw allow 80/tcp')"
fi
expected_rules="$(printf '%s\n' "${expected_rules}" | sort -u)"
actual_rules="$(ufw show added | sed -n '/^ufw /p' | sort -u)"
[ "${actual_rules}" = "${expected_rules}" ] ||
  fail "UFW rules differ from the reviewed policy"
ufw_status="$(ufw status verbose)"
grep -q '^Status: active$' <<< "${ufw_status}" ||
  fail "UFW is inactive"
grep -q '^Default: deny (incoming), allow (outgoing), deny (routed)$' <<< "${ufw_status}" ||
  fail "UFW default policy is unsafe"
grep -q '^Logging: on (low)$' <<< "${ufw_status}" ||
  fail "UFW low-volume logging is not enabled"

sshd_effective="$(sshd -T -C 'user=virtroid,host=localhost,addr=127.0.0.1')"
for expected in \
  'maxauthtries 3' \
  'logingracetime 30' \
  'permitemptypasswords no' \
  'kbdinteractiveauthentication no' \
  'x11forwarding no' \
  'allowagentforwarding no' \
  'allowtcpforwarding no' \
  'allowstreamlocalforwarding no' \
  'gatewayports no' \
  'permittunnel no' \
  'permituserenvironment no' \
  'maxsessions 3'; do
  grep -qx "${expected}" <<< "${sshd_effective}" ||
    fail "effective SSH configuration is missing: ${expected}"
done
if [ "${require_key_only_ssh}" = true ]; then
  for expected in \
    'permitrootlogin no' \
    'passwordauthentication no' \
    'pubkeyauthentication yes' \
    'authenticationmethods publickey'; do
    grep -qx "${expected}" <<< "${sshd_effective}" ||
      fail "effective key-only SSH configuration is missing: ${expected}"
  done
fi
if [ "${require_root_locked}" = true ]; then
  root_status="$(passwd -S root | awk '{print $2}')"
  [ "${root_status}" = L ] ||
    fail "root password is not locked"
fi

audit_status="$(auditctl -s)"
grep -q '^enabled 1$' <<< "${audit_status}" ||
  fail "kernel auditing is disabled"
grep -q '^failure 1$' <<< "${audit_status}" ||
  fail "kernel audit failure policy differs from the reviewed policy"
grep -q '^lost 0$' <<< "${audit_status}" ||
  fail "kernel audit events have been lost"
audit_rules="$(auditctl -l)"
for audit_key in identity privilege remote-access firewall virtroid-secrets virtroid-deployment virtroid-release privileged-tools service-config host-identity; do
  grep -q "key=${audit_key}\\|key=\"${audit_key}\"\\|-k ${audit_key}" <<< "${audit_rules}" ||
    fail "audit rule key is missing: ${audit_key}"
done

check_sysctl() {
  local key="$1"
  local expected="$2"
  local actual
  actual="$(sysctl -n "${key}")"
  [ "${actual}" = "${expected}" ] ||
    fail "${key}=${actual}, expected ${expected}"
}

check_sysctl kernel.kptr_restrict 2
check_sysctl kernel.dmesg_restrict 1
check_sysctl kernel.perf_event_paranoid 3
check_sysctl kernel.yama.ptrace_scope 2
check_sysctl kernel.unprivileged_bpf_disabled 0
check_sysctl fs.suid_dumpable 0
check_sysctl net.ipv4.conf.all.accept_redirects 0
check_sysctl net.ipv4.conf.default.accept_redirects 0
check_sysctl net.ipv4.conf.all.send_redirects 0
check_sysctl net.ipv4.conf.default.send_redirects 0
check_sysctl net.ipv6.conf.all.accept_redirects 0
check_sysctl net.ipv6.conf.default.accept_redirects 0

fail2ban-client status sshd >/dev/null ||
  fail "Fail2ban SSH jail is unavailable"

unattended_config=/etc/apt/apt.conf.d/52virtroid-unattended-upgrades
[ -f "${unattended_config}" ] && [ ! -L "${unattended_config}" ] ||
  fail "reviewed unattended-upgrades configuration is missing"
grep -q '^APT::Periodic::Unattended-Upgrade "1";$' "${unattended_config}" ||
  fail "unattended security upgrades are not enabled"
grep -q '^Unattended-Upgrade::Automatic-Reboot "false";$' "${unattended_config}" ||
  fail "automatic package reboot policy differs from the reviewed policy"

if docker inspect virtroidd >/dev/null 2>&1; then
  [ "$(docker inspect virtroidd --format '{{.Config.User}}')" = virtroid ] ||
    fail "virtroidd is not configured as its unprivileged user"
  [ "$(docker inspect virtroidd --format '{{.HostConfig.ReadonlyRootfs}}')" = true ] ||
    fail "virtroidd root filesystem is writable"
  docker inspect virtroidd --format '{{json .HostConfig.CapDrop}}' | grep -q '"ALL"' ||
    fail "virtroidd does not drop all capabilities"
  docker inspect virtroidd --format '{{json .HostConfig.SecurityOpt}}' | grep -q 'no-new-privileges:true' ||
    fail "virtroidd lacks no-new-privileges"
  docker port virtroidd 8080/tcp | grep -qx '127.0.0.1:8080' ||
    fail "virtroidd API is not bound only to loopback"
fi
if docker inspect virtnoded >/dev/null 2>&1; then
  [ "$(docker inspect virtnoded --format '{{.HostConfig.Privileged}}')" = false ] ||
    fail "virtnoded container is unexpectedly privileged"
  [ "$(docker inspect virtnoded --format '{{.AppArmorProfile}}')" = docker-default ] ||
    fail "virtnoded is not confined by docker-default AppArmor"
  docker port virtnoded 8090/tcp | grep -qx '127.0.0.1:8090' ||
    fail "virtnoded relay is not bound only to loopback"
fi

echo "Virtroid host hardening verified"
