#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly trusted_helper=/usr/local/sbin/virtroid-renterd-smoke-test
readonly deployment_dir=/opt/virtroid/deploy/vps
readonly deploy_env=${deployment_dir}/.env
readonly release_state=/var/lib/virtroid-deploy/current.env
readonly smoke_state=/var/lib/virtroid-deploy/renterd-smoke.env

fail() {
  echo "$1" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || fail "run as root"
[ "$(readlink -f -- "$0")" = "${trusted_helper}" ] ||
  fail "run the installed root-owned helper: ${trusted_helper}"
case "${1:-}" in
  run|verify|enable) ;;
  *) echo "usage: virtroid-renterd-smoke-test [run|verify|enable]" >&2; exit 64 ;;
esac
[ "$#" -eq 1 ] || exit 64

state_value() {
  local input_file="$1"
  local key="$2"
  awk -v wanted="${key}" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      separator = index($0, "=")
      if (separator == 0) next
      key = substr($0, 1, separator - 1)
      if (key == wanted) { found++; value = substr($0, separator + 1) }
    }
    END { if (found != 1) exit 1; print value }
  ' "${input_file}"
}

require_safe_state() {
  local file="$1"
  [ -f "${file}" ] && [ ! -L "${file}" ] || fail "unsafe or missing state file: ${file}"
  [ "$(stat -c '%u' "${file}")" -eq 0 ] || fail "state file is not root-owned: ${file}"
  case "$(stat -c '%a' "${file}")" in
    400|600) ;;
    *) fail "state file has an unsafe mode: ${file}" ;;
  esac
}

verify_smoke_state() {
  local recorded_at recorded_source recorded_tree current_source current_tree
  require_safe_state "${release_state}"
  require_safe_state "${smoke_state}"
  recorded_at="$(state_value "${smoke_state}" RENTERD_SMOKE_VERIFIED_AT)" || fail "smoke state is missing its timestamp"
  recorded_source="$(state_value "${smoke_state}" VIRTROID_SOURCE_SHA)" || fail "smoke state is missing its source identity"
  recorded_tree="$(state_value "${smoke_state}" VIRTROID_DEPLOYMENT_TREE_SHA256)" || fail "smoke state is missing its deployment-tree identity"
  current_source="$(state_value "${release_state}" VIRTROID_SOURCE_SHA)" || fail "release state is missing its source identity"
  current_tree="$(state_value "${release_state}" VIRTROID_DEPLOYMENT_TREE_SHA256)" || fail "release state is missing its deployment-tree identity"
  [[ "${recorded_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
    fail "smoke state contains an invalid timestamp"
  [[ "${recorded_source}" =~ ^[0-9a-f]{40}$ ]] && [ "${recorded_source}" = "${current_source}" ] ||
    fail "smoke state does not match the current backend release"
  [[ "${recorded_tree}" =~ ^[0-9a-f]{64}$ ]] && [ "${recorded_tree}" = "${current_tree}" ] ||
    fail "smoke state does not match the current deployment tree"
  [ "$(state_value "${smoke_state}" NODE_SIA_RENTERD_MIN_SHARDS)" = 10 ] &&
    [ "$(state_value "${smoke_state}" NODE_SIA_RENTERD_TOTAL_SHARDS)" = 30 ] ||
    fail "smoke state does not record the approved 10-of-30 policy"
  echo "current renterd smoke-test evidence verified"
}

update_blob_store_kind() {
  local kind="$1"
  python3 - "${deploy_env}" "${kind}" <<'PY'
import os
import sys
import tempfile

path, value = sys.argv[1:]
if value not in ("local-disk", "sia-renterd"):
    raise SystemExit("invalid blob-store kind")
with open(path, "r", encoding="utf-8") as f:
    lines = f.readlines()
output = []
seen = False
for raw in lines:
    if raw.startswith("NODE_BLOB_STORE_KIND="):
        if not seen:
            output.append(f"NODE_BLOB_STORE_KIND={value}\n")
            seen = True
        continue
    output.append(raw if raw.endswith("\n") else raw + "\n")
if not seen:
    output.append(f"NODE_BLOB_STORE_KIND={value}\n")
directory = os.path.dirname(path)
fd, temporary = tempfile.mkstemp(prefix=".blob-store-kind.", dir=directory, text=True)
try:
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.writelines(output)
        f.flush()
        os.fsync(f.fileno())
    os.chown(temporary, 0, 0)
    os.replace(temporary, path)
    directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
}

if [ "${1}" = verify ]; then
  verify_smoke_state
  exit 0
fi

if [ "${1}" = enable ]; then
  /usr/local/sbin/virtroid-configure-renterd-secrets verify-activation >/dev/null
  verify_smoke_state >/dev/null
  /usr/local/sbin/virtroid-renterd-admin prepare-smoke >/dev/null
  [ "$(state_value "${deploy_env}" NODE_BLOB_STORE_KIND)" = local-disk ] ||
    fail "live blob storage is not in the required local-disk pre-cutover state"
  update_blob_store_kind sia-renterd
  if ! VIRTROID_PROFILES=edge,renterd "${deployment_dir}/deploy.sh" up; then
    echo "Sia cutover failed; restoring local-disk mode" >&2
    update_blob_store_kind local-disk
    VIRTROID_PROFILES=edge,renterd "${deployment_dir}/deploy.sh" up ||
      fail "Sia cutover and automatic local-disk rollback both failed"
    fail "Sia cutover failed and was rolled back to local-disk"
  fi
  if [ "$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' virtnoded | awk -F= '$1 == "NODE_BLOB_STORE_KIND" { found++; value=$2 } END { if (found != 1) exit 1; print value }')" != sia-renterd ]; then
    fail "virtnoded did not activate sia-renterd after the cutover"
  fi
  /usr/local/sbin/virtroid-renterd-admin prepare-smoke >/dev/null
  echo "Sia encrypted blob storage is enabled; local manifests will migrate only after verified remote generations"
  exit 0
fi

/usr/local/sbin/virtroid-configure-renterd-secrets verify-activation >/dev/null
/usr/local/sbin/virtroid-renterd-admin prepare-smoke >/dev/null
require_safe_state "${release_state}"
for container_name in virtroid-renterd-mysql virtroid-renterd; do
  [ "$(docker inspect --format '{{.State.Running}}' "${container_name}" 2>/dev/null)" = true ] ||
    fail "required container is not running: ${container_name}"
done
[ "$(docker inspect --format '{{.State.Health.Status}}' virtroid-renterd-mysql)" = healthy ] ||
  fail "renterd MySQL is not healthy"

compose=(
  docker compose
  --env-file "${deploy_env}"
  --env-file "${release_state}"
  -f "${deployment_dir}/docker-compose.yml"
  --profile renterd
)
"${compose[@]}" run --rm --no-deps \
  -e NODE_BLOB_PREFLIGHT=1 \
  -e NODE_BLOB_STORE_KIND=sia-renterd \
  -e NODE_RUNTIME_ROOT=/tmp/virtroid-blob-smoke \
  virtnoded
"${compose[@]}" run --rm --no-deps \
  -e NODE_BLOB_SMOKE_TEST=1 \
  -e NODE_BLOB_STORE_KIND=sia-renterd \
  -e NODE_RUNTIME_ROOT=/tmp/virtroid-blob-smoke \
  virtnoded

temporary="$(mktemp /var/lib/virtroid-deploy/.renterd-smoke.XXXXXX)"
trap 'find "${temporary:-}" -maxdepth 0 -delete 2>/dev/null || true' EXIT
printf '%s\n' \
  "RENTERD_SMOKE_VERIFIED_AT=$(date -u +%FT%TZ)" \
  "VIRTROID_SOURCE_SHA=$(state_value "${release_state}" VIRTROID_SOURCE_SHA)" \
  "VIRTROID_DEPLOYMENT_TREE_SHA256=$(state_value "${release_state}" VIRTROID_DEPLOYMENT_TREE_SHA256)" \
  'NODE_SIA_RENTERD_MIN_SHARDS=10' \
  'NODE_SIA_RENTERD_TOTAL_SHARDS=30' > "${temporary}"
chmod 0400 "${temporary}"
chown root:root "${temporary}"
sync -f "${temporary}"
mv -Tf "${temporary}" "${smoke_state}"
temporary=
sync -f /var/lib/virtroid-deploy
trap - EXIT
verify_smoke_state >/dev/null
echo "live renterd encrypted write/restore/delete smoke test passed and was recorded"
