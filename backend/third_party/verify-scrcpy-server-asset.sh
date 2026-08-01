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

python3 - "${built_apk}" "${embedded_jar}" <<'PY'
from __future__ import annotations

import hashlib
import sys
import zipfile
from pathlib import Path

built_path = Path(sys.argv[1])
embedded_path = Path(sys.argv[2])
expected_embedded_sha256 = "d9114c91fceb2567df6877a15b7505aff4b1bedb4f8169526d8166a645ef3c6a"

actual_embedded_sha256 = hashlib.sha256(embedded_path.read_bytes()).hexdigest()
if actual_embedded_sha256 != expected_embedded_sha256:
    raise SystemExit(
        "embedded scrcpy server asset digest mismatch: "
        f"expected {expected_embedded_sha256}, got {actual_embedded_sha256}"
    )


def payloads(path: Path) -> dict[str, bytes]:
    try:
        with zipfile.ZipFile(path) as archive:
            names = [entry.filename for entry in archive.infolist()]
            if len(names) != len(set(names)):
                raise SystemExit(f"{path}: duplicate ZIP members are not allowed")
            if any(name.startswith("/") or ".." in Path(name).parts for name in names):
                raise SystemExit(f"{path}: unsafe ZIP member name")
            return {name: archive.read(name) for name in names if not name.endswith("/")}
    except zipfile.BadZipFile as exc:
        raise SystemExit(f"{path}: invalid APK/ZIP payload: {exc}") from exc


built_payloads = payloads(built_path)
embedded_payloads = payloads(embedded_path)
if built_payloads.keys() != embedded_payloads.keys():
    missing = sorted(built_payloads.keys() - embedded_payloads.keys())
    extra = sorted(embedded_payloads.keys() - built_payloads.keys())
    raise SystemExit(
        "embedded scrcpy server member list differs from the vendored source build: "
        f"missing={missing}, extra={extra}"
    )

different = [
    name
    for name, built_payload in built_payloads.items()
    if built_payload != embedded_payloads[name]
]
if different:
    raise SystemExit(
        "embedded scrcpy server payload differs from the vendored source build: "
        + ", ".join(different)
    )

print(
    "embedded scrcpy server digest is pinned and every packaged payload byte "
    "matches the vendored source build"
)
PY
