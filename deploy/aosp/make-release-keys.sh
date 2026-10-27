#!/usr/bin/env bash
# make-release-keys.sh KEYDIR: creates the keys Virtroid's Android image is
# signed with, in place of AOSP's public test keys. Run it on the build host:
#
#   KEYDIR ($LINEAGE_ROOT/keys)  the private halves, which never leave it
#   keys/ (beside this script)   the public halves, keys.mk and Android.bp,
#                                which are in git; copy them back
#
# It only adds keys KEYDIR doesn't have. Never replace one: a runtime's data
# records the certificates its system apps were signed with, and while
# Android accepts a change of system signatures, every change is another
# migration to test (README.md, release keys).
#
# build-lineage.sh's patch stage puts both halves in vendor/lineage-priv/keys,
# which LineageOS's configuration reads: keys.mk makes releasekey the default
# certificate (so the build is tagged release-keys), app certificates named
# like "platform" come from the same directory, and so do APEX payload keys.
# Certificates that modules keep in their own directories are swapped for
# ours with PRODUCT_CERTIFICATE_OVERRIDES. A key missing here falls back to
# the module's public one without a word, which is why the package stage runs
# check-signatures.py.
set -euo pipefail

keydir="${1:?usage: make-release-keys.sh KEYDIR}"
here="$(cd "$(dirname "$0")" && pwd)"
public="${here}/keys"
src="${LINEAGE_ROOT:-/srv/lineage}/src"
avbtool="${src}/external/avb/avbtool.py"
# A plain subject: apps can read the system's certificates, so naming
# Virtroid here would label every runtime.
subject="/CN=Android"

# Certificates the build looks up by name in the default certificate's
# directory (certificate: "platform" and the like).
app_certs=(releasekey platform shared media networkstack sdk_sandbox bluetooth nfc testkey)

# Modules that name a certificate module of their own, and the certificate
# each gets instead: one per certificate file the module tree had.
overrides=(
  AdServicesApk:com.android.adservices.api
  FederatedCompute:com.android.federatedcompute
  HealthConnectBackupRestore:com.android.health.connect.backuprestore
  HealthConnectController:com.android.healthconnect.controller
  OsuLogin:com.android.hotspot2.osulogin
  SafetyCenterResources:com.android.safetycenter.resources
  ServiceConnectivityResources:com.android.connectivity.resources
  ServiceUwbResources:com.android.uwb.resources
  ServiceWifiResources:com.android.wifi.resources
  WifiDialog:com.android.wifi.dialog
  com.android.appsearch.apk:com.android.appsearch.apk
  com.android.adbd:com.android.adbd
  com.android.adservices:com.android.adservices
  com.android.appsearch:com.android.appsearch
  com.android.art:com.android.art
  com.android.bt:com.android.bt
  com.android.configinfrastructure:com.android.configinfrastructure
  com.android.conscrypt:com.android.conscrypt
  com.android.crashrecovery:com.android.crashrecovery
  com.android.devicelock:com.android.devicelock
  com.android.extservices:com.android.extservices
  com.android.hardware.cas:com.android.hardware
  com.android.hardware.gnss:com.android.hardware
  com.android.healthfitness:com.android.healthfitness
  com.android.i18n:com.android.i18n
  com.android.ipsec:com.android.ipsec
  com.android.media:com.android.media
  com.android.media.swcodec:com.android.media.swcodec
  com.android.mediaprovider:com.android.mediaprovider
  com.android.neuralnetworks:com.android.neuralnetworks
  com.android.nfcservices:com.android.nfcservices
  com.android.os.statsd:com.android.os.statsd
  com.android.permission:com.android.permission
  com.android.profiling:com.android.profiling
  com.android.resolv:com.android.resolv
  com.android.rkpd:com.android.rkpd
  com.android.runtime:com.android.runtime
  com.android.scheduling:com.android.scheduling
  com.android.sdkext:com.android.sdkext
  com.android.telephonycore:com.android.telephonycore
  com.android.tzdata:com.android.tzdata
  com.android.uprobestats:com.android.uprobestats
  com.android.uwb:com.android.uwb
  com.android.virt:com.android.virt
  com.android.wifi:com.android.wifi
)

# APEX payload keys, by the name of each APEX's apex_key files.
apex_keys=(
  com.android.adbd com.android.adservices com.android.appsearch com.android.art
  com.android.bt com.android.configinfrastructure com.android.conscrypt
  com.android.crashrecovery com.android.devicelock com.android.extservices
  com.android.hardware com.android.healthfitness com.android.i18n
  com.android.ipsec com.android.media com.android.media.swcodec
  com.android.mediaprovider com.android.neuralnetworks com.android.nfcservices
  com.android.ondevicepersonalization com.android.os.statsd
  com.android.permission com.android.profiling com.android.resolv
  com.android.rkpd com.android.runtime com.android.scheduling com.android.sdkext
  com.android.telephonycore com.android.tethering com.android.tzdata
  com.android.uprobestats com.android.uwb com.android.virt com.android.wifi
)

umask 077
mkdir -p "${keydir}" "${public}"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

# NAME.pk8 (private, DER PKCS#8) and NAME.x509.pem: an APK or APEX container
# certificate, as development/tools/make_key makes them, in 4096-bit RSA.
new_cert() {
  local name="$1"
  [ -f "${keydir}/${name}.pk8" ] && return
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out "${tmp}/key.pem" 2>/dev/null
  openssl req -new -x509 -sha256 -days 10000 -subj "${subject}" -key "${tmp}/key.pem" -out "${keydir}/${name}.x509.pem"
  openssl pkcs8 -topk8 -nocrypt -in "${tmp}/key.pem" -outform DER -out "${keydir}/${name}.pk8"
  rm -f "${tmp}/key.pem"
  echo "created certificate ${name}"
}

# NAME.pem (private) and NAME.avbpubkey: an APEX payload key. apexer signs
# payloads with SHA256_RSA4096, so these must be 4096-bit RSA.
new_payload_key() {
  local name="$1"
  [ -f "${keydir}/${name}.pem" ] && return
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out "${tmp}/key.pem" 2>/dev/null
  openssl rsa -in "${tmp}/key.pem" -traditional -out "${keydir}/${name}.pem" 2>/dev/null
  rm -f "${tmp}/key.pem"
  python3 "${avbtool}" extract_public_key --key "${keydir}/${name}.pem" --output "${keydir}/${name}.avbpubkey"
  echo "created payload key ${name}"
}

for name in "${app_certs[@]}"; do new_cert "${name}"; done
for pair in "${overrides[@]}"; do new_cert "${pair#*:}"; done
for name in "${apex_keys[@]}"; do new_payload_key "${name}"; done

# The public halves, for git and for check-signatures.py.
rm -f "${public}"/*.x509.pem "${public}"/*.avbpubkey
cp "${keydir}"/*.x509.pem "${keydir}"/*.avbpubkey "${public}/"
chmod 644 "${public}"/*

{
  echo "# Virtroid's release keys (written by deploy/aosp/make-release-keys.sh)."
  echo "PRODUCT_DEFAULT_DEV_CERTIFICATE := vendor/lineage-priv/keys/releasekey"
  echo "# No OTA keys: a runtime never installs an update package."
  echo "PRODUCT_EXTRA_RECOVERY_KEYS :="
  echo "PRODUCT_CERTIFICATE_OVERRIDES += \\"
  for pair in "${overrides[@]}"; do
    echo "    ${pair%%:*}:virtroid.${pair#*:} \\"
  done
  echo ""
} >"${public}/keys.mk"

{
  echo "// Virtroid's certificates for modules that name one of their own"
  echo "// (keys.mk overrides each). Written by deploy/aosp/make-release-keys.sh."
  for cert in $(printf '%s\n' "${overrides[@]}" | cut -d: -f2 | sort -u); do
    echo ""
    echo "android_app_certificate {"
    echo "    name: \"virtroid.${cert}\","
    echo "    certificate: \"${cert}\","
    echo "}"
  done
} >"${public}/Android.bp"

echo "${keydir}: $(ls "${keydir}" | grep -c '\.pk8$') certificates, $(ls "${keydir}" | grep -c '\.avbpubkey$') payload keys; public halves in ${public}"
