#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
umask 0077

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(git -C "${script_dir}" rev-parse --show-toplevel)"

usage() {
  cat >&2 <<'EOF'
Usage:
  sudo bash /opt/virtroid-source/deploy/vps/build-provisioner-image.sh

Builds the optional runtime VM provisioner image (virtroid-provisioner plus
Aleph's aleph CLI at a pinned commit) from the clean, root-owned offline
source checkout, and prints the two .env lines that select it:
  PROVISIONER_IMAGE=virtroid-provisioner:release-<source sha>
  PROVISIONER_IMAGE_ID=sha256:<image id>
Docker fetches the Dockerfile's digest-pinned base images and the pinned
aleph-rs commit when they are not cached locally.
EOF
}

if [ "${1:-}" = -h ] || [ "${1:-}" = --help ]; then
  usage
  exit 0
fi
if [ "$#" -ne 0 ]; then
  usage
  exit 64
fi
if [ "$(id -u)" -ne 0 ] || [ "$(uname -s)" != Linux ]; then
  echo "the provisioner image must be built as root on the Linux VPS" >&2
  exit 1
fi

[ -z "$(git -C "${repository_root}" remote)" ] || {
  echo "production VPS source repository must not have a Git remote" >&2
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
  echo "source checkout must be clean and committed before building" >&2
  exit 1
fi

source_sha="$(git -C "${repository_root}" rev-parse --verify HEAD)"
[[ "${source_sha}" =~ ^[0-9a-f]{40}$ ]] || {
  echo "could not determine a full local source commit SHA" >&2
  exit 1
}
image_tag="virtroid-provisioner:release-${source_sha}"

docker_build_args=(build --platform linux/amd64)
if docker build --help 2>&1 | grep -q -- '--provenance'; then
  docker_build_args+=(--provenance=false)
fi
docker "${docker_build_args[@]}" \
  --build-arg "VCS_REF=${source_sha}" \
  --tag "${image_tag}" \
  --file "${repository_root}/deploy/provisioner/Dockerfile" \
  "${repository_root}"

image_id="$(docker image inspect --format '{{.Id}}' "${image_tag}")"
revision="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "${image_tag}")"
platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "${image_tag}")"
[[ "${image_id}" =~ ^sha256:[0-9a-f]{64}$ ]] &&
  [ "${revision}" = "${source_sha}" ] &&
  [ "${platform}" = linux/amd64 ] || {
  echo "built provisioner image identity or labels do not match the source" >&2
  exit 1
}

printf '%s\n' "PROVISIONER_IMAGE=${image_tag}" "PROVISIONER_IMAGE_ID=${image_id}"
