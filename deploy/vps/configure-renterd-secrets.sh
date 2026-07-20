#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly trusted_helper=/usr/local/sbin/virtroid-configure-renterd-secrets
readonly secret_dir=/etc/virtroid/secrets
readonly config_file=${secret_dir}/renterd.yml
readonly password_file=${secret_dir}/renterd-api-password
readonly mysql_password_file=${secret_dir}/renterd-mysql-password
readonly mysql_root_password_file=${secret_dir}/renterd-mysql-root-password
readonly deploy_env=/opt/virtroid/deploy/vps/.env
readonly state_root=/var/lib/virtroid-deploy
readonly ceremony_state=${state_root}/renterd-seed-ceremony.env
readonly min_shards=10
readonly total_shards=30
readonly mysql_image='mysql:8.4@sha256:c592c15aaf4a1961e15d82eb31ea5987dda862d1c4b1e93424438c0e91dc1f8d'

usage() {
  cat >&2 <<'EOF'
Usage:
  virtroid-configure-renterd-secrets configure
  virtroid-configure-renterd-secrets confirm-funded
  virtroid-configure-renterd-secrets verify-secrets
  virtroid-configure-renterd-secrets verify-activation

The configure command is intentionally interactive. Generate the mainnet seed
and wallet address on a separate offline device first, and never paste the seed
into a chat, command argument, shell script, environment file, or ticket.
EOF
}

fail() {
  echo "$1" >&2
  exit 1
}

require_root() {
  [ "$(id -u)" -eq 0 ] || fail "run as root"
  [ "$(readlink -f -- "$0")" = "${trusted_helper}" ] ||
    fail "run the installed root-owned helper: ${trusted_helper}"
}

require_safe_directory() {
  local directory="$1"
  local wanted_mode="$2"
  [ -d "${directory}" ] && [ ! -L "${directory}" ] ||
    fail "unsafe or missing directory: ${directory}"
  [ "$(stat -c '%u' "${directory}")" -eq 0 ] ||
    fail "directory is not root-owned: ${directory}"
  [ "$(stat -c '%a' "${directory}")" = "${wanted_mode}" ] ||
    fail "directory has unsafe mode: ${directory}"
}

require_safe_secret() {
  local file="$1"
  [ -f "${file}" ] && [ ! -L "${file}" ] ||
    fail "unsafe or missing secret file: ${file}"
  [ "$(stat -c '%u' "${file}")" -eq 0 ] ||
    fail "secret file is not root-owned: ${file}"
  case "$(stat -c '%a' "${file}")" in
    400|600) ;;
    *) fail "secret file has unsafe mode: ${file}" ;;
  esac
}

state_value() {
  local key="$1"
  awk -v wanted="${key}" '
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
  ' "${ceremony_state}"
}

validate_secret_files() {
  local api_password config_password database_password config_database_password database_secret seed_count

  require_safe_directory "${secret_dir}" 700
  require_safe_secret "${config_file}"
  require_safe_secret "${password_file}"
  require_safe_secret "${mysql_password_file}"
  require_safe_secret "${mysql_root_password_file}"
  [ "$(stat -c '%s' "${config_file}")" -le 4096 ] ||
    fail "renterd config is unexpectedly large"
  [ "$(stat -c '%s' "${password_file}")" -eq 65 ] ||
    fail "renterd API password file has an invalid size"
  grep -Eq '^[0-9a-f]{64}$' "${password_file}" ||
    fail "renterd API password must be a generated 256-bit hexadecimal value"
  for database_secret in "${mysql_password_file}" "${mysql_root_password_file}"; do
    [ "$(stat -c '%s' "${database_secret}")" -eq 65 ] &&
      grep -Eq '^[0-9a-f]{64}$' "${database_secret}" ||
      fail "renterd database passwords must be generated 256-bit hexadecimal values"
  done

  seed_count="$(grep -Ec '^seed: [a-z]+( [a-z]+){11}$' "${config_file}" || true)"
  [ "${seed_count}" -eq 1 ] || fail "renterd config must contain exactly one 12-word seed"
  [ "$(grep -Ec '^network: mainnet$' "${config_file}" || true)" -eq 1 ] ||
    fail "renterd config is not pinned to mainnet"
  [ "$(grep -Ec '^autoOpenWebUI: false$' "${config_file}" || true)" -eq 1 ] ||
    fail "renterd automatic browser UI must remain disabled"
  [ "$(grep -Ec '^    pprof: false$' "${config_file}" || true)" -eq 1 ] ||
    fail "renterd pprof must remain disabled"
  [ "$(grep -Ec '^    allowUnauthenticatedDownloads: false$' "${config_file}" || true)" -eq 1 ] ||
    fail "renterd unauthenticated downloads must remain disabled"
  [ "$(grep -Ec '^    enabled: false$' "${config_file}" || true)" -eq 1 ] ||
    fail "renterd S3 API must remain disabled"
  [ "$(grep -Ec '^    disable: true$' "${config_file}" || true)" -eq 1 ] ||
    fail "renterd third-party explorer integration must remain disabled"

  api_password="$(tr -d '\n' < "${password_file}")"
  config_password="$(awk '
    /^http:$/ { in_http = 1; next }
    in_http && /^[^[:space:]]/ { in_http = 0 }
    in_http && /^    password: / {
      found++
      value = substr($0, 15)
    }
    END {
      if (found != 1) exit 1
      print value
    }
  ' "${config_file}")" || fail "renterd config must contain exactly one HTTP password"
  [ "${config_password}" = "${api_password}" ] ||
    fail "renterd config and worker API password file do not match"
  database_password="$(tr -d '\n' < "${mysql_password_file}")"
  config_database_password="$(awk '
    /^database:$/ { in_database = 1; next }
    in_database && /^[^[:space:]]/ { in_database = 0 }
    in_database && /^        password: / {
      found++
      value = substr($0, 19)
    }
    END {
      if (found != 1) exit 1
      print value
    }
  ' "${config_file}")" || fail "renterd config must contain exactly one database password"
  [ "${config_database_password}" = "${database_password}" ] ||
    fail "renterd config and database password file do not match"
  grep -Fxq '        uri: renterd-mysql:3306' "${config_file}" ||
    fail "renterd config does not use its isolated MySQL service"
  grep -Fxq '        database: renterd' "${config_file}" ||
    fail "renterd main database name is invalid"
  grep -Fxq '        metricsDatabase: renterd_metrics' "${config_file}" ||
    fail "renterd metrics database name is invalid"
  unset api_password config_password database_password config_database_password
}

validate_ceremony_state() {
  local address backup_at copies configured_min configured_total

  require_safe_secret "${ceremony_state}"
  address="$(state_value RENTERD_WALLET_ADDRESS)" || fail "ceremony state is missing the wallet address"
  backup_at="$(state_value RENTERD_OFFLINE_BACKUP_VERIFIED_AT)" || fail "ceremony state is missing backup verification"
  copies="$(state_value RENTERD_OFFLINE_BACKUP_COPIES)" || fail "ceremony state is missing the backup count"
  configured_min="$(state_value RENTERD_MIN_SHARDS)" || fail "ceremony state is missing min shards"
  configured_total="$(state_value RENTERD_TOTAL_SHARDS)" || fail "ceremony state is missing total shards"
  [[ "${address}" =~ ^[0-9a-f]{76}$ ]] || fail "ceremony state contains an invalid wallet address"
  [[ "${backup_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
    fail "ceremony state contains an invalid backup timestamp"
  [ "${copies}" -ge 2 ] || fail "at least two separate offline seed copies are required"
  [ "${configured_min}" = "${min_shards}" ] && [ "${configured_total}" = "${total_shards}" ] ||
    fail "ceremony state does not match the approved 10-of-30 shard policy"
}

validate_environment_state() {
  local configured_address
  require_safe_secret "${deploy_env}"
  if grep -Eq '^(RENTERD_SEED|RENTERD_API_PASSWORD|NODE_SIA_RENTERD_PASSWORD)=.+' "${deploy_env}"; then
    fail "legacy renterd secret values remain in the deployment environment"
  fi
  grep -Fxq "RENTERD_CONFIG_FILE=${config_file}" "${deploy_env}" ||
    fail "deployment environment does not use the protected renterd config path"
  grep -Fxq "RENTERD_API_PASSWORD_FILE=${password_file}" "${deploy_env}" ||
    fail "deployment environment does not use the protected renterd password path"
  grep -Fxq "RENTERD_MYSQL_PASSWORD_FILE=${mysql_password_file}" "${deploy_env}" ||
    fail "deployment environment does not use the protected renterd database password path"
  grep -Fxq "RENTERD_MYSQL_ROOT_PASSWORD_FILE=${mysql_root_password_file}" "${deploy_env}" ||
    fail "deployment environment does not use the protected renterd database root-password path"
  grep -Fxq "RENTERD_MYSQL_IMAGE=${mysql_image}" "${deploy_env}" ||
    fail "deployment environment does not use the approved immutable MySQL image"
  grep -Fxq "NODE_SIA_RENTERD_MIN_SHARDS=${min_shards}" "${deploy_env}" ||
    fail "deployment environment does not use 10 data shards"
  grep -Fxq "NODE_SIA_RENTERD_TOTAL_SHARDS=${total_shards}" "${deploy_env}" ||
    fail "deployment environment does not use 30 total shards"
  configured_address="$(awk -F= '$1 == "NODE_SIA_RENTERD_WALLET_ADDRESS" { found++; value = substr($0, index($0, "=") + 1) } END { if (found != 1) exit 1; print value }' "${deploy_env}")" ||
    fail "deployment environment must contain exactly one renterd wallet address"
  [ "${configured_address}" = "$(state_value RENTERD_WALLET_ADDRESS)" ] ||
    fail "deployment and ceremony wallet addresses do not match"
}

verify_secrets() {
  validate_secret_files
  validate_ceremony_state
  validate_environment_state
  echo "renterd secret and offline-backup gates verified"
}

verify_activation() {
  local funded_at
  verify_secrets >/dev/null
  funded_at="$(state_value RENTERD_FUNDING_CONFIRMED_AT)" ||
    fail "ceremony state is missing the funding confirmation"
  [[ "${funded_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
    fail "mainnet SC funding must be confirmed before renterd can start"
  echo "renterd offline-backup, funding, and shard-policy activation gates verified"
}

update_deploy_environment() {
  local wallet_address="$1"
  local temporary
  temporary="$(mktemp "$(dirname "${deploy_env}")/.renterd-env.XXXXXX")"
  awk \
    -v address="${wallet_address}" \
    -v config="${config_file}" \
    -v password="${password_file}" \
    -v mysql_password="${mysql_password_file}" \
    -v mysql_root_password="${mysql_root_password_file}" \
    -v mysql_image="${mysql_image}" \
    -v min="${min_shards}" \
    -v total="${total_shards}" '
    BEGIN {
      replacement["NODE_SIA_RENTERD_WALLET_ADDRESS"] = address
      replacement["NODE_SIA_RENTERD_MIN_SHARDS"] = min
      replacement["NODE_SIA_RENTERD_TOTAL_SHARDS"] = total
      replacement["RENTERD_CONFIG_FILE"] = config
      replacement["RENTERD_API_PASSWORD_FILE"] = password
      replacement["RENTERD_MYSQL_PASSWORD_FILE"] = mysql_password
      replacement["RENTERD_MYSQL_ROOT_PASSWORD_FILE"] = mysql_root_password
      replacement["RENTERD_MYSQL_IMAGE"] = mysql_image
    }
    {
      separator = index($0, "=")
      key = separator == 0 ? "" : substr($0, 1, separator - 1)
      if (key == "RENTERD_SEED" || key == "RENTERD_API_PASSWORD" || key == "NODE_SIA_RENTERD_PASSWORD") next
      if (key in replacement) {
        if (!seen[key]++) print key "=" replacement[key]
        next
      }
      print
    }
    END {
      order[1] = "NODE_SIA_RENTERD_WALLET_ADDRESS"
      order[2] = "NODE_SIA_RENTERD_MIN_SHARDS"
      order[3] = "NODE_SIA_RENTERD_TOTAL_SHARDS"
      order[4] = "RENTERD_CONFIG_FILE"
      order[5] = "RENTERD_API_PASSWORD_FILE"
      order[6] = "RENTERD_MYSQL_PASSWORD_FILE"
      order[7] = "RENTERD_MYSQL_ROOT_PASSWORD_FILE"
      order[8] = "RENTERD_MYSQL_IMAGE"
      for (i = 1; i <= 8; i++) {
        key = order[i]
        if (!seen[key]) print key "=" replacement[key]
      }
    }
  ' "${deploy_env}" > "${temporary}"
  chmod 0600 "${temporary}"
  chown root:root "${temporary}"
  mv -Tf "${temporary}" "${deploy_env}"
  sync -f "${deploy_env}"
}

configure() {
  local confirmation seed seed_again wallet_address wallet_address_again api_password database_password database_root_password
  local config_temporary password_temporary mysql_password_temporary mysql_root_password_temporary state_temporary
  local seed_pattern='^([a-z]+ ){11}[a-z]+$'

  [ -r /dev/tty ] && [ -w /dev/tty ] || fail "configure requires a private interactive terminal"
  require_safe_secret "${deploy_env}"
  install -d -o root -g root -m 0700 "${secret_dir}" "${state_root}"
  require_safe_directory "${secret_dir}" 700
  require_safe_directory "${state_root}" 700
  if [ -s "${config_file}" ] || [ -s "${ceremony_state}" ]; then
    fail "renterd is already configured; seed rotation is intentionally not automated"
  fi

  printf '%s\n' \
    "This command will not generate or display a seed." \
    "Use a dedicated offline device, generate a mainnet seed with the pinned official renterd v2.9.3 binary," \
    "record the seed exactly on two physical copies stored in separate secure locations, and retain the wallet address." \
    "Do not use the VPS, this Mac, a password manager, cloud storage, screenshots, or chat as either offline copy." >/dev/tty
  printf 'Type OFFLINE COPIES VERIFIED to continue: ' >/dev/tty
  IFS= read -r confirmation </dev/tty
  [ "${confirmation}" = "OFFLINE COPIES VERIFIED" ] || fail "offline seed-copy verification was not confirmed"

  printf 'Enter the 76-character offline wallet address: ' >/dev/tty
  IFS= read -r -s wallet_address </dev/tty
  printf '\nRe-enter the offline wallet address: ' >/dev/tty
  IFS= read -r -s wallet_address_again </dev/tty
  printf '\n' >/dev/tty
  [ "${wallet_address}" = "${wallet_address_again}" ] || fail "wallet addresses do not match"
  [[ "${wallet_address}" =~ ^[0-9a-f]{76}$ ]] || fail "wallet address format is invalid"

  printf 'Enter the 12-word renterd seed from the offline copy: ' >/dev/tty
  IFS= read -r -s seed </dev/tty
  printf '\nRe-enter the 12-word renterd seed: ' >/dev/tty
  IFS= read -r -s seed_again </dev/tty
  printf '\n' >/dev/tty
  [ "${seed}" = "${seed_again}" ] || fail "seed entries do not match"
  [[ "${seed}" =~ ${seed_pattern} ]] || fail "seed must be exactly 12 lowercase words separated by single spaces"

  api_password="$(openssl rand -hex 32)"
  database_password="$(openssl rand -hex 32)"
  database_root_password="$(openssl rand -hex 32)"
  [[ "${api_password}" =~ ^[0-9a-f]{64}$ ]] &&
    [[ "${database_password}" =~ ^[0-9a-f]{64}$ ]] &&
    [[ "${database_root_password}" =~ ^[0-9a-f]{64}$ ]] ||
    fail "failed to generate renterd service passwords"
  config_temporary="$(mktemp "${secret_dir}/.renterd.yml.XXXXXX")"
  password_temporary="$(mktemp "${secret_dir}/.renterd-api-password.XXXXXX")"
  mysql_password_temporary="$(mktemp "${secret_dir}/.renterd-mysql-password.XXXXXX")"
  mysql_root_password_temporary="$(mktemp "${secret_dir}/.renterd-mysql-root-password.XXXXXX")"
  state_temporary="$(mktemp "${state_root}/.renterd-seed-ceremony.XXXXXX")"
  trap 'unset seed seed_again api_password database_password database_root_password; find "${config_temporary:-}" "${password_temporary:-}" "${mysql_password_temporary:-}" "${mysql_root_password_temporary:-}" "${state_temporary:-}" -maxdepth 0 -delete 2>/dev/null || true' EXIT

  printf '%s\n' \
    "seed: ${seed}" \
    'directory: /data' \
    'network: mainnet' \
    'autoOpenWebUI: false' \
    'http:' \
    '    address: ":9980"' \
    "    password: ${api_password}" \
    '    pprof: false' \
    'database:' \
    '    mysql:' \
    '        uri: renterd-mysql:3306' \
    '        user: renterd' \
    "        password: ${database_password}" \
    '        database: renterd' \
    '        metricsDatabase: renterd_metrics' \
    'bus:' \
    '    bootstrap: true' \
    '    gatewayAddr: ":9981"' \
    '    allowPrivateIPs: false' \
    'worker:' \
    '    enabled: true' \
    '    allowUnauthenticatedDownloads: false' \
    'autopilot:' \
    '    enabled: true' \
    's3:' \
    '    enabled: false' \
    'explorer:' \
    '    disable: true' \
    'log:' \
    '    level: info' \
    '    stdout:' \
    '        enabled: true' \
    '        format: json' \
    '        enableANSI: false' \
    '    file:' \
    '        enabled: true' \
    '        format: json' > "${config_temporary}"
  printf '%s\n' "${api_password}" > "${password_temporary}"
  printf '%s\n' "${database_password}" > "${mysql_password_temporary}"
  printf '%s\n' "${database_root_password}" > "${mysql_root_password_temporary}"
  printf '%s\n' \
    "RENTERD_OFFLINE_BACKUP_VERIFIED_AT=$(date -u +%FT%TZ)" \
    'RENTERD_OFFLINE_BACKUP_COPIES=2' \
    "RENTERD_WALLET_ADDRESS=${wallet_address}" \
    "RENTERD_MIN_SHARDS=${min_shards}" \
    "RENTERD_TOTAL_SHARDS=${total_shards}" \
    'RENTERD_FUNDING_CONFIRMED_AT=' > "${state_temporary}"
  chmod 0400 "${config_temporary}" "${password_temporary}" "${mysql_password_temporary}" "${mysql_root_password_temporary}" "${state_temporary}"
  chown root:root "${config_temporary}" "${password_temporary}" "${mysql_password_temporary}" "${mysql_root_password_temporary}" "${state_temporary}"
  sync -f "${config_temporary}"
  sync -f "${password_temporary}"
  sync -f "${mysql_password_temporary}"
  sync -f "${mysql_root_password_temporary}"
  sync -f "${state_temporary}"
  mv -Tf "${config_temporary}" "${config_file}"
  config_temporary=
  mv -Tf "${password_temporary}" "${password_file}"
  password_temporary=
  mv -Tf "${mysql_password_temporary}" "${mysql_password_file}"
  mysql_password_temporary=
  mv -Tf "${mysql_root_password_temporary}" "${mysql_root_password_file}"
  mysql_root_password_temporary=
  mv -Tf "${state_temporary}" "${ceremony_state}"
  state_temporary=
  sync -f "${secret_dir}"
  sync -f "${state_root}"
  unset seed seed_again api_password database_password database_root_password

  update_deploy_environment "${wallet_address}"
  verify_secrets >/dev/null
  trap - EXIT
  echo "renterd secrets installed without starting renterd"
  echo "approved mainnet shard policy: 10 data shards / 30 total shards"
  echo "next gate: fund the recorded address with mainnet SC, then run confirm-funded"
}

confirm_funded() {
  local confirmation temporary
  [ -r /dev/tty ] && [ -w /dev/tty ] || fail "confirm-funded requires a private interactive terminal"
  verify_secrets >/dev/null
  printf '%s\n' \
    "This records an operator assertion only; it does not query a third-party explorer." \
    "Confirm the mainnet transfer was sent to the exact offline-derived address before continuing." >/dev/tty
  printf 'Type MAINNET SC SENT to continue: ' >/dev/tty
  IFS= read -r confirmation </dev/tty
  [ "${confirmation}" = "MAINNET SC SENT" ] || fail "funding was not confirmed"
  temporary="$(mktemp "${state_root}/.renterd-seed-ceremony.XXXXXX")"
  awk -v funded_at="$(date -u +%FT%TZ)" '
    /^RENTERD_FUNDING_CONFIRMED_AT=/ {
      if (!seen++) print "RENTERD_FUNDING_CONFIRMED_AT=" funded_at
      next
    }
    { print }
    END { if (!seen) print "RENTERD_FUNDING_CONFIRMED_AT=" funded_at }
  ' "${ceremony_state}" > "${temporary}"
  chmod 0400 "${temporary}"
  chown root:root "${temporary}"
  sync -f "${temporary}"
  mv -Tf "${temporary}" "${ceremony_state}"
  sync -f "${state_root}"
  verify_activation >/dev/null
  echo "mainnet funding assertion recorded; renterd may now be started for on-chain verification"
}

require_root
case "${1:-}" in
  configure)
    [ "$#" -eq 1 ] || { usage; exit 64; }
    configure
    ;;
  confirm-funded)
    [ "$#" -eq 1 ] || { usage; exit 64; }
    confirm_funded
    ;;
  verify-secrets)
    [ "$#" -eq 1 ] || { usage; exit 64; }
    verify_secrets
    ;;
  verify-activation)
    [ "$#" -eq 1 ] || { usage; exit 64; }
    verify_activation
    ;;
  *)
    usage
    exit 64
    ;;
esac
