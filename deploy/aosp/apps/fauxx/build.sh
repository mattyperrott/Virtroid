#!/usr/bin/env bash
# Builds the Fauxx that Virtroid's Android image carries (README.md): Fauxx at
# the upstream commit below with this directory's patches, built the way
# Fauxx's own F-Droid recipe builds it (assembleFullRelease), signed with
# Virtroid's release key, and its complete source as a tarball.
#
#   deploy/aosp/apps/fauxx/build.sh OUT_DIR
#
# Writes OUT_DIR/Fauxx.apk and OUT_DIR/fauxx-source-<version>.tar.gz; their
# SHA-256s go in device/virtroid/prebuilt.sha256 and prebuilt-sources.sha256.
# Needs git, JDK 21 (JAVA_HOME), the Android SDK (ANDROID_HOME) with platform
# 37 and build-tools 36.0.0, and the release keystore's password in the macOS
# keychain, where android-client/signing/build-signed-release.sh finds it.
set -Eeuo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "${here}/../../../.." && pwd)"
upstream=https://github.com/digital-grease/fauxx.git
commit=1598ac1e5813d06079da4f65ab6392970c4e3f43 # tag v0.9.3
version=0.9.3-virtroid.3

out="$(mkdir -p "$1" && cd "$1" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

git init -q "${work}/src"
git -C "${work}/src" fetch -q --depth 1 "${upstream}" "${commit}"
git -C "${work}/src" checkout -q FETCH_HEAD
[ "$(git -C "${work}/src" rev-parse HEAD)" = "${commit}" ]
# The committer and dates come from the patches, so the patched commit, and
# the source tarball made from it, are the same on every run.
GIT_COMMITTER_NAME=Virtroid GIT_COMMITTER_EMAIL=build@virtroid.invalid \
  git -C "${work}/src" -c core.autocrlf=false am -q --committer-date-is-author-date "${here}"/patches/*.patch
grep -qx "versionName=${version}" "${work}/src/app/version.properties"

git -C "${work}/src" archive --format=tar.gz --prefix="fauxx-${version}/" \
  -o "${out}/fauxx-source-${version}.tar.gz" HEAD

(cd "${work}/src" && ./gradlew --no-daemon -q assembleFullRelease)
unsigned="${work}/src/app/build/outputs/apk/full/release/app-full-release-unsigned.apk"

build_tools="${ANDROID_HOME}/build-tools/36.0.0"
"${build_tools}/zipalign" -c -P 16 4 "${unsigned}"
password="$(security find-generic-password -a "${USER}" -s "Virtroid Android Release Keystore" -w)"
# v2 and v3 only, like android-client's own release signing.
VIRTROID_KS_PASS="${password}" "${build_tools}/apksigner" sign \
  --ks "${repo}/android-client/signing/virtroid-release.jks" --ks-key-alias virtroid-release \
  --ks-pass env:VIRTROID_KS_PASS --key-pass env:VIRTROID_KS_PASS \
  --v1-signing-enabled false --v2-signing-enabled true --v3-signing-enabled true \
  --out "${out}/Fauxx.apk" "${unsigned}"
rm -f "${out}/Fauxx.apk.idsig"

signer="$("${build_tools}/apksigner" verify --print-certs "${out}/Fauxx.apk" | sed -n 's/^Signer #1 certificate SHA-256 digest: //p')"
expected="$(sed -n 's/^Certificate SHA-256: //p' "${repo}/android-client/signing/RELEASE_KEY_FINGERPRINT.txt")"
[ "${signer}" = "${expected}" ] || { echo "Fauxx.apk is signed by ${signer}, not the release key" >&2; exit 1; }
(cd "${out}" && shasum -a 256 Fauxx.apk "fauxx-source-${version}.tar.gz")
