#!/bin/bash
# Builds the runtimes' Android image: ReDroid's stock Android 16 image plus
# Google's ARM translation (ndk_translation), taken from Google's own Android
# 16 x86_64 emulator image, so ARM-only apps such as SimpleX run. ReDroid's
# Android 16 image still advertises arm64-v8a but no longer carries the
# translation, and names a bridge (libnb.so) it does not ship.
#
#   sudo deploy/aleph/build-runtime-image.sh OUT_DIR [BASE_TAR]
#
# BASE_TAR, when given, is our own Android build (runtime-image.tar from
# deploy/aosp/build-lineage.sh), which takes the place of ReDroid's image;
# the translation goes into it the same way, from the emulator image of its
# own Android release (16 QPR2).
#
# Writes OUT_DIR/runtime-image.tar (for `docker load`, named
# localhost/virtroid-runtime:<tag>) and OUT_DIR/runtime-image.json; copy both
# into the VM image's assets directory. Needs root (loop mounts), Nix and
# python3. Both inputs are pinned, so a rebuild adds the same files.
set -Eeuo pipefail

# 16.0.0_64only-250627, linux/amd64 (security patch 2025-06-05).
redroid="redroid/redroid@sha256:18eaf7058f1fbe17378106daa9c02936a7d293e71325db432462086854d9e9f0"
# system-images;android-36;google_apis;x86_64, revision 7.
sysimg_url="https://dl.google.com/android/repository/sys-img/google_apis/x86_64-36_r07.zip"
sysimg_sha256="b1bb0769d0bed7698e61f203d7dc9bf6e7c37cd01a39d0d8788a11186bc78160"
tag="redroid-16.0.0-ndk"
release=16
base="${redroid}"

out="$(mkdir -p "${1:?usage: build-runtime-image.sh OUT_DIR [BASE_TAR]}" && cd "$1" && pwd)"
base_tar="${2:+$(realpath "$2")}"
work="${out}/work"
mkdir -p "${work}"

# Tools from a pinned nixpkgs (the one the VM image's Go comes from).
nixpkgs="github:NixOS/nixpkgs/02c087f3fe673c9e317f206a627cb6b910336dbe"
nixbuild() { nix --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths "$1" | head -1; }
crane="$(nixbuild "${nixpkgs}#crane")/bin/crane"
lpunpack="$(nixbuild "${nixpkgs}#android-tools")/bin/lpunpack"
skopeo="$(nixbuild "${nixpkgs}#skopeo^out")/bin/skopeo"

loop=""
registry=""
cleanup() {
  mountpoint -q "${work}/sys" && umount "${work}/sys"
  [ -z "${loop}" ] || losetup -d "${loop}" 2>/dev/null || true
  [ -z "${registry}" ] || kill "${registry}" 2>/dev/null || true
}
trap cleanup EXIT

if [ -n "${base_tar}" ]; then
  # crane reads its base from a registry, so serve the tarball from a local
  # one for the length of the build.
  rm -rf "${work}/registry"
  mkdir -p "${work}/registry"
  "${crane}" registry serve --address 127.0.0.1:0 --disk "${work}/registry" 2>"${work}/registry.log" &
  registry=$!
  port=""
  for _ in $(seq 50); do
    port="$(sed -n 's/.*serving on port \([0-9]*\).*/\1/p' "${work}/registry.log" | head -1)"
    [ -n "${port}" ] && break
    sleep 0.2
  done
  [ -n "${port}" ] || { echo "local registry did not start" >&2; cat "${work}/registry.log" >&2; exit 1; }
  redroid="$("${crane}" push "${base_tar}" "localhost:${port}/base" 2>/dev/null)"
  read -r base_ref base_id < <(tar -xOf "${base_tar}" manifest.json | python3 -I -c 'import json, sys
m = json.load(sys.stdin)[0]
print(m["RepoTags"][0], m["Config"].removesuffix(".json"))')
  tag="${base_ref##*:}-ndk"
  base="${base_ref} (image sha256:${base_id}, tarball sha256:$(sha256sum "${base_tar}" | cut -c1-64))"
  # The translator plugs into the system it runs on, so it has to come from
  # the same Android release: Android 16's crashes ARM apps on our QPR2 build.
  # system-images;android-36.1;google_apis;x86_64, revision 4.
  sysimg_url="https://dl.google.com/android/repository/sys-img/google_apis/x86_64-36.1_r04.zip"
  sysimg_sha256="d4e62da6f1ef0bf217657e51f42e48638a15355e6f4112762a6ceabfba6cef21"
fi

zip="${work}/$(basename "${sysimg_url}")"
[ -f "${zip}" ] || curl -sS -L --fail -o "${zip}" "${sysimg_url}"
echo "${sysimg_sha256}  ${zip}" | sha256sum -c --quiet -

# The emulator's system.img is a disk whose second partition is a dynamic
# "super" partition; only its system partition is needed, kept per image.
system_ext4="${work}/system-$(basename "${zip}" .zip).ext4"
if [ ! -f "${system_ext4}" ]; then
  python3 -I -c 'import sys, zipfile, shutil
with zipfile.ZipFile(sys.argv[1]).open("x86_64/system.img") as s, open(sys.argv[2], "wb") as d:
    shutil.copyfileobj(s, d, 1 << 20)' "${zip}" "${work}/disk.img"
  loop="$(losetup -f --show -P -r "${work}/disk.img")"
  "${lpunpack}" --partition=system "${loop}p2" "${work}" >/dev/null
  losetup -d "${loop}"
  loop=""
  rm -f "${work}/disk.img"
  mv "${work}/system.img" "${system_ext4}"
fi

mkdir -p "${work}/sys"
mount -o loop,ro "${system_ext4}" "${work}/sys"

layer="${work}/layer"
rm -rf "${layer}"
mkdir -p "${layer}"
# The layer's directories replace the base image's, so take their owner and
# mode from it, along with the build.prop changed below.
"${crane}" export --platform linux/amd64 "${redroid}" - |
  tar -x --no-recursion -C "${layer}" system system/bin system/lib64 system/etc system/etc/init vendor vendor/build.prop
mkdir -p "${layer}/system/etc/binfmt_misc"
src="${work}/sys/system"
# The translator is Berberis: it also maps its code region from
# libberberis_exec_region.so, and aborts at an ARM app's start without it.
cp -a "${src}"/lib64/libndk_translation*.so "${src}/lib64/libberberis_exec_region.so" "${src}/lib64/arm64" "${layer}/system/lib64/"
cp -a "${src}/bin/arm64" "${src}/bin/ndk_translation_program_runner_binfmt_misc_arm64" "${layer}/system/bin/"
cp -a "${src}/etc/binfmt_misc/arm64_exe" "${src}/etc/binfmt_misc/arm64_dyn" "${layer}/system/etc/binfmt_misc/"
cp -a "${src}/etc/ld.config.arm64.txt" "${layer}/system/etc/"
# Android 16's translator reads etc/cpuinfo.arm64.txt; QPR2's keeps it in
# etc/berberis.
if [ -d "${src}/etc/berberis" ]; then
  cp -a "${src}/etc/berberis" "${layer}/system/etc/"
else
  cp -a "${src}/etc/cpuinfo.arm64.txt" "${layer}/system/etc/"
fi
cp -a "${src}/etc/init/ndk_translation.rc" "${layer}/system/etc/init/"
grep -E '^ro\.berberis\.' "${src}/build.prop" >"${work}/berberis.prop" || true
umount "${work}/sys"

# The image already sets ro.dalvik.vm.isa.arm64 and ro.enable.native.bridge.exec;
# point its native bridge at the translation, as Google's image does.
grep -qx 'ro.dalvik.vm.native.bridge=libnb.so' "${layer}/vendor/build.prop"
sed -i 's/^ro\.dalvik\.vm\.native\.bridge=libnb\.so$/ro.dalvik.vm.native.bridge=libndk_translation.so/' "${layer}/vendor/build.prop"
# Our own builds also run the translator with the settings Google's emulator
# image gives it (ro.berberis.flags, for one); ReDroid's image is left as it
# was approved.
if [ -n "${base_tar}" ]; then
  cat "${work}/berberis.prop" >>"${layer}/vendor/build.prop"
fi

tar --sort=name --format=gnu --numeric-owner --mtime=@0 -C "${layer}" -cf "${work}/layer.tar" system vendor
"${crane}" append --platform linux/amd64 -b "${redroid}" -f "${work}/layer.tar" \
  -t "localhost/virtroid-runtime:${tag}" -o "${work}/appended.tar"
# Uncompressed layers, as `docker save` writes them: the VM's root compresses
# them far better and faster than gzipped ones, and each VM loads the image
# without gunzipping it at boot.
rm -f "${out}/runtime-image.tar"
"${skopeo}" --insecure-policy copy -q "docker-archive:${work}/appended.tar" \
  "docker-archive:${out}/runtime-image.tar:localhost/virtroid-runtime:${tag}"
image_id="$(tar -xOf "${out}/runtime-image.tar" manifest.json | python3 -I -c 'import json, sys; print(json.load(sys.stdin)[0]["Config"].removesuffix(".json"))')"

cat > "${out}/runtime-image.json" <<EOF
{
  "imageName": "localhost/virtroid-runtime",
  "imageId": "sha256:${image_id}",
  "tag": "${tag}",
  "hash": "",
  "androidRelease": "${release}",
  "base": "${base}",
  "translationFrom": "${sysimg_url}"
}
EOF
echo "built ${out}/runtime-image.tar ($(du -h "${out}/runtime-image.tar" | cut -f1)); layer sha256 $(sha256sum "${work}/layer.tar" | cut -c1-64)"
cat "${out}/runtime-image.json"
