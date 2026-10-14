#!/bin/bash
# Nightly copy of the two databases a lost server could not do without
# (indexd.md, "Backups"): the control plane's, and indexd's, which is the only
# map from a runtime's snapshot to its shards on Sia.
#
# Each copy is sealed with a key derived from the indexd wallet's recovery
# phrase, which the operator keeps offline, checked by opening it again, kept
# here (the newest three), and stored on Aleph by the provisioner's account
# (the newest fourteen). Restoring needs only the phrase and a copy.
#
#   usage: virtroid-db-backup
set -euo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly env_file=/opt/virtroid/deploy/vps/.env
readonly phrase_file=/etc/virtroid/secrets/indexd.yml
readonly aleph_password_file=/etc/virtroid/secrets/aleph-password
readonly state_dir=/var/lib/virtroid-db-backup
readonly maintenance_lock=/var/lib/virtroid-deploy/maintenance.lock
readonly keep_here=3
readonly keep_on_aleph=14

die() {
  echo "virtroid-db-backup: $*" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || die "run as root"

# One setting from .env, without running the file.
setting() {
  local value
  value="$(sed -n "s/^$1=//p" "${env_file}" | tail -n 1)"
  value="${value%\"}"
  value="${value#\"}"
  printf '%s' "${value:-$2}"
}

# The images the running release uses, which carry the tools this needs.
running_image() {
  docker inspect --format '{{.Config.Image}}' "$1" 2>/dev/null || true
}
backend_image="$(running_image virtroidd)"
provisioner_image="$(running_image virtroid-provisioner)"
provisioner_image="${provisioner_image:-$(setting PROVISIONER_IMAGE "")}"
aleph_account="$(setting ALEPH_ACCOUNT virtroid-provisioner)"
aleph_owner="$(setting ALEPH_OWNER_ADDRESS "")"
postgres_user="$(setting POSTGRES_USER virtroid)"
postgres_db="$(setting POSTGRES_DB virtroid)"
[ -n "${backend_image}" ] || die "virtroidd is not running, so the release's image is unknown"
[ -r "${phrase_file}" ] || die "the recovery phrase (${phrase_file}) is missing; backups are sealed with it"

install -d -o root -g root -m 0700 "${state_dir}"
# Releases restart these databases; wait for one in progress.
exec 8>"${maintenance_lock}"
flock -w 3600 8 || die "a release held the maintenance lock for an hour"

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
work="$(mktemp -d "${state_dir}/.work-XXXXXX")"
trap 'rm -rf "${work}"' EXIT

dump() {
  local container="$1" user="$2" database="$3" out="$4"
  docker exec "${container}" pg_dump -U "${user}" -d "${database}" -Fc > "${out}"
  [ -s "${out}" ] || die "${container} produced an empty dump"
  # A dump pg_restore cannot list would not restore either.
  docker exec -i "${container}" pg_restore --list < "${out}" > /dev/null ||
    die "${container}'s dump does not read back"
}

dump virtroid-postgres "${postgres_user}" "${postgres_db}" "${work}/control-plane.dump"
included="control-plane.dump"
if [ "$(docker inspect -f '{{.State.Running}}' virtroid-indexd-postgres 2>/dev/null)" = true ]; then
  dump virtroid-indexd-postgres indexd indexd "${work}/indexd.dump"
  included="${included} indexd.dump"
fi
{
  echo "created=${stamp}"
  echo "source=$(docker inspect --format '{{index .Config.Labels "com.virtroid.source-sha"}}' virtroidd 2>/dev/null || echo unknown)"
  echo "files=${included}"
} > "${work}/manifest.txt"
# shellcheck disable=SC2086
tar -C "${work}" -cf "${work}/bundle.tar" manifest.txt ${included}

seal() {
  docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
    --user 0:0 --entrypoint virtroid-admin \
    -v "${work}:/work" -v "${phrase_file}:/run/phrase:ro" \
    "${backend_image}" "$@" --phrase-file /run/phrase
}
seal backup-seal --in /work/bundle.tar --out /work/bundle.vbak
# Check the copy before keeping it: it must open to the same bundle.
seal backup-open --in /work/bundle.vbak --out /work/reopened.tar
cmp -s "${work}/bundle.tar" "${work}/reopened.tar" || die "the sealed copy does not open to the same bundle"

copy="${state_dir}/virtroid-db-${stamp}.vbak"
mv "${work}/bundle.vbak" "${copy}"
echo "virtroid-db-backup: sealed ${copy} ($(stat -c %s "${copy}") bytes: ${included})"
ls -1t "${state_dir}"/virtroid-db-*.vbak | tail -n +$((keep_here + 1)) | xargs -r rm -f --

if [ -z "${provisioner_image}" ] || ! docker volume inspect virtroid_provisioner-state > /dev/null 2>&1; then
  die "the provisioner is not set up, so this copy stays on this server only"
fi
# The provisioner's user uploads, so it gets its own copy of the sealed file.
upload_dir="${work}/upload"
install -d -o 10071 -g 10071 -m 0700 "${upload_dir}"
install -o 10071 -g 10071 -m 0400 "${copy}" "${upload_dir}/"
aleph() {
  docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges --user 10071:10071 \
    -e HOME=/var/lib/virtroid-provisioner -e ALEPH_CONFIG_HOME=/var/lib/virtroid-provisioner/aleph \
    -v virtroid_provisioner-state:/var/lib/virtroid-provisioner \
    -v "${aleph_password_file}:/run/secrets/aleph-password:ro" \
    -v "${upload_dir}:/copies:ro" \
    --entrypoint /bin/sh "${provisioner_image}" -c \
    'ALEPH_PASSWORD="$(cat /run/secrets/aleph-password)" exec aleph --json "$@"' aleph "$@" \
    --account "${aleph_account}" ${aleph_owner:+--on-behalf-of "${aleph_owner}"}
}
uploaded="$(aleph file upload "/copies/$(basename "${copy}")" --payment-type credit)"
item_hash="$(grep -o '"item_hash"[[:space:]]*:[[:space:]]*"[0-9a-f]\{64\}"' <<< "${uploaded}" | head -n 1 | grep -o '[0-9a-f]\{64\}')" ||
  die "Aleph did not take the copy: ${uploaded}"
echo "${stamp} ${item_hash}" >> "${state_dir}/aleph-copies"
echo "virtroid-db-backup: stored on Aleph as ${item_hash}"

# Forget the oldest copies on Aleph beyond the newest ${keep_on_aleph}.
total="$(wc -l < "${state_dir}/aleph-copies")"
if [ "${total}" -gt "${keep_on_aleph}" ]; then
  head -n $((total - keep_on_aleph)) "${state_dir}/aleph-copies" | while read -r _ old_hash; do
    if aleph message forget "${old_hash}" --yes --reason "Virtroid database copy superseded" > /dev/null; then
      sed -i "/ ${old_hash}\$/d" "${state_dir}/aleph-copies"
    else
      echo "virtroid-db-backup: could not forget ${old_hash} yet" >&2
    fi
  done
fi
