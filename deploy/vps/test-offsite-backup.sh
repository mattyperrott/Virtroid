#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
trap 'find "${tmp_dir}" -depth -delete' EXIT
umask 0077

backup_root="${tmp_dir}/backups"
restore_root="${tmp_dir}/restore"
fake_bin="${tmp_dir}/bin"
config_dir="${tmp_dir}/config"
backup_set="${backup_root}/daily-20990101T000000Z"
mkdir -p "${backup_set}" "${restore_root}" "${fake_bin}" "${config_dir}" \
  "${tmp_dir}/payload/virtroid" "${tmp_dir}/deployed/vps" "${tmp_dir}/preinstall/vps"
printf 'runtime data\n' > "${tmp_dir}/payload/virtroid/data"
printf 'installed reviewed tree\n' > "${tmp_dir}/deployed/vps/deploy.sh"
chmod 0755 "${tmp_dir}/deployed/vps/deploy.sh"
installed_digest="$("${script_dir}/deployment-tree-digest.sh" "${tmp_dir}/deployed/vps")"
printf 'prior reviewed tree\n' > "${tmp_dir}/preinstall/vps/deploy.sh"
chmod 0755 "${tmp_dir}/preinstall/vps/deploy.sh"
preinstall_digest="$("${script_dir}/deployment-tree-digest.sh" "${tmp_dir}/preinstall/vps")"
printf '%s\n' "${preinstall_digest}" > "${tmp_dir}/preinstall/DEPLOYMENT_TREE_SHA256"
printf 'POSTGRES_PASSWORD=test-only\n' > "${backup_set}/deploy.env"
printf 'VIRTROID_BACKEND_IMAGE=virtroid-backend:release-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\nVIRTROID_BACKEND_IMAGE_ID=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nVIRTROID_BACKEND_MANIFEST_DIGEST=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\nVIRTROID_SOURCE_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\nVIRTROID_SCHEMA_VERSION=1\nVIRTROID_DEPLOYMENT_TREE_SHA256=%s\n' "${preinstall_digest}" > "${backup_set}/release-current.env"
printf 'test metadata\ninstalled_deployment_tree_sha256=%s\nrelease_deployment_tree_sha256=%s\npreinstall_deployment_tree_included=1\npreinstall_deployment_tree_sha256=%s\n' "${installed_digest}" "${preinstall_digest}" "${preinstall_digest}" > "${backup_set}/metadata.txt"
printf 'test database dump\n' > "${backup_set}/virtroid-postgres.dump"
tar -C "${tmp_dir}/payload" -czf "${backup_set}/srv-virtroid.tgz" virtroid
tar -C "${tmp_dir}/deployed" -czf "${backup_set}/deploy-tree.tgz" vps
tar -C "${tmp_dir}/preinstall" -czf "${backup_set}/preinstall-deploy-tree.tgz" vps DEPLOYMENT_TREE_SHA256

if command -v sha256sum >/dev/null 2>&1; then
  (
    cd "${backup_set}"
    sha256sum deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS
  )
else
  (
    cd "${backup_set}"
    shasum -a 256 deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS
  )
fi
chmod 0700 "${backup_set}"
chmod 0600 "${backup_set}"/*
touch "${backup_set}"

mkdir -p "${fake_bin}/restore-fixture"
cp -Rp "${backup_set}" "${fake_bin}/restore-fixture/"
printf '%s\n' "${backup_root}" > "${fake_bin}/backup-root"

cat > "${fake_bin}/restic" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

fake_dir="$(cd "$(dirname "$0")" && pwd)"
{
  printf '%s' "${1:-}"
  shift || true
  printf '\t%s' "$@"
  printf '\n'
} >> "${fake_dir}/restic.log"
printf '%s\n' "${RESTIC_REPOSITORY:?}" > "${fake_dir}/repository"
printf '%s\n' "${RESTIC_PASSWORD_FILE:?}" > "${fake_dir}/password-file"
printf '%s\n' "${AWS_SECRET_ACCESS_KEY:?}" > "${fake_dir}/secret-key"
[ -z "${EVIL_INHERITED:-}" ] || exit 90

if [ "$(tail -n 1 "${fake_dir}/restic.log" | cut -f1)" = restore ]; then
  target=""
  previous=""
  for argument in "$@"; do
    if [ "${previous}" = --target ]; then
      target="${argument}"
      break
    fi
    previous="${argument}"
  done
  [ -n "${target}" ]
  backup_root="$(cat "${fake_dir}/backup-root")"
  mkdir -p "${target}${backup_root}"
  cp -Rp "${fake_dir}/restore-fixture/." "${target}${backup_root}/"
fi
EOF
chmod 0700 "${fake_bin}/restic"

cat > "${fake_bin}/pg_restore" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = --file=/dev/null ]
[ -s "$2" ]
grep -q '^corrupt-payload$' "$2" && exit 1
exit 0
EOF
chmod 0700 "${fake_bin}/pg_restore"

cat > "${fake_bin}/flock" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$(dirname "$0")/flock.log"
[ "$#" -eq 2 ]
[ "$1" = -n ]
[ "$2" = 8 ] || [ "$2" = 9 ]
[ "${VIRTROID_FAKE_FLOCK_FAIL:-0}" != 1 ]
EOF
chmod 0700 "${fake_bin}/flock"

cat > "${fake_bin}/getent" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 2 ]
[ "$1" = ahosts ]
case "$2" in
  s3.example.test|remote.example.test|storage.example.test)
    printf '203.0.113.10 STREAM %s\n' "$2"
    ;;
  local-v4.example.test|local-openstack.example.test)
    printf '185.223.207.157 STREAM %s\n' "$2"
    ;;
  mixed.example.test)
    printf '203.0.113.20 STREAM %s\n' "$2"
    printf '10.20.30.40 STREAM %s\n' "$2"
    ;;
  local-v6.example.test)
    printf '2001:db8::10 STREAM %s\n' "$2"
    ;;
  loop-alias.example.test)
    printf '127.0.0.2 STREAM %s\n' "$2"
    ;;
  link-local.example.test)
    printf '169.254.23.9 STREAM %s\n' "$2"
    ;;
  link-local-v6.example.test)
    printf 'fe80::1234 STREAM %s\n' "$2"
    ;;
  empty.example.test)
    exit 0
    ;;
  unresolved.example.test)
    exit 2
    ;;
  *)
    exit 3
    ;;
esac
EOF
chmod 0700 "${fake_bin}/getent"

cat > "${fake_bin}/hostname" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 1 ]
case "$1" in
  -s)
    printf 'virtroid-cp\n'
    ;;
  -f)
    printf 'virtroid-cp.example.test\n'
    ;;
  -I)
    case "${VIRTROID_FAKE_HOSTNAME_I_MODE:-addresses}" in
      addresses) printf '185.223.207.157 10.20.30.40 2001:db8::10\n' ;;
      empty) exit 0 ;;
      fail) exit 2 ;;
      *) exit 3 ;;
    esac
    ;;
  *)
    exit 4
    ;;
esac
EOF
chmod 0700 "${fake_bin}/hostname"
export VIRTROID_RESTIC_TEST_GETENT="${fake_bin}/getent"
export VIRTROID_RESTIC_TEST_HOSTNAME="${fake_bin}/hostname"

password_file="${config_dir}/restic-password"
config_file="${config_dir}/restic.env"
printf 'test-only-restic-password\n' > "${password_file}"
marker="${tmp_dir}/command-substitution-ran"
cat > "${config_file}" <<EOF
RESTIC_REPOSITORY=s3:s3.example.test/virtroid
RESTIC_PASSWORD_FILE=${password_file}
AWS_ACCESS_KEY_ID=test-access-key
AWS_SECRET_ACCESS_KEY=\$(touch ${marker})
AWS_REGION=test-region
VIRTROID_RESTIC_BACKUP_HOST=virtroid-cp
VIRTROID_RESTIC_TAG=virtroid-offsite
VIRTROID_RESTIC_CHECK_PERCENT=5
VIRTROID_RESTIC_MAX_LOCAL_AGE_HOURS=12
EOF
chmod 0600 "${password_file}" "${config_file}"

run_script() {
  VIRTROID_RESTIC_TEST_MODE=1 \
    VIRTROID_RESTIC_ENV_FILE="${config_file}" \
    VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
    VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
    VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
    VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
    VIRTROID_FAKE_FLOCK_FAIL="${VIRTROID_FAKE_FLOCK_FAIL:-0}" \
    EVIL_INHERITED=must-not-reach-restic \
    bash "${script_dir}/virtroid-offsite-backup.sh" "$@"
}

expect_config_rejected() {
  local candidate_config="$1"
  local failure_message="$2"
  if config_file="${candidate_config}" run_script snapshots >/dev/null 2>&1; then
    echo "${failure_message}" >&2
    exit 1
  fi
}

expect_resolved_repository_rejected() {
  local endpoint_host="$1"
  local failure_message="$2"
  local candidate_config="${config_dir}/resolved-${endpoint_host}.env"
  sed "s|^RESTIC_REPOSITORY=.*|RESTIC_REPOSITORY=rest:https://${endpoint_host}:8000/virtroid|" \
    "${config_file}" > "${candidate_config}"
  chmod 0600 "${candidate_config}"
  expect_config_rejected "${candidate_config}" "${failure_message}"
}

symlink_restore_target="${tmp_dir}/symlink-restore-target"
symlink_restore_root="${tmp_dir}/symlink-restore-root"
mkdir -m 0700 "${symlink_restore_target}"
ln -s "${symlink_restore_target}" "${symlink_restore_root}"
if restore_root="${symlink_restore_root}" run_script snapshots >/dev/null 2>&1; then
  echo "off-site operation accepted a symlinked Restic state directory" >&2
  exit 1
fi
[ -L "${symlink_restore_root}" ] || {
  echo "off-site operation followed or replaced a symlinked Restic state directory" >&2
  exit 1
}

stale_restore="${restore_root}/restore-check.STALE"
stale_validation="${restore_root}/tree-validation.STALE"
stale_listing="${restore_root}/tree-list.STALE"
mkdir -p "${stale_restore}" "${stale_validation}"
printf 'must survive a rejected lock acquisition\n' > "${stale_restore}/marker"
printf 'must survive a rejected lock acquisition\n' > "${stale_validation}/marker"
printf '/private/deployment/path\0' > "${stale_listing}"
if VIRTROID_FAKE_FLOCK_FAIL=1 run_script snapshots >/dev/null 2>&1; then
  echo "off-site operation continued without acquiring its internal lock" >&2
  exit 1
fi
[ -f "${stale_restore}/marker" ] || {
  echo "stale restore cleanup ran before the internal operation lock" >&2
  exit 1
}
[ -f "${stale_validation}/marker" ] || {
  echo "stale validation cleanup ran before the internal operation lock" >&2
  exit 1
}
[ -f "${stale_listing}" ] || {
  echo "stale tree-list cleanup ran before the internal operation lock" >&2
  exit 1
}

run_script backup >/dev/null
[ ! -e "${stale_restore}" ] || {
  echo "locked off-site operation did not clean a stale restore directory" >&2
  exit 1
}
[ ! -e "${stale_validation}" ] || {
  echo "locked off-site operation did not clean a stale validation directory" >&2
  exit 1
}
[ ! -e "${stale_listing}" ] || {
  echo "locked off-site operation did not clean a stale tree-list file" >&2
  exit 1
}

wrong_type_validation="${restore_root}/tree-validation.BADTYPE"
printf 'not a directory\n' > "${wrong_type_validation}"
if run_script snapshots >/dev/null 2>&1; then
  echo "off-site operation accepted a stale validation path with the wrong type" >&2
  exit 1
fi
[ -f "${wrong_type_validation}" ] || {
  echo "off-site operation removed an untrusted stale validation path" >&2
  exit 1
}
find "${wrong_type_validation}" -delete

symlink_listing="${restore_root}/tree-list.SYMLINK"
ln -s "${password_file}" "${symlink_listing}"
if run_script snapshots >/dev/null 2>&1; then
  echo "off-site operation accepted a stale tree-list symlink" >&2
  exit 1
fi
[ -L "${symlink_listing}" ] || {
  echo "off-site operation followed or removed an untrusted stale tree-list symlink" >&2
  exit 1
}
find "${symlink_listing}" -delete

insecure_listing="${restore_root}/tree-list.INSECURE"
printf '/private/deployment/path\0' > "${insecure_listing}"
chmod 0644 "${insecure_listing}"
if run_script snapshots >/dev/null 2>&1; then
  echo "off-site operation accepted an insecure stale tree-list mode" >&2
  exit 1
fi
[ -f "${insecure_listing}" ] || {
  echo "off-site operation removed an insecure stale tree-list file" >&2
  exit 1
}
chmod 0600 "${insecure_listing}"
run_script snapshots >/dev/null
[ ! -e "${insecure_listing}" ] || {
  echo "off-site operation did not clean a trusted stale tree-list file" >&2
  exit 1
}
grep -q '^-n 8$' "${fake_bin}/flock.log"
grep -q '^-n 9$' "${fake_bin}/flock.log"
[ ! -e "${marker}" ] || {
  echo "Restic configuration executed shell syntax" >&2
  exit 1
}
grep -q '^backup' "${fake_bin}/restic.log"
grep -q '^check' "${fake_bin}/restic.log"
if grep -q '^forget' "${fake_bin}/restic.log"; then
  echo "retention ran with deletion disabled" >&2
  exit 1
fi
[ "$(cat "${fake_bin}/repository")" = s3:s3.example.test/virtroid ]
[ "$(cat "${fake_bin}/password-file")" = "${password_file}" ]
[ "$(cat "${fake_bin}/secret-key")" = "\$(touch ${marker})" ]
if grep -Fq "${marker}" "${fake_bin}/restic.log"; then
  echo "backend credential was exposed as a Restic command argument" >&2
  exit 1
fi

printf 'VIRTROID_RESTIC_PRUNE_ENABLED=true\n' >> "${config_file}"
run_script backup >/dev/null
grep -q '^forget' "${fake_bin}/restic.log"
grep -q -- '--group-by' "${fake_bin}/restic.log"
grep -q $'forget.*\t--host\tvirtroid-cp\t--tag\tvirtroid-offsite' "${fake_bin}/restic.log"

run_script restore-check >/dev/null

fixture_set="${fake_bin}/restore-fixture/$(basename "${backup_set}")"
cp "${fixture_set}/virtroid-postgres.dump" "${tmp_dir}/valid-postgres.dump"
cp "${fixture_set}/SHA256SUMS" "${tmp_dir}/valid-SHA256SUMS"
printf 'corrupt-payload\n' > "${fixture_set}/virtroid-postgres.dump"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${fixture_set}" && sha256sum deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
else
  (cd "${fixture_set}" && shasum -a 256 deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
fi
if run_script restore-check >/dev/null 2>&1; then
  echo "restore check accepted a checksum-valid unreadable database payload" >&2
  exit 1
fi
cp "${tmp_dir}/valid-postgres.dump" "${fixture_set}/virtroid-postgres.dump"
cp "${tmp_dir}/valid-SHA256SUMS" "${fixture_set}/SHA256SUMS"

extraction_error_first="${tmp_dir}/extraction-error-first"
extraction_error_second="${tmp_dir}/extraction-error-second"
mkdir -p "${extraction_error_first}/conflict" "${extraction_error_second}"
cp -Rp "${tmp_dir}/deployed/vps" "${extraction_error_first}/vps"
printf 'first archive entry\n' > "${extraction_error_first}/conflict/child"
printf 'conflicting later archive entry\n' > "${extraction_error_second}/conflict"
tar -C "${extraction_error_first}" -cf "${tmp_dir}/extraction-error.tar" vps conflict
tar -C "${extraction_error_second}" -rf "${tmp_dir}/extraction-error.tar" conflict
gzip -c "${tmp_dir}/extraction-error.tar" > "${fixture_set}/deploy-tree.tgz"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${fixture_set}" && sha256sum deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
else
  (cd "${fixture_set}" && shasum -a 256 deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
fi
if run_script restore-check >/dev/null 2>"${tmp_dir}/extraction-error.log"; then
  echo "restore check ignored a deployment-tree extraction error after restoring matching content" >&2
  exit 1
fi
grep -q 'installed deployment-tree archive failed trusted digest verification' \
  "${tmp_dir}/extraction-error.log" || {
  echo "extraction-error fixture failed outside deployment-tree validation" >&2
  exit 1
}
cp "${backup_set}/deploy-tree.tgz" "${fixture_set}/deploy-tree.tgz"
cp "${backup_set}/SHA256SUMS" "${fixture_set}/SHA256SUMS"

sed 's/^preinstall_deployment_tree_sha256=.*/preinstall_deployment_tree_sha256=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd/' \
  "${fixture_set}/metadata.txt" > "${fixture_set}/metadata.txt.new"
mv "${fixture_set}/metadata.txt.new" "${fixture_set}/metadata.txt"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${fixture_set}" && sha256sum deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
else
  (cd "${fixture_set}" && shasum -a 256 deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
fi
if run_script restore-check >/dev/null 2>&1; then
  echo "restore check accepted a prior deployment tree with the wrong recorded digest" >&2
  exit 1
fi
cp "${backup_set}/metadata.txt" "${fixture_set}/metadata.txt"
cp "${backup_set}/SHA256SUMS" "${fixture_set}/SHA256SUMS"

tampered_tree="${tmp_dir}/tampered-tree"
mkdir -p "${tampered_tree}"
tar -xzf "${backup_set}/preinstall-deploy-tree.tgz" -C "${tampered_tree}"
printf 'tampered prior tree\n' >> "${tampered_tree}/vps/deploy.sh"
tar -C "${tampered_tree}" -czf "${fixture_set}/preinstall-deploy-tree.tgz" vps DEPLOYMENT_TREE_SHA256
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${fixture_set}" && sha256sum deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
else
  (cd "${fixture_set}" && shasum -a 256 deploy.env release-current.env deploy-tree.tgz metadata.txt preinstall-deploy-tree.tgz srv-virtroid.tgz virtroid-postgres.dump > SHA256SUMS)
fi
if run_script restore-check >/dev/null 2>&1; then
  echo "restore check accepted modified prior deployment-tree content" >&2
  exit 1
fi
cp "${backup_set}/preinstall-deploy-tree.tgz" "${fixture_set}/preinstall-deploy-tree.tgz"
cp "${backup_set}/SHA256SUMS" "${fixture_set}/SHA256SUMS"

printf 'corrupt\n' >> "${fake_bin}/restore-fixture/$(basename "${backup_set}")/virtroid-postgres.dump"
if run_script restore-check >/dev/null 2>&1; then
  echo "restore check accepted a corrupt backup" >&2
  exit 1
fi

chmod 0644 "${config_file}"
if run_script snapshots >/dev/null 2>&1; then
  echo "world-readable Restic configuration was accepted" >&2
  exit 1
fi
chmod 0600 "${config_file}"

cp "${config_file}" "${config_dir}/local.env"
sed 's|^RESTIC_REPOSITORY=.*|RESTIC_REPOSITORY=/tmp/not-offsite|' \
  "${config_dir}/local.env" > "${config_dir}/local.env.new"
mv "${config_dir}/local.env.new" "${config_dir}/local.env"
chmod 0600 "${config_dir}/local.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/local.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
    VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
    VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
    VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "local Restic repository was accepted" >&2
  exit 1
fi

cp "${config_file}" "${config_dir}/rclone.env"
sed 's|^RESTIC_REPOSITORY=.*|RESTIC_REPOSITORY=rclone:local:/tmp/not-offsite|' \
  "${config_dir}/rclone.env" > "${config_dir}/rclone.env.new"
mv "${config_dir}/rclone.env.new" "${config_dir}/rclone.env"
chmod 0600 "${config_dir}/rclone.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/rclone.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
    VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
    VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
    VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "indirection through an rclone repository was accepted" >&2
  exit 1
fi

cp "${config_file}" "${config_dir}/loopback.env"
sed 's|^RESTIC_REPOSITORY=.*|RESTIC_REPOSITORY=rest:https://127.0.0.1:8000/virtroid|' \
  "${config_dir}/loopback.env" > "${config_dir}/loopback.env.new"
mv "${config_dir}/loopback.env.new" "${config_dir}/loopback.env"
chmod 0600 "${config_dir}/loopback.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/loopback.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
    VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
    VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
    VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "loopback Restic repository was accepted as off-site" >&2
  exit 1
fi

cp "${config_file}" "${config_dir}/s3-loopback.env"
sed 's|^RESTIC_REPOSITORY=.*|RESTIC_REPOSITORY=s3:https://127.0.0.1:9000/virtroid|' \
  "${config_dir}/s3-loopback.env" > "${config_dir}/s3-loopback.env.new"
mv "${config_dir}/s3-loopback.env.new" "${config_dir}/s3-loopback.env"
chmod 0600 "${config_dir}/s3-loopback.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/s3-loopback.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
  VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
  VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
  VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
  VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "loopback S3 Restic repository was accepted as off-site" >&2
  exit 1
fi

cp "${config_file}" "${config_dir}/s3-localhost.env"
sed 's|^RESTIC_REPOSITORY=.*|RESTIC_REPOSITORY=s3:localhost:9000/virtroid|' \
  "${config_dir}/s3-localhost.env" > "${config_dir}/s3-localhost.env.new"
mv "${config_dir}/s3-localhost.env.new" "${config_dir}/s3-localhost.env"
chmod 0600 "${config_dir}/s3-localhost.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/s3-localhost.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
  VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
  VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
  VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
  VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "localhost S3 Restic repository was accepted as off-site" >&2
  exit 1
fi

expect_resolved_repository_rejected \
  local-v4.example.test \
  "Restic repository alias resolving to a local IPv4 interface was accepted as off-site"
expect_resolved_repository_rejected \
  mixed.example.test \
  "Restic repository with a later local DNS answer was accepted as off-site"
expect_resolved_repository_rejected \
  local-v6.example.test \
  "Restic repository alias resolving to a local IPv6 interface was accepted as off-site"
expect_resolved_repository_rejected \
  loop-alias.example.test \
  "Restic repository alias resolving to loopback was accepted as off-site"
expect_resolved_repository_rejected \
  link-local.example.test \
  "Restic repository alias resolving to IPv4 link-local was accepted as off-site"
expect_resolved_repository_rejected \
  link-local-v6.example.test \
  "Restic repository alias resolving to IPv6 link-local was accepted as off-site"
expect_resolved_repository_rejected \
  unresolved.example.test \
  "Restic repository with failed DNS resolution was accepted as off-site"
expect_resolved_repository_rejected \
  empty.example.test \
  "Restic repository with an empty DNS result was accepted as off-site"

if VIRTROID_FAKE_HOSTNAME_I_MODE=fail run_script snapshots >/dev/null 2>&1; then
  echo "off-site endpoint validation ignored local-interface discovery failure" >&2
  exit 1
fi
if VIRTROID_FAKE_HOSTNAME_I_MODE=empty run_script snapshots >/dev/null 2>&1; then
  echo "off-site endpoint validation accepted an empty local-interface result" >&2
  exit 1
fi

cp "${config_file}" "${config_dir}/local-openstack.env"
printf 'OS_STORAGE_URL=https://local-openstack.example.test/v1/container\n' >> \
  "${config_dir}/local-openstack.env"
chmod 0600 "${config_dir}/local-openstack.env"
expect_config_rejected \
  "${config_dir}/local-openstack.env" \
  "OpenStack endpoint alias resolving to a local interface was accepted as off-site"

cp "${config_file}" "${config_dir}/plaintext-storage.env"
printf 'OS_STORAGE_URL=HTTP://storage.example.test/v1/container\n' >> "${config_dir}/plaintext-storage.env"
chmod 0600 "${config_dir}/plaintext-storage.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/plaintext-storage.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
    VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
    VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
    VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "plaintext OpenStack storage endpoint was accepted" >&2
  exit 1
fi

ca_file="${config_dir}/custom-ca.pem"
printf 'test CA\n' > "${ca_file}"
chmod 0666 "${ca_file}"
cp "${config_file}" "${config_dir}/writable-ca.env"
printf 'RESTIC_CACERT=%s\n' "${ca_file}" >> "${config_dir}/writable-ca.env"
chmod 0600 "${config_dir}/writable-ca.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/writable-ca.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
    VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
    VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
    VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "group/world-writable custom CA file was accepted" >&2
  exit 1
fi

cp "${config_file}" "${config_dir}/password-command.env"
printf 'RESTIC_PASSWORD_COMMAND=printf insecure\n' >> "${config_dir}/password-command.env"
chmod 0600 "${config_dir}/password-command.env"
if VIRTROID_RESTIC_TEST_MODE=1 \
  VIRTROID_RESTIC_ENV_FILE="${config_dir}/password-command.env" \
  VIRTROID_RESTIC_TEST_BACKUP_ROOT="${backup_root}" \
    VIRTROID_RESTIC_TEST_RESTORE_ROOT="${restore_root}" \
    VIRTROID_RESTIC_TEST_BINARY="${fake_bin}/restic" \
    VIRTROID_RESTIC_TEST_FLOCK="${fake_bin}/flock" \
    VIRTROID_RESTIC_TEST_PG_RESTORE="${fake_bin}/pg_restore" \
  bash "${script_dir}/virtroid-offsite-backup.sh" snapshots >/dev/null 2>&1; then
  echo "RESTIC_PASSWORD_COMMAND was accepted" >&2
  exit 1
fi

bash "${script_dir}/test-certbot-deploy-hook.sh" >/dev/null

echo "off-site backup safety checks passed"
