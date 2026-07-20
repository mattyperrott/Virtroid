#!/bin/bash
set -Eeuo pipefail

export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 0077

readonly trusted_helper=/usr/local/sbin/virtroid-renterd-admin
readonly password_file=/etc/virtroid/secrets/renterd-api-password
readonly deploy_env=/opt/virtroid/deploy/vps/.env

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi
if [ "$(readlink -f -- "$0")" != "${trusted_helper}" ]; then
  echo "run the installed root-owned helper: ${trusted_helper}" >&2
  exit 1
fi
case "${1:-}" in
  status|activate-policy|prepare-smoke) ;;
  *)
    echo "usage: virtroid-renterd-admin [status|activate-policy|prepare-smoke]" >&2
    exit 64
    ;;
esac
[ "$#" -eq 1 ] || exit 64

for protected_file in "${password_file}" "${deploy_env}"; do
  [ -f "${protected_file}" ] && [ ! -L "${protected_file}" ] || {
    echo "unsafe or missing protected file: ${protected_file}" >&2
    exit 1
  }
  [ "$(stat -c '%u' "${protected_file}")" -eq 0 ] || {
    echo "protected file is not root-owned: ${protected_file}" >&2
    exit 1
  }
  case "$(stat -c '%a' "${protected_file}")" in
    400|600) ;;
    *) echo "protected file has an unsafe mode: ${protected_file}" >&2; exit 1 ;;
  esac
done

exec python3 - "${1}" "${password_file}" "${deploy_env}" <<'PY'
import base64
import json
import os
import re
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

command, password_path, env_path = sys.argv[1:]

with open(password_path, "r", encoding="ascii") as f:
    password = f.read().strip()
if len(password) != 64 or any(c not in "0123456789abcdef" for c in password):
    raise SystemExit("invalid renterd API password file")

env = {}
with open(env_path, "r", encoding="utf-8") as f:
    for raw in f:
        line = raw.rstrip("\r\n")
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key in env:
            raise SystemExit(f"duplicate deployment environment key: {key}")
        env[key] = value

expected_address = env.get("NODE_SIA_RENTERD_WALLET_ADDRESS", "")
bucket_name = env.get("NODE_SIA_RENTERD_BUCKET", "")
if re.fullmatch(r"[0-9a-f]{76}", expected_address) is None:
    raise SystemExit("deployment environment is missing the offline-derived wallet address")
if not bucket_name:
    raise SystemExit("deployment environment is missing the renterd bucket")

authorization = "Basic " + base64.b64encode((":" + password).encode()).decode()

def request(method, path, payload=None):
    data = None
    headers = {"Authorization": authorization, "Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload, separators=(",", ":")).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(
        "http://127.0.0.1:9980" + path,
        data=data,
        headers=headers,
        method=method,
    )
    try:
        with urllib.request.urlopen(req, timeout=20) as response:
            body = response.read(8 * 1024 * 1024)
    except urllib.error.HTTPError as e:
        detail = e.read(2048).decode(errors="replace").strip()
        raise SystemExit(f"renterd {method} {path} failed with HTTP {e.code}: {detail}")
    except OSError as e:
        raise SystemExit(f"renterd {method} {path} failed: {e}")
    if not body:
        return None
    try:
        return json.loads(body)
    except json.JSONDecodeError as e:
        raise SystemExit(f"renterd {method} {path} returned invalid JSON: {e}")

def currency_positive(value):
    if isinstance(value, str):
        return value.isdigit() and int(value) > 0
    if isinstance(value, (int, float)):
        return value > 0
    if isinstance(value, dict):
        return any(currency_positive(v) for v in value.values())
    return False

def spendable_positive(wallet):
    for key in ("spendable", "confirmed", "siacoins", "balance"):
        if key in wallet and currency_positive(wallet[key]):
            return True
    return False

def get_state():
    consensus = request("GET", "/api/bus/consensus/state") or {}
    wallet = request("GET", "/api/bus/wallet") or {}
    autopilot = request("GET", "/api/autopilot/state") or {}
    buckets = request("GET", "/api/bus/buckets") or []
    contracts = request("GET", "/api/bus/contracts?filtermode=active") or []
    private_bucket = any(
        b.get("name") == bucket_name and not b.get("policy", {}).get("publicReadAccess", False)
        for b in buckets
        if isinstance(b, dict)
    )
    return {
        "consensus_synced": consensus.get("synced") is True,
        "wallet_address_match": wallet.get("address") == expected_address,
        "wallet_spendable_positive": spendable_positive(wallet),
        "autopilot_enabled": autopilot.get("enabled") is True,
        "private_bucket": private_bucket,
        "active_contracts": len(contracts) if isinstance(contracts, list) else 0,
    }

def update_environment(updates):
    directory = os.path.dirname(env_path)
    with open(env_path, "r", encoding="utf-8") as f:
        lines = f.readlines()
    seen = set()
    output = []
    for raw in lines:
        line = raw.rstrip("\r\n")
        key = line.split("=", 1)[0] if "=" in line else ""
        if key in updates:
            if key not in seen:
                output.append(f"{key}={updates[key]}\n")
                seen.add(key)
            continue
        output.append(raw if raw.endswith("\n") else raw + "\n")
    for key, value in updates.items():
        if key not in seen:
            output.append(f"{key}={value}\n")
    fd, temporary = tempfile.mkstemp(prefix=".renterd-admin.", dir=directory, text=True)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.writelines(output)
            f.flush()
            os.fsync(f.fileno())
        os.chown(temporary, 0, 0)
        os.replace(temporary, env_path)
        directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)

if command == "activate-policy":
    consensus = request("GET", "/api/bus/consensus/state") or {}
    wallet = request("GET", "/api/bus/wallet") or {}
    if consensus.get("synced") is not True:
        raise SystemExit("renterd consensus is not synced")
    if wallet.get("address") != expected_address:
        raise SystemExit("renterd wallet does not match the offline-derived address")
    if not spendable_positive(wallet):
        raise SystemExit("renterd wallet has no spendable mainnet SC")

    buckets = request("GET", "/api/bus/buckets") or []
    matching = [b for b in buckets if isinstance(b, dict) and b.get("name") == bucket_name]
    if not matching:
        request("POST", "/api/bus/buckets", {
            "name": bucket_name,
            "policy": {"publicReadAccess": False},
        })
    elif matching[0].get("policy", {}).get("publicReadAccess", False):
        request("PUT", "/api/bus/bucket/" + urllib.parse.quote(bucket_name, safe="") + "/policy", {
            "policy": {"publicReadAccess": False},
        })
    request("PUT", "/api/bus/autopilot", {"enabled": True})

if command == "prepare-smoke":
    state = get_state()
    required = (
        "consensus_synced",
        "wallet_address_match",
        "wallet_spendable_positive",
        "autopilot_enabled",
        "private_bucket",
    )
    failed = [key for key in required if state[key] is not True]
    if failed:
        raise SystemExit("renterd is not ready for smoke testing: " + ", ".join(failed))
    if state["active_contracts"] < 30:
        raise SystemExit(f"renterd has {state['active_contracts']} active contracts; 30 are required")
    update_environment({"NODE_SIA_RENTERD_WORKER_URL": "http://renterd:9980"})

state = get_state()
for key in (
    "consensus_synced",
    "wallet_address_match",
    "wallet_spendable_positive",
    "autopilot_enabled",
    "private_bucket",
    "active_contracts",
):
    value = state[key]
    if isinstance(value, bool):
        value = str(value).lower()
    print(f"{key}={value}")
PY
