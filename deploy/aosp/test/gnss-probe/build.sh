#!/usr/bin/env bash
# build.sh [OUT.apk]: builds the GNSS probe that boot-test.sh's location check
# runs in a runtime (BOOT_TEST_GNSS_PROBE), with the tools of a built tree
# ($LINEAGE_ROOT/src after build-lineage.sh's build stage). A test app, signed
# with AOSP's public test key: never part of the image.
set -euo pipefail
src="${LINEAGE_ROOT:-/srv/lineage}/src"
here="$(cd "$(dirname "$0")" && pwd)"
out="$(realpath -m "${1:-${here}/gnss-probe.apk}")"
host="${src}/out/host/linux-x86/bin"
jdk="${src}/prebuilts/jdk/jdk21/linux-x86/bin"
sdk="${src}/prebuilts/sdk/36/public/android.jar"
# d8 runs the java it finds first.
export PATH="${jdk}:${PATH}"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
mkdir -p "${work}/classes" "${work}/dex"
"${jdk}/javac" -source 17 -target 17 -cp "${sdk}" -d "${work}/classes" $(find "${here}/src" -name '*.java')
"${host}/d8" --min-api 31 --lib "${sdk}" --output "${work}/dex" $(find "${work}/classes" -name '*.class')
"${host}/aapt2" link --manifest "${here}/AndroidManifest.xml" -I "${sdk}" -o "${work}/base.apk"
(cd "${work}/dex" && zip -q ../base.apk classes.dex)
"${host}/zipalign" -f 4 "${work}/base.apk" "${work}/aligned.apk"
"${jdk}/java" -jar "${src}/prebuilts/sdk/tools/linux/lib/apksigner.jar" sign \
  --key "${src}/build/make/target/product/security/testkey.pk8" \
  --cert "${src}/build/make/target/product/security/testkey.x509.pem" --out "${out}" "${work}/aligned.apk"
sha256sum "${out}"
