#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
umask 0077

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(git -C "${script_dir}" rev-parse --show-toplevel)"
output_root="${1:-${repository_root}/dist/releases}"

usage() {
  cat >&2 <<'EOF'
Usage:
  sudo /opt/virtroid-source/deploy/vps/build-local-release.sh [output-directory]

Builds the exact linux/amd64 backend image on the isolated production VPS and
creates a checksummed release bundle. The offline source checkout must be clean,
committed, root-owned, and have no configured Git remote. Docker may fetch the
Dockerfile's digest-pinned base images when they are not cached locally.
EOF
}

if [ "${1:-}" = -h ] || [ "${1:-}" = --help ]; then
  usage
  exit 0
fi
if [ "$#" -gt 1 ]; then
  usage
  exit 64
fi

if [ "$(id -u)" -ne 0 ] || [ "$(uname -s)" != Linux ]; then
  echo "VPS-local release builds must run as root on Linux" >&2
  exit 1
fi

for command_name in docker find git grep id mkdir mktemp mv sed stat tar uname; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "missing VPS-local release command: ${command_name}" >&2
    exit 1
  }
done

[ ! -e "${repository_root}/.github" ] && [ ! -L "${repository_root}/.github" ] || {
  echo "GitHub automation must not exist in the production VPS source" >&2
  exit 1
}
[ -z "$(git -C "${repository_root}" remote)" ] || {
  echo "production VPS source repository must not have a Git remote" >&2
  exit 1
}
unsafe_path="$(find "${repository_root}" -xdev -mindepth 1 \
  \( -type l -o ! -type f ! -type d \) -print -quit)"
[ -z "${unsafe_path}" ] || {
  echo "production VPS source contains a symlink or special path: ${unsafe_path}" >&2
  exit 1
}
foreign_path="$(find "${repository_root}" -xdev -mindepth 0 ! -user root -print -quit)"
[ -z "${foreign_path}" ] || {
  echo "production VPS source is not entirely root-owned: ${foreign_path}" >&2
  exit 1
}
writable_path="$(find "${repository_root}" -xdev -mindepth 0 -perm /022 -print -quit)"
[ -z "${writable_path}" ] || {
  echo "production VPS source is group/world writable: ${writable_path}" >&2
  exit 1
}

if ! git -C "${repository_root}" diff --quiet --ignore-submodules -- ||
   ! git -C "${repository_root}" diff --cached --quiet --ignore-submodules -- ||
   [ -n "$(git -C "${repository_root}" ls-files --others --exclude-standard)" ]; then
  echo "release checkout must be clean and committed before building" >&2
  exit 1
fi

source_sha="$(git -C "${repository_root}" rev-parse --verify HEAD)"
[[ "${source_sha}" =~ ^[0-9a-f]{40}$ ]] || {
  echo "could not determine a full local source commit SHA" >&2
  exit 1
}
schema_version="$(sed -nE 's/^[[:space:]]*currentSchemaVersion[[:space:]]+int64[[:space:]]*=[[:space:]]*([0-9]+).*$/\1/p' \
  "${repository_root}/backend/internal/store/store.go")"
[[ "${schema_version}" =~ ^[0-9]+$ ]] && [ "${schema_version}" -gt 0 ] || {
  echo "could not determine the backend schema version" >&2
  exit 1
}
deployment_tree_digest="$("${script_dir}/deployment-tree-digest.sh" "${script_dir}")"
[[ "${deployment_tree_digest}" =~ ^[0-9a-f]{64}$ ]] || {
  echo "could not determine the deployment-tree digest" >&2
  exit 1
}

image_tag="virtroid-backend:release-${source_sha}"
build_date="$(git -C "${repository_root}" show -s --format=%cI "${source_sha}")"
mkdir -p "${output_root}"
output_root="$(cd "${output_root}" && pwd -P)"
[ "$(stat -c '%u' "${output_root}")" -eq 0 ] &&
  (( (8#$(stat -c '%a' "${output_root}") & 0022) == 0 )) || {
  echo "VPS release output directory must be root-owned and not group/world writable" >&2
  exit 1
}
final_dir="${output_root}/${source_sha}"
[ ! -e "${final_dir}" ] || {
  echo "release bundle already exists: ${final_dir}" >&2
  exit 1
}
temporary_dir="$(mktemp -d "${output_root}/.local-release.XXXXXX")"
cleanup() {
  if [ -d "${temporary_dir}" ]; then
    find "${temporary_dir}" -depth -delete
  fi
}
trap cleanup EXIT

docker_build_args=(build --platform linux/amd64)
if docker build --help 2>&1 | grep -q -- '--provenance'; then
  docker_build_args+=(--provenance=false)
fi
docker "${docker_build_args[@]}" \
  --build-arg "VCS_REF=${source_sha}" \
  --build-arg "BUILD_DATE=${build_date}" \
  --build-arg "SCHEMA_VERSION=${schema_version}" \
  --build-arg "DEPLOYMENT_TREE_DIGEST=${deployment_tree_digest}" \
  --tag "${image_tag}" \
  --file "${repository_root}/backend/Dockerfile" \
  "${repository_root}/backend"

local_engine_image_id="$(docker image inspect --format '{{.Id}}' "${image_tag}")"
architecture="$(docker image inspect --format '{{.Architecture}}' "${image_tag}")"
operating_system="$(docker image inspect --format '{{.Os}}' "${image_tag}")"
revision="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "${image_tag}")"
image_schema="$(docker image inspect --format '{{index .Config.Labels "com.virtroid.schema-version"}}' "${image_tag}")"
image_tree="$(docker image inspect --format '{{index .Config.Labels "com.virtroid.deployment-tree-sha256"}}' "${image_tag}")"
[[ "${local_engine_image_id}" =~ ^sha256:[0-9a-f]{64}$ ]] &&
  [ "${operating_system}/${architecture}" = linux/amd64 ] &&
  [ "${revision}" = "${source_sha}" ] &&
  [ "${image_schema}" = "${schema_version}" ] &&
  [ "${image_tree}" = "${deployment_tree_digest}" ] || {
  echo "built backend image identity or labels do not match the requested release" >&2
  exit 1
}

docker save --output "${temporary_dir}/backend-image.tar" "${image_tag}"
config_path="$(tar -xOf "${temporary_dir}/backend-image.tar" manifest.json |
  sed -nE 's/^\[{"Config":"([^"]+)".*$/\1/p')"
case "${config_path}" in
  blobs/sha256/[0-9a-f][0-9a-f]*)
    image_id="sha256:${config_path##*/}"
    ;;
  [0-9a-f][0-9a-f]*.json)
    image_id="sha256:${config_path%.json}"
    ;;
  *)
    echo "saved image does not expose a recognized config blob" >&2
    exit 1
    ;;
esac
[[ "${image_id}" =~ ^sha256:[0-9a-f]{64}$ ]] || {
  echo "saved image config ID is invalid" >&2
  exit 1
}
if tar -tf "${temporary_dir}/backend-image.tar" index.json >/dev/null 2>&1; then
  image_manifest_digest="$(tar -xOf "${temporary_dir}/backend-image.tar" index.json |
    sed -nE 's/^.*"digest":"(sha256:[0-9a-f]{64})".*$/\1/p')"
else
  # The classic Docker engine stores the config digest as its immutable image
  # identity and does not emit an OCI index. Record that same exact identity in
  # both fields so the bundle remains portable without inventing a digest.
  image_manifest_digest="${image_id}"
fi
[[ "${image_manifest_digest}" =~ ^sha256:[0-9a-f]{64}$ ]] &&
  { [ "${local_engine_image_id}" = "${image_id}" ] ||
    [ "${local_engine_image_id}" = "${image_manifest_digest}" ]; } || {
  echo "saved image does not expose one portable config ID and manifest digest" >&2
  exit 1
}
if command -v sha256sum >/dev/null 2>&1; then
  saved_config_digest="$(tar -xOf "${temporary_dir}/backend-image.tar" "${config_path}" | sha256sum | awk '{print $1}')"
else
  saved_config_digest="$(tar -xOf "${temporary_dir}/backend-image.tar" "${config_path}" | shasum -a 256 | awk '{print $1}')"
fi
[ "sha256:${saved_config_digest}" = "${image_id}" ] || {
  echo "saved image config blob does not match its declared ID" >&2
  exit 1
}
COPYFILE_DISABLE=1 tar --no-xattrs -C "${repository_root}/deploy" \
  --exclude='vps/.env' \
  --exclude='vps/release.env' \
  -czf "${temporary_dir}/deployment-tree.tar.gz" vps
printf '%s\n' \
  "VIRTROID_BACKEND_IMAGE=${image_tag}" \
  "VIRTROID_BACKEND_IMAGE_ID=${image_id}" \
  "VIRTROID_BACKEND_MANIFEST_DIGEST=${image_manifest_digest}" \
  "VIRTROID_SOURCE_SHA=${source_sha}" \
  "VIRTROID_SCHEMA_VERSION=${schema_version}" \
  "VIRTROID_DEPLOYMENT_TREE_SHA256=${deployment_tree_digest}" \
  > "${temporary_dir}/release.env"

if command -v sha256sum >/dev/null 2>&1; then
  (
    cd "${temporary_dir}"
    sha256sum backend-image.tar deployment-tree.tar.gz release.env > SHA256SUMS
  )
elif command -v shasum >/dev/null 2>&1; then
  (
    cd "${temporary_dir}"
    shasum -a 256 backend-image.tar deployment-tree.tar.gz release.env > SHA256SUMS
  )
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi
chmod 0600 "${temporary_dir}"/*
mv "${temporary_dir}" "${final_dir}"
temporary_dir=
trap - EXIT

printf 'VPS-local release bundle ready: %s\n' "${final_dir}"
printf 'source_sha=%s\nimage=%s\nimage_id=%s\nmanifest_digest=%s\nschema=%s\ndeployment_tree_sha256=%s\n' \
  "${source_sha}" "${image_tag}" "${image_id}" "${image_manifest_digest}" \
  "${schema_version}" "${deployment_tree_digest}"
