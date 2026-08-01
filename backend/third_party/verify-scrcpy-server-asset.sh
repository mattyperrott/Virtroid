#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
built_apk="${repo_root}/third_party/scrcpy-server/build/outputs/apk/release/scrcpy-server-release-unsigned.apk"
embedded_jar="${repo_root}/backend/cmd/virtnoded/assets/scrcpy-server.jar"

if [ ! -f "${built_apk}" ]; then
  echo "build the scrcpy server release module before verifying the embedded asset" >&2
  exit 1
fi
if [ ! -f "${embedded_jar}" ]; then
  echo "embedded scrcpy server asset is missing" >&2
  exit 1
fi

if ! cmp -s "${built_apk}" "${embedded_jar}"; then
  echo "embedded scrcpy server asset does not byte-for-byte match the vendored source build" >&2
  exit 1
fi

echo "embedded scrcpy server asset byte-for-byte matches vendored source build"
