#!/usr/bin/env bash
# Builds the custom Android image on the build host: LineageOS 23.2 on
# ReDroid's container device, with ReDroid's Android 16 patches (four of them
# ported, third_party/redroid-patches-lineage) and our own (patches/),
# packaged as a container image.
# The plan is README.md, milestone 2.
#
#   deploy/aosp/build-lineage.sh STAGE... [VARIANT]
#
# Stages, in order: sync, patch, build, package (or all). VARIANT is user
# (default) or userdebug. Run it from a checkout (or a copy) of this
# repository on the build host; it works in $LINEAGE_ROOT (default
# /srv/lineage):
#
#   src/                  the LineageOS tree
#   redroid-patches/      ReDroid's patches, at the commit below
#   prebuilts/            the APKs device/virtroid/prebuilt.sha256 pins
#   keys/                 the private halves of our release keys
#                         (make-release-keys.sh); nothing else has them
#   ccache/               the compiler cache
#   android-ndk-r29/, x264/
#                         what build-x264.sh builds the screen encoder's
#                         x264 with (it needs nasm on the host)
#   out/<build id>/       each build's image and the inputs that made it:
#                         runtime-image.tar and runtime-image.json go into the
#                         VM image builder's assets directory, and
#                         x264-source.tar.gz is published with the VM image
#
# Every stage can be re-run. package needs sudo (it loop-mounts the images),
# Docker and skopeo.
set -Eeuo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${here}/../.." && pwd)"
root="${LINEAGE_ROOT:-/srv/lineage}"
src="${root}/src"
branch="lineage-23.2"
release="bp4a"
product="lineage_redroid_x86_64"
device="redroid_x86_64_only"
redroid_patches_url="https://github.com/remote-android/redroid-patches.git"
redroid_patches_commit="a5c70c0dd89c12b5027c902ab4182f31d38f3bb7"
ports="${repo_root}/third_party/redroid-patches-lineage/patches"
jobs="$(nproc)"

stages=()
variant="user"
for arg in "$@"; do
  case "${arg}" in
    sync|patch|build|package) stages+=("${arg}") ;;
    all) stages+=(sync patch build package) ;;
    user|userdebug) variant="${arg}" ;;
    *) echo "usage: $(basename "$0") sync|patch|build|package|all... [user|userdebug]" >&2; exit 2 ;;
  esac
done
[ "${#stages[@]}" -gt 0 ] || { echo "usage: $(basename "$0") sync|patch|build|package|all... [user|userdebug]" >&2; exit 2; }

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }

mounted=""
unmount() {
  [ -n "${mounted}" ] || return 0
  sudo umount "${mounted}/system" "${mounted}/vendor" 2>/dev/null || true
  mounted=""
}
trap unmount EXIT

stage_sync() {
  mkdir -p "${src}"
  cd "${src}"
  if [ ! -d .repo ]; then
    # Shallow: a build needs no history, and it halves the download.
    repo init -u https://github.com/LineageOS/android.git -b "${branch}" \
      --git-lfs --no-clone-bundle --depth=1 -g default,-darwin
  fi
  mkdir -p .repo/local_manifests
  cp "${here}"/local_manifests/*.xml .repo/local_manifests/
  log "sync ${branch}"
  repo sync -c -j"${jobs}" --jobs-checkout="${jobs}" --no-clone-bundle --no-tags \
    --optimized-fetch --force-sync --retry-fetches=3
}

# Every patch, as "project<TAB>file<TAB>kind": ReDroid's (upstream), with our
# port in place of any that no longer applies (port), then our own (ours).
patch_list() {
  local upstream="${root}/redroid-patches/android-16.0.0_r2"
  (cd "${upstream}" && find . -name '*.patch' | sed 's#^\./##' | sort) | while read -r rel; do
    if [ -f "${ports}/${rel}" ]; then
      printf '%s\t%s\tport\n' "$(dirname "${rel}")" "${ports}/${rel}"
    else
      printf '%s\t%s\tupstream\n' "$(dirname "${rel}")" "${upstream}/${rel}"
    fi
  done
  (cd "${here}/patches" && find . -name '*.patch' | sed 's#^\./##' | sort) | while read -r rel; do
    printf '%s\t%s\tours\n' "$(dirname "${rel}")" "${here}/patches/${rel}"
  done
}

stage_patch() {
  if [ ! -d "${root}/redroid-patches" ]; then
    git clone -q "${redroid_patches_url}" "${root}/redroid-patches"
  fi
  git -C "${root}/redroid-patches" fetch -q origin
  git -C "${root}/redroid-patches" checkout -q "${redroid_patches_commit}"

  local projects
  projects="$(patch_list | cut -f1 | sort -u)"
  cd "${src}"
  # Back to the manifest's revisions first, so a re-run applies each patch
  # once: every project, so one whose patches were dropped since loses them.
  for project in ${projects}; do
    git -C "${project}" am --abort >/dev/null 2>&1 || true
  done
  repo sync -l -d -q -j"${jobs}"
  local git=(git -c user.name="Virtroid build" -c user.email=build@virtroid.invalid)
  patch_list | while IFS=$'\t' read -r project patch kind; do
    local name
    name="$(basename "${patch}")"
    if [ "${kind}" = port ]; then
      # Our ports are plain diffs; commit each under the upstream patch's subject.
      local subject
      subject="$(sed -n 's/^Subject: \(\[PATCH[^]]*\] \)\{0,1\}//p' "${root}/redroid-patches/android-16.0.0_r2/${project}/${name}" | head -1)"
      if ! "${git[@]}" -C "${project}" apply --index --whitespace=nowarn "${patch}"; then
        git -C "${project}" reset -q --hard
        echo "patch failed: ${project}: ${name} (our port)" >&2
        exit 1
      fi
      "${git[@]}" -C "${project}" commit -q -m "${subject:-${name%.patch}}" -m "ReDroid's ${name}, ported to ${branch} by Virtroid (third_party/redroid-patches-lineage)."
    elif ! "${git[@]}" -C "${project}" am -q --keep-cr --whitespace=nowarn "${patch}"; then
      git -C "${project}" am --abort || true
      echo "patch failed: ${project}: ${name} (${kind})" >&2
      exit 1
    fi
    log "applied ${project}: ${name} (${kind})"
  done

  rm -rf device/virtroid
  cp -a "${here}/device/virtroid" device/virtroid
  place_prebuilts device/virtroid
  # x264 for the screen encoder (patches/frameworks/av 0004), next to the
  # Android.bp that imports it.
  "${here}/build-x264.sh" "${root}" device/virtroid/x264
  place_keys vendor/lineage-priv/keys
}

# Puts our release keys where LineageOS's configuration reads them: keys/'s
# public halves, keys.mk and Android.bp from here, each private half from
# $root/keys. Refuses a public half without its private one, or one that
# doesn't match it, so the build never signs with a key we don't hold.
place_keys() {
  local dest="$1"
  local private="${root}/keys"
  [ -d "${private}" ] || { echo "no ${private}: run make-release-keys.sh ${private} first" >&2; exit 1; }
  rm -rf "${dest}"
  mkdir -p "${dest}"
  cp "${here}/keys/keys.mk" "${here}/keys/Android.bp" "${dest}/"
  local cert name
  for cert in "${here}"/keys/*.x509.pem; do
    name="$(basename "${cert}" .x509.pem)"
    [ -f "${private}/${name}.pk8" ] || { echo "no private key ${private}/${name}.pk8" >&2; exit 1; }
    if [ "$(openssl x509 -in "${cert}" -noout -pubkey)" != "$(openssl pkey -inform DER -in "${private}/${name}.pk8" -pubout)" ]; then
      echo "${private}/${name}.pk8 doesn't match keys/${name}.x509.pem" >&2
      exit 1
    fi
    cp "${cert}" "${private}/${name}.pk8" "${dest}/"
  done
  local pub
  for pub in "${here}"/keys/*.avbpubkey; do
    name="$(basename "${pub}" .avbpubkey)"
    [ -f "${private}/${name}.pem" ] || { echo "no private key ${private}/${name}.pem" >&2; exit 1; }
    python3 external/avb/avbtool.py extract_public_key --key "${private}/${name}.pem" --output "${dest}/${name}.avbpubkey"
    if ! cmp -s "${pub}" "${dest}/${name}.avbpubkey"; then
      echo "${private}/${name}.pem doesn't match keys/${name}.avbpubkey" >&2
      exit 1
    fi
    cp "${private}/${name}.pem" "${dest}/"
  done
  chmod -R go-rwx "${dest}"
  log "release keys: $(ls "${dest}" | grep -c '\.pk8$') certificates, $(ls "${dest}" | grep -c '\.avbpubkey$') payload keys"
}

# Puts each APK prebuilt.sha256 pins into DEVICE/prebuilt: fetched once into
# $root/prebuilts (or put there by hand, for "local"), and refused unless its
# SHA-256 matches.
place_prebuilts() {
  local device_dir="$1"
  mkdir -p "${root}/prebuilts" "${device_dir}/prebuilt"
  grep -v '^\s*#' "${device_dir}/prebuilt.sha256" | while read -r sha name source; do
    [ -n "${sha}" ] || continue
    local cached="${root}/prebuilts/${name}"
    if [ ! -f "${cached}" ]; then
      if [ "${source}" = local ]; then
        echo "missing ${cached}: put ${name} there (see prebuilt.sha256)" >&2
        exit 1
      fi
      curl -sS -L --fail -o "${cached}.part" "${source}"
      mv "${cached}.part" "${cached}"
    fi
    if ! echo "${sha}  ${cached}" | sha256sum -c --quiet -; then
      echo "${cached} does not match prebuilt.sha256" >&2
      exit 1
    fi
    cp "${cached}" "${device_dir}/prebuilt/${name}"
    log "prebuilt ${name} (${sha:0:12})"
  done
}

# In a subshell: envsetup.sh sets variables of its own (src among them) that
# would otherwise clobber this script's.
stage_build() (
  cd "${src}"
  export USE_CCACHE=1 CCACHE_EXEC=/usr/bin/ccache CCACHE_DIR="${root}/ccache"
  # envsetup.sh and lunch expect neither -e nor -u.
  set +eu
  # shellcheck disable=SC1091
  source build/envsetup.sh
  lunch "${product}-${release}-${variant}" || exit 1
  # A neutral build user, host and number. They go into every build.prop
  # and fingerprint, which apps can read, and by default name the build
  # host and its user (ro.build.host, an eng.<user> incremental).
  export BUILD_USERNAME=android-build BUILD_HOSTNAME=localhost
  BUILD_NUMBER="$(date -u +%Y%m%d%H)"
  export BUILD_NUMBER
  log "build ${product}-${release}-${variant} with ${jobs} jobs"
  m -j"${jobs}" || exit 1
)

stage_package() {
  local product_out="${src}/out/target/product/${device}"
  if [ ! -f "${product_out}/system.img" ] || [ ! -f "${product_out}/vendor.img" ]; then
    echo "no images in ${product_out}: run the build stage first" >&2
    exit 1
  fi
  # Nothing signed with a key we don't hold goes out.
  log "check signatures"
  python3 -I "${here}/check-signatures.py" "${src}" "${product_out}" "${here}/keys" "${here}/device/virtroid/prebuilt.sha256"
  local id
  id="$(date -u +%Y%m%dT%H%M%SZ)-${variant}"
  local out="${root}/out/${id}"
  local mnt="${out}/mnt"
  mkdir -p "${out}" "${mnt}/system" "${mnt}/vendor"

  # The inputs: every project at its exact revision, ours and ReDroid's patches.
  (cd "${src}" && repo manifest -r -o "${out}/manifest-pinned.xml")
  cp -a "${src}/.repo/local_manifests" "${out}/local_manifests"
  patch_list | cut -f2 | while read -r patch; do sha256sum "${patch}"; done | sed "s#  ${root}/#  #; s#  ${repo_root}/#  #" >"${out}/patches.sha256"
  (cd "${repo_root}" && find deploy/aosp third_party/redroid-patches-lineage -type f -print0 | sort -z | xargs -0 sha256sum) >"${out}/virtroid-inputs.sha256"
  if git -C "${repo_root}" rev-parse -q --verify HEAD >"${out}/virtroid-commit" 2>/dev/null; then
    git -C "${repo_root}" status --porcelain -- deploy/aosp third_party/redroid-patches-lineage >"${out}/virtroid-uncommitted"
  else
    rm -f "${out}/virtroid-commit"
  fi
  # x264's licence: its source, and ours that links it, goes out with every
  # image that ships the screen encoder.
  "${here}/x264-source.sh" "${out}"

  # ReDroid's container image: system's files at the root, vendor in /vendor.
  mounted="${mnt}"
  sudo mount -o loop,ro "${product_out}/system.img" "${mnt}/system"
  sudo mount -o loop,ro "${product_out}/vendor.img" "${mnt}/vendor"
  local tag="${branch}-${id}"
  local ref="localhost/virtroid-runtime:${tag}"
  # Each entry by its plain name (system, not ./system), as in ReDroid's own
  # image: deploy/aleph/build-runtime-image.sh picks files out by name.
  local entries=()
  mapfile -t entries < <(sudo ls -A "${mnt}/system" | grep -vx vendor)
  # The label tells virtnoded the image boots in its unprivileged container
  # (backend/cmd/virtnoded/runtime_container.go); stock ReDroid needs
  # --privileged.
  sudo tar --xattrs -c -C "${mnt}" vendor -C "${mnt}/system" "${entries[@]}" |
    sudo docker import -c 'ENTRYPOINT ["/init", "androidboot.hardware=redroid"]' \
      -c 'LABEL io.virtroid.container-profile=unprivileged-1' - "${ref}" >/dev/null
  unmount
  rmdir "${mnt}/system" "${mnt}/vendor" "${mnt}"
  sudo docker save "${ref}" -o "${out}/saved.tar"
  sudo chown "$(id -u):$(id -g)" "${out}/saved.tar"
  # Uncompressed layers, as deploy/aleph/build-runtime-image.sh writes them:
  # the VM's root compresses them better, and each VM loads the image without
  # gunzipping it. This Docker saves them gzipped.
  skopeo --insecure-policy copy -q "docker-archive:${out}/saved.tar" "docker-archive:${out}/runtime-image.tar:${ref}"
  rm -f "${out}/saved.tar"
  local image_id
  image_id="$(tar -xOf "${out}/runtime-image.tar" manifest.json | python3 -I -c 'import json, sys; print(json.load(sys.stdin)[0]["Config"].removesuffix(".json"))')"
  # What deploy/aleph/nix/flake.nix reads beside runtime-image.tar.
  cat >"${out}/runtime-image.json" <<EOF
{
  "imageName": "localhost/virtroid-runtime",
  "imageId": "sha256:${image_id}",
  "tag": "${tag}",
  "hash": "",
  "androidRelease": "16",
  "base": "LineageOS ${branch}, ${product}-${release}-${variant}, built ${id}"
}
EOF
  sha256sum "${out}/runtime-image.tar" >"${out}/runtime-image.tar.sha256"
  log "packaged ${ref}: ${out}/runtime-image.tar ($(du -h "${out}/runtime-image.tar" | cut -f1))"
}

for stage in "${stages[@]}"; do
  log "stage ${stage}"
  "stage_${stage}"
done
