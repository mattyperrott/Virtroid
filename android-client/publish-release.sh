#!/bin/bash
# Publishes a signed release APK as a GitHub release, with its SHA-256, so
# installed apps offer it as an update (app/.../update/AppUpdater.kt). Run it
# on the Mac that built the release; nothing secret leaves it.
#
#   usage: android-client/publish-release.sh <version> [notes file]
set -euo pipefail

version="${1:?usage: publish-release.sh <version> [notes file]}"
notes="${2:-}"
here="$(cd "$(dirname "$0")" && pwd)"
repository=mattyperrott/Virtroid
name="Virtroid-${version}-release.apk"
apk="${here}/signing/output/${name}"
built="${here}/app/build/outputs/apk/release/app-release.apk"
build_tools="$(ls -d "${ANDROID_HOME:-$HOME/Library/Android/sdk}"/build-tools/* | tail -1)"

die() { echo "publish-release: $*" >&2; exit 1; }

version_of() { "${build_tools}/aapt2" dump badging "$1" | sed -n "s/.*versionName='\([^']*\)'.*/\1/p" | head -1; }

if [ ! -f "${apk}" ]; then
  [ -f "${built}" ] && [ "$(version_of "${built}")" = "${version}" ] ||
    die "no ${version} release APK: build it with signing/build-signed-release.sh first"
  cp "${built}" "${apk}"
fi
[ "$(version_of "${apk}")" = "${version}" ] || die "${name} is not version ${version}"

# Only an APK signed with the release key is published.
expected="$(grep -o 'Certificate SHA-256: [0-9A-Fa-f:]*' "${here}/signing/RELEASE_KEY_FINGERPRINT.txt" | awk '{print $3}' | tr -d ':' | tr 'A-F' 'a-f')"
actual="$("${build_tools}/apksigner" verify --print-certs "${apk}" | sed -n 's/.*certificate SHA-256 digest: //p' | head -1)"
[ -n "${expected}" ] && [ "${actual}" = "${expected}" ] || die "${name} is not signed with the release key"

sha="$(shasum -a 256 "${apk}" | cut -c1-64)"
echo "${sha}  ${name}" > "${apk}.sha256"

notes_args=(--notes "Virtroid ${version}. SHA-256 of ${name}: ${sha}")
[ -n "${notes}" ] && notes_args=(--notes-file "${notes}")
gh release create "v${version}" "${apk}" "${apk}.sha256" \
  --repo "${repository}" --title "Virtroid ${version}" --target main "${notes_args[@]}"
echo "published v${version}: ${name} (SHA-256 ${sha})"
