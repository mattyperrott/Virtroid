#!/usr/bin/env bash
# Builds x264 as a static library for the image's screen encoder
# (c2.virtroid.x264.encoder in frameworks/av, patches/frameworks/av 0004),
# with the Android NDK and x264's own build system, because its x86
# assembly needs NASM, which the platform build doesn't have.
#
#   deploy/aosp/build-x264.sh ROOT DEST
#
# ROOT is build-lineage.sh's $LINEAGE_ROOT: the NDK and x264's source are
# kept there between builds. DEST (device/virtroid/x264 in the tree) gets
# lib/libx264.a and include/, next to the Android.bp that imports them.
# Both downloads are pinned: the NDK by SHA-256, x264 by commit.
# Needs nasm 2.13 or later on the build host.
set -Eeuo pipefail

root="$1"
mkdir -p "$2"
dest="$(cd "$2" && pwd)"
ndk_version="r29"
ndk_sha256="4abbbcdc842f3d4879206e9695d52709603e52dd68d3c1fff04b3b5e7a308ecf"
x264_url="https://code.videolan.org/videolan/x264.git"
x264_commit="b35605ace3ddf7c1a5d67a2eb553f034aef41d55" # stable, 2025-06-08
# The swcodec APEX's min_sdk_version.
api=29

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }

ndk="${root}/android-ndk-${ndk_version}"
if [ ! -d "${ndk}" ]; then
  zip="${root}/android-ndk-${ndk_version}-linux.zip"
  curl -sS -L --fail -o "${zip}.part" "https://dl.google.com/android/repository/android-ndk-${ndk_version}-linux.zip"
  echo "${ndk_sha256}  ${zip}.part" | sha256sum -c --quiet -
  unzip -q -d "${root}" "${zip}.part" "android-ndk-${ndk_version}/toolchains/llvm/*" "android-ndk-${ndk_version}/source.properties"
  rm "${zip}.part"
fi
toolchain="${ndk}/toolchains/llvm/prebuilt/linux-x86_64"

src="${root}/x264"
if [ ! -d "${src}/.git" ]; then
  git init -q "${src}"
  git -C "${src}" remote add origin "${x264_url}"
fi
if [ "$(git -C "${src}" rev-parse -q --verify HEAD || true)" != "${x264_commit}" ]; then
  git -C "${src}" fetch -q --depth 1 origin "${x264_commit}"
  git -C "${src}" checkout -q --detach "${x264_commit}"
fi

build="$(mktemp -d)"
trap 'rm -rf "${build}"' EXIT
cd "${build}"
# Only what the encoder component calls: 8-bit 4:2:0 encoding on the
# caller's thread (it never asks for x264's own threads), no CLI.
CC="${toolchain}/bin/x86_64-linux-android${api}-clang" \
AR="${toolchain}/bin/llvm-ar" RANLIB="${toolchain}/bin/llvm-ranlib" STRIP="${toolchain}/bin/llvm-strip" \
  "${src}/configure" --host=x86_64-linux-android --enable-static --enable-pic \
  --disable-cli --disable-opencl --disable-thread --disable-avs --disable-swscale \
  --disable-lavf --disable-ffms --disable-gpac --disable-lsmash \
  --bit-depth=8 --chroma-format=420 >/dev/null
make -s -j"$(nproc)" lib-static

rm -rf "${dest}/lib" "${dest}/include"
mkdir -p "${dest}/lib" "${dest}/include"
cp libx264.a "${dest}/lib/"
cp "${src}/x264.h" x264_config.h "${dest}/include/"
cp "${src}/COPYING" "${dest}/COPYING"
log "x264 ${x264_commit:0:7} for android-${api} ($(grep -m1 X264_POINTVER x264_config.h | cut -d'"' -f2))"
