#!/usr/bin/env bash
# Puts the source of the image's copyleft app prebuilts beside a build: each
# tarball device/virtroid/prebuilt-sources.sha256 pins, fetched once into
# $LINEAGE_ROOT/prebuilt-sources and refused unless its SHA-256 matches,
# copied unchanged (so a tarball that doesn't change between images is the
# same file, and the same Aleph object, every time), with a SOURCE.md that
# says which app each is.
#
#   deploy/aosp/app-sources.sh OUT
#
# OUT is a build's $LINEAGE_ROOT/out/<build id> directory; build-lineage.sh's
# package stage calls this. Writes OUT/app-sources/.
set -Eeuo pipefail

out="$(cd "$1" && pwd)"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="${LINEAGE_ROOT:-/srv/lineage}"
pins="${here}/device/virtroid/prebuilt-sources.sha256"
cache="${root}/prebuilt-sources"
dest="${out}/app-sources"

mkdir -p "${cache}"
rm -rf "${dest}"
mkdir -p "${dest}"
{
  echo "# Source of the apps built into Virtroid's Android build $(basename "${out}")"
  echo
  echo "Each tarball is the source an app's APK was built from: F-Droid's, as"
  echo "F-Droid keeps it, or (\"local\") Virtroid's own, with a SOURCE.md inside."
  echo "SHA256SUMS lists them. The apps are free software under the licences"
  echo "their source names (deploy/aosp/device/virtroid/prebuilt-sources.sha256"
  echo "in https://github.com/mattyperrott/Virtroid says which is which)."
  echo
} >"${dest}/SOURCE.md"

grep -v '^\s*#' "${pins}" | while read -r sha name source; do
  [ -n "${sha}" ] || continue
  cached="${cache}/${name}"
  if [ ! -f "${cached}" ]; then
    if [ "${source}" = local ]; then
      echo "missing ${cached}: put ${name} there (see prebuilt-sources.sha256)" >&2
      exit 1
    fi
    curl -sS -L --fail -o "${cached}.part" "${source}"
    mv "${cached}.part" "${cached}"
  fi
  if ! echo "${sha}  ${cached}" | sha256sum -c --quiet -; then
    echo "${cached} does not match prebuilt-sources.sha256" >&2
    exit 1
  fi
  cp "${cached}" "${dest}/${name}"
  echo "- ${name}: ${source}" >>"${dest}/SOURCE.md"
done
(cd "${dest}" && sha256sum -- *.tar.gz >SHA256SUMS)
echo "app-sources: $(grep -c . "${dest}/SHA256SUMS") tarballs in ${dest}"
