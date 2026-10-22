#!/bin/bash
# Builds the runtimes' Android image: ReDroid's stock Android 16 image plus
# Google's ARM translation (ndk_translation), taken from Google's own Android
# 16 x86_64 emulator image, so ARM-only apps such as SimpleX run. ReDroid's
# Android 16 image still advertises arm64-v8a but no longer carries the
# translation, and names a bridge (libnb.so) it does not ship.
#
#   sudo deploy/aleph/build-runtime-image.sh OUT_DIR
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

out="$(mkdir -p "${1:?usage: build-runtime-image.sh OUT_DIR}" && cd "$1" && pwd)"
work="${out}/work"
mkdir -p "${work}"

# Tools from a pinned nixpkgs (the one the VM image's Go comes from).
nixpkgs="github:NixOS/nixpkgs/02c087f3fe673c9e317f206a627cb6b910336dbe"
nixbuild() { nix --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths "$1" | head -1; }
crane="$(nixbuild "${nixpkgs}#crane")/bin/crane"
lpunpack="$(nixbuild "${nixpkgs}#android-tools")/bin/lpunpack"
skopeo="$(nixbuild "${nixpkgs}#skopeo^out")/bin/skopeo"

loop=""
cleanup() {
  mountpoint -q "${work}/sys" && umount "${work}/sys"
  [ -z "${loop}" ] || losetup -d "${loop}" 2>/dev/null || true
}
trap cleanup EXIT

zip="${work}/$(basename "${sysimg_url}")"
[ -f "${zip}" ] || curl -sS -L --fail -o "${zip}" "${sysimg_url}"
echo "${sysimg_sha256}  ${zip}" | sha256sum -c --quiet -

# The emulator's system.img is a disk whose second partition is a dynamic
# "super" partition; only its system partition is needed.
if [ ! -f "${work}/system.ext4" ]; then
  python3 -I -c 'import sys, zipfile, shutil
with zipfile.ZipFile(sys.argv[1]).open("x86_64/system.img") as s, open(sys.argv[2], "wb") as d:
    shutil.copyfileobj(s, d, 1 << 20)' "${zip}" "${work}/disk.img"
  loop="$(losetup -f --show -P -r "${work}/disk.img")"
  "${lpunpack}" --partition=system "${loop}p2" "${work}" >/dev/null
  losetup -d "${loop}"
  loop=""
  rm -f "${work}/disk.img"
  mv "${work}/system.img" "${work}/system.ext4"
fi

mkdir -p "${work}/sys"
mount -o loop,ro "${work}/system.ext4" "${work}/sys"

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
cp -a "${src}/etc/cpuinfo.arm64.txt" "${src}/etc/ld.config.arm64.txt" "${layer}/system/etc/"
cp -a "${src}/etc/init/ndk_translation.rc" "${layer}/system/etc/init/"
umount "${work}/sys"

# The image already sets ro.dalvik.vm.isa.arm64 and ro.enable.native.bridge.exec;
# point its native bridge at the translation, as Google's image does.
grep -qx 'ro.dalvik.vm.native.bridge=libnb.so' "${layer}/vendor/build.prop"
sed -i 's/^ro\.dalvik\.vm\.native\.bridge=libnb\.so$/ro.dalvik.vm.native.bridge=libndk_translation.so/' "${layer}/vendor/build.prop"

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
  "base": "${redroid}",
  "translationFrom": "${sysimg_url}"
}
EOF
echo "built ${out}/runtime-image.tar ($(du -h "${out}/runtime-image.tar" | cut -f1)); layer sha256 $(sha256sum "${work}/layer.tar" | cut -c1-64)"
cat "${out}/runtime-image.json"
