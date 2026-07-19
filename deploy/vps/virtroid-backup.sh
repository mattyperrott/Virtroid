#!/usr/bin/env bash
set -euo pipefail

backup_root=/var/backups/virtroid
retention_days="${VIRTROID_BACKUP_RETENTION_DAYS:-7}"
if [[ ! "${retention_days}" =~ ^[0-9]+$ ]] ||
  [ "${retention_days}" -lt 1 ] || [ "${retention_days}" -gt 365 ]; then
  echo "VIRTROID_BACKUP_RETENTION_DAYS must be between 1 and 365" >&2
  exit 1
fi

umask 0077
install -d -o root -g root -m 0700 "${backup_root}"
exec 9>"${backup_root}/.backup.lock"
if ! flock -n 9; then
  echo "another Virtroid backup is already running" >&2
  exit 1
fi

run_id="$(date -u +%Y%m%dT%H%M%SZ)"
partial_dir="$(mktemp -d "${backup_root}/.partial-${run_id}.XXXXXX")"
final_dir="${backup_root}/daily-${run_id}"
cleanup() {
  if [ -d "${partial_dir}" ]; then
    find "${partial_dir}" -depth -delete
  fi
}
trap cleanup EXIT

if [ -e "${final_dir}" ]; then
  echo "backup destination already exists: ${final_dir}" >&2
  exit 1
fi
if [ ! -r /opt/virtroid/deploy/vps/.env ]; then
  echo "deployment environment is not readable" >&2
  exit 1
fi
if [ "$(docker inspect -f '{{.State.Running}}' virtroid-postgres 2>/dev/null)" != true ]; then
  echo "virtroid-postgres is not running" >&2
  exit 1
fi

source_bytes="$(du -sb /srv/virtroid | awk '{print $1}')"
available_bytes="$(df -B1 --output=avail "${backup_root}" | tail -n 1 | tr -d ' ')"
minimum_free=$((source_bytes * 2 + 1073741824))
if [ "${available_bytes}" -lt "${minimum_free}" ]; then
  echo "insufficient free space for a safe Virtroid backup" >&2
  exit 1
fi

docker exec virtroid-postgres pg_dump -U virtroid -d virtroid --format=custom > "${partial_dir}/virtroid-postgres.dump"
docker exec -i virtroid-postgres pg_restore --list < "${partial_dir}/virtroid-postgres.dump" >/dev/null
tar --xattrs --acls -C /srv -czf "${partial_dir}/srv-virtroid.tgz" virtroid
tar -tzf "${partial_dir}/srv-virtroid.tgz" >/dev/null
install -m 0600 /opt/virtroid/deploy/vps/.env "${partial_dir}/deploy.env"
{
  date -u +%FT%TZ
  docker inspect --format '{{.Name}} {{.Image}} restart={{.RestartCount}}' \
    virtroid-postgres virtroidd virtnoded virtroid-edge
  docker exec virtroid-postgres psql -U virtroid -d virtroid -Atc \
    'SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1;'
} > "${partial_dir}/metadata.txt"

(
  cd "${partial_dir}"
  sha256sum \
    deploy.env \
    metadata.txt \
    srv-virtroid.tgz \
    virtroid-postgres.dump > SHA256SUMS
  sha256sum --check SHA256SUMS >/dev/null
)
sync -f "${partial_dir}"
mv "${partial_dir}" "${final_dir}"
trap - EXIT
sync -f "${backup_root}"

mapfile -t completed_backups < <(
  find "${backup_root}" -mindepth 1 -maxdepth 1 -type d \
    -name 'daily-????????T??????Z' -printf '%T@ %p\n' | sort -nr | cut -d' ' -f2-
)
for ((index = retention_days; index < ${#completed_backups[@]}; index++)); do
  find "${completed_backups[index]}" -depth -delete
done

printf 'Virtroid backup completed: %s\n' "${final_dir}"
