#!/usr/bin/env python3
"""Loopback-only control-plane fixture for Android UI QA.

Run this behind ``adb reverse tcp:18080 tcp:18080`` and build the debug app
with ``VIRTROID_DEBUG_CONTROL_PLANE_URL=http://127.0.0.1:18080``. The server
does not validate signatures; production authentication is covered separately.
"""

from __future__ import annotations

import argparse
import copy
import json
import re
import threading
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


NODE_PUBLIC_KEY = (
    "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE7UFmyDRf7CB8zNxI0x7vlu4wrgrK"
    "Wg+1d3AJcV8bFfhknFaGr3Bf5Ddy+gmK1MVbQP5rMCLmJZXht/PHWxXrHA=="
)
QA_BOOTSTRAP_INVITE = "virtroid-qa-bootstrap-invite"


def runtime_fixture(runtime_id: str, name: str = "QA Android") -> dict:
    return {
        "id": runtime_id,
        "name": name,
        "status": "stopped",
        "desired_state": "stopped",
        "connection_status": "offline",
        "host_id": "qa-host-1",
        "persona_version": 3,
        "android_image": "virtroid/android-15:qa",
        "android_version": "Android 15",
        "width_px": 1080,
        "height_px": 2280,
        "density_dpi": 420,
        "audio_enabled": True,
        "camera_mode": "disabled",
        "file_mode": "upload-only",
        "blob_auto_snapshot": True,
        "blob_retain_days": 7,
        "blob_last_snapshot_at": "2026-07-21T08:00:00Z",
        "blob_manifest_json": {"snapshot_id": "qa-snapshot-1", "generation": 4},
        "started_at": None,
        "load_average": None,
        "adb_port": None,
        "viewer_port": None,
        "last_error": None,
        "active_persona_json": json.dumps(
            {
                "brand": "Virtroid",
                "model": "Private Device",
                "manufacturer": "Virtroid",
                "release": "15",
                "fingerprint": "virtroid/qa/device:15/QA:user/release-keys",
            }
        ),
    }


class FixtureState:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.reset()

    def reset(self) -> None:
        self.account_id = "qa-account"
        self.device_id = "qa-device-current"
        self.runtimes = {"qa-runtime-1": runtime_fixture("qa-runtime-1")}
        self.deleted_runtimes: set[str] = set()
        self.selected_apps = {"org.fdroid.fdroid", "com.simplemobiletools.notes.pro"}
        self.revoked_devices: set[str] = set()
        self.session_id = "qa-session-1"
        self.bootstrap_invite_consumed = False

    def runtime(self, runtime_id: str) -> dict:
        return self.runtimes[runtime_id]


STATE = FixtureState()


def runtime_state(item: dict) -> dict:
    running = item["status"] == "running"
    deleted = item["id"] in STATE.deleted_runtimes
    return {
        "runtime": copy.deepcopy(item),
        "effective_state": "deleted" if deleted else item["status"],
        "runtime_ready": running,
        "has_active_session": False,
        "has_current_device_session": False,
        "current_device_session_id": None,
        "can_connect": running,
        "can_start": not running and not deleted,
        "can_stop": running,
        "can_wipe": not deleted,
        "can_delete": not deleted,
        "is_busy": False,
        "blocked_reason": None,
    }


def entitlement() -> dict:
    runtime_count = len([key for key in STATE.runtimes if key not in STATE.deleted_runtimes])
    running_count = sum(
        runtime["status"] == "running"
        for key, runtime in STATE.runtimes.items()
        if key not in STATE.deleted_runtimes
    )
    return {
        "account_id": STATE.account_id,
        "source": "trial",
        "status": "active",
        "runtime_limit": 3,
        "runtime_count": runtime_count,
        "runtime_remaining": max(0, 3 - runtime_count),
        "active_runtime_limit": 2,
        "active_runtime_count": running_count,
        "active_runtime_remaining": max(0, 2 - running_count),
        "runtime_starts_per_day": 10,
        "runtime_starts_used_today": 2,
        "runtime_starts_remaining_today": 8,
        "storage_bytes_limit": 1073741824,
        "storage_bytes_used": 67108864,
        "storage_bytes_remaining": 1006632960,
        "trial_runtime_seconds": 3600,
        "trial_runtime_seconds_used": 300,
        "trial_runtime_seconds_remaining": 3300,
        "expires_at": "2026-08-21T08:00:00Z",
        "can_create_runtime": runtime_count < 3,
        "can_start_runtime": running_count < 2,
        "create_runtime_blocked_code": None,
        "create_runtime_blocked_reason": None,
        "start_runtime_blocked_code": None,
        "start_runtime_blocked_reason": None,
    }


def app_catalog() -> list[dict]:
    rows = [
        ("org.fdroid.fdroid", "F-Droid", "Privacy-friendly app catalogue", True, 13_000_000),
        ("com.simplemobiletools.notes.pro", "Simple Notes", "Offline notes without an account", True, 8_000_000),
        ("org.mozilla.fennec_fdroid", "Fennec", "Independent web browser", False, 91_000_000),
        ("com.aurora.store", "Aurora Store", "Anonymous Play Store client", False, 11_000_000),
    ]
    return [
        {
            "package_name": package,
            "source": "fdroid",
            "display_name": name,
            "summary": summary,
            "icon_url": None,
            "version_name": "1.0-qa",
            "version_code": 100,
            "apk_size_bytes": size,
            "min_sdk": 28,
            "native_code": None,
            "recommended": recommended,
            "selected": package in STATE.selected_apps,
            "catalog_updated_at": "2026-07-21T08:00:00Z",
        }
        for package, name, summary, recommended, size in rows
    ]


class Handler(BaseHTTPRequestHandler):
    server_version = "VirtroidUiQa/1"

    def log_message(self, fmt: str, *args: object) -> None:
        print(f"{self.command} {self.path} - {fmt % args}", flush=True)

    def read_json(self) -> dict:
        length = int(self.headers.get("Content-Length", "0"))
        if length == 0:
            return {}
        return json.loads(self.rfile.read(length).decode("utf-8"))

    def reply(self, payload: dict, status: int = 200) -> None:
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def route(self) -> tuple[str, dict, urllib.parse.ParseResult]:
        parsed = urllib.parse.urlparse(self.path)
        return parsed.path, self.read_json(), parsed

    def do_GET(self) -> None:  # noqa: N802
        path, _, parsed = self.route()
        with STATE.lock:
            if path == "/__qa__/state":
                self.reply({"runtimes": list(STATE.runtimes.values()), "entitlement": entitlement()})
                return
            if path == "/api/v1/me/runtimes":
                self.reply({"items": self.visible_runtimes()})
                return
            if path == "/api/v1/me/runtimes/state":
                self.reply({"items": [runtime_state(item) for item in self.visible_runtimes()]})
                return
            if path == "/api/v1/me/entitlement":
                self.reply(entitlement())
                return
            if path in {"/api/v1/apps/catalog", "/api/v1/me/apps/catalog"}:
                search = urllib.parse.parse_qs(parsed.query).get("search", [""])[0].lower()
                items = app_catalog()
                if search:
                    items = [item for item in items if search in item["display_name"].lower() or search in item["summary"].lower()]
                self.reply({"items": items})
                return
            if path == "/api/v1/me/storage":
                self.reply(
                    {
                        "provider": "local-disk",
                        "funding_model": "operator",
                        "wallet_address": None,
                        "funding_address": None,
                        "status": "ready",
                        "encrypted_seed_backed_up": False,
                        "last_preflight_status": "ready",
                        "last_preflight_json": None,
                        "last_preflight_at": "2026-07-21T08:00:00Z",
                    }
                )
                return
            if path == "/api/v1/me/devices":
                devices = [
                    {
                        "id": STATE.device_id,
                        "name": "Android emulator",
                        "public_key": "qa-current-public-key",
                        "created_at": "2026-07-21T08:00:00Z",
                        "last_seen_at": "2026-07-21T08:30:00Z",
                        "revoked_at": None,
                    },
                    {
                        "id": "qa-device-secondary",
                        "name": "Secondary QA device",
                        "public_key": "qa-secondary-public-key",
                        "created_at": "2026-07-20T08:00:00Z",
                        "last_seen_at": "2026-07-21T07:30:00Z",
                        "revoked_at": "2026-07-21T08:40:00Z" if "qa-device-secondary" in STATE.revoked_devices else None,
                    },
                ]
                self.reply({"items": devices})
                return
            runtime_match = re.fullmatch(r"/api/v1/me/runtimes/([^/]+)/(state|logs)", path)
            if runtime_match:
                runtime_id, operation = runtime_match.groups()
                item = STATE.runtimes.get(runtime_id)
                if item is None:
                    self.reply({"error": "runtime not found", "code": "runtime_not_found"}, 404)
                elif operation == "state":
                    self.reply(runtime_state(item))
                else:
                    self.reply(
                        {
                            "items": [
                                {"level": "INFO", "source": "runtime", "message": "Runtime fixture ready", "created_at": "2026-07-21T08:00:00Z"},
                                {"level": "WARN", "source": "storage", "message": "QA warning for filter coverage", "created_at": "2026-07-21T08:01:00Z"},
                            ]
                        }
                    )
                return
            session_match = re.fullmatch(r"/api/v1/me/sessions/([^/]+)", path)
            if session_match:
                item = next(iter(self.visible_runtimes()), runtime_fixture("qa-runtime-missing"))
                self.reply(
                    {
                        "session": {"id": session_match.group(1), "runtime_id": item["id"], "status": "active", "ended_at": None, "end_reason": None},
                        "runtime": item,
                        "effective_status": "active",
                        "can_resume": True,
                        "runtime_ready": True,
                        "is_expired": False,
                    }
                )
                return
        self.reply({"error": "fixture route not found", "code": "not_found"}, 404)

    def do_POST(self) -> None:  # noqa: N802
        path, body, parsed = self.route()
        with STATE.lock:
            if path == "/__qa__/reset":
                STATE.reset()
                self.reply({"status": "reset"})
                return
            if path == "/api/v1/bootstrap":
                if (
                    body.get("invite_token") != QA_BOOTSTRAP_INVITE
                    or STATE.bootstrap_invite_consumed
                ):
                    self.reply(
                        {
                            "error": "bootstrap invitation is invalid, expired, or already used",
                            "code": "bootstrap_invite_invalid",
                        },
                        403,
                    )
                    return
                STATE.bootstrap_invite_consumed = True
                STATE.account_id = body.get("account_id", STATE.account_id)
                STATE.device_id = body.get("device_id", STATE.device_id)
                self.reply(
                    {
                        "account": {"id": STATE.account_id},
                        "device": {"id": STATE.device_id},
                    },
                    201,
                )
                return
            if path == "/api/v1/me/identity/register" or path.endswith("/capability"):
                self.reply({"status": "ok"})
                return
            if path == "/api/v1/me/runtimes":
                runtime_id = f"qa-runtime-{len(STATE.runtimes) + 1}"
                item = runtime_fixture(runtime_id, body.get("name") or f"QA Android {len(STATE.runtimes) + 1}")
                item.update({key: value for key, value in body.items() if key in item})
                STATE.runtimes[runtime_id] = item
                self.reply(item, 201)
                return
            lease_match = re.fullmatch(r"/api/v1/me/runtimes/([^/]+)/blob-key-lease", path)
            if lease_match:
                runtime_id = lease_match.group(1)
                self.reply(
                    {
                        "lease_id": str(uuid.uuid4()),
                        "runtime_id": runtime_id,
                        "host_id": "qa-host-1",
                        "operation": body.get("operation", "start"),
                        "algorithm": "P256_ECDH_HKDF_SHA256_AESGCM_V1",
                        "node_public_key": NODE_PUBLIC_KEY,
                    }
                )
                return
            action_match = re.fullmatch(r"/api/v1/me/runtimes/([^/]+)/(start|stop|wipe|delete)", path)
            if action_match:
                runtime_id, action = action_match.groups()
                item = STATE.runtimes.get(runtime_id)
                if item is None:
                    self.reply({"error": "runtime not found", "code": "runtime_not_found"}, 404)
                    return
                if action == "start":
                    item.update(status="running", desired_state="running", connection_status="online", started_at="2026-07-21T08:45:00Z", load_average=0.24, adb_port=5555, viewer_port=27183)
                elif action == "stop":
                    item.update(status="stopped", desired_state="stopped", connection_status="offline", started_at=None, load_average=None, adb_port=None, viewer_port=None)
                elif action == "wipe":
                    item.update(blob_last_snapshot_at=None, blob_manifest_json={})
                else:
                    item.update(status="deleting", desired_state="deleted", connection_status="offline")
                    STATE.deleted_runtimes.add(runtime_id)
                self.reply(item)
                return
            session_create = re.fullmatch(r"/api/v1/me/runtimes/([^/]+)/session", path)
            if session_create:
                runtime_id = session_create.group(1)
                STATE.session_id = f"qa-session-{uuid.uuid4().hex[:8]}"
                self.reply(
                    {
                        "session": {"id": STATE.session_id, "relay_token": "qa-relay-token"},
                        "relay_host": "127.0.0.1",
                        "relay_port": 65535,
                        "relay_tls": True,
                        "relay_path": f"/api/v1/relay/{STATE.session_id}",
                        "viewer_public_key": NODE_PUBLIC_KEY,
                        "viewer_address": "qa-viewer.invalid:65535",
                        "runtime_id": runtime_id,
                    },
                    201,
                )
                return
            if re.fullmatch(r"/api/v1/me/sessions/[^/]+/(close|heartbeat|relay-token)", path):
                if path.endswith("relay-token"):
                    self.reply({"session": {"id": STATE.session_id, "relay_token": "qa-relay-token-refreshed"}})
                else:
                    self.reply({"status": "ok"})
                return
            if re.fullmatch(r"/api/v1/me/sessions/[^/]+/end", path):
                runtime_id = urllib.parse.parse_qs(parsed.query).get("runtime_id", [""])[0]
                item = STATE.runtimes.get(runtime_id)
                if item is None:
                    self.reply({"error": "runtime not found", "code": "runtime_not_found"}, 404)
                    return
                item.update(status="stopped", desired_state="stopped", connection_status="offline")
                self.reply(item)
                return
        self.reply({"error": "fixture route not found", "code": "not_found"}, 404)

    def do_PATCH(self) -> None:  # noqa: N802
        path, body, _ = self.route()
        runtime_match = re.fullmatch(r"/api/v1/me/runtimes/([^/]+)", path)
        with STATE.lock:
            if runtime_match and runtime_match.group(1) in STATE.runtimes:
                item = STATE.runtime(runtime_match.group(1))
                item.update({key: value for key, value in body.items() if key in item})
                self.reply(item)
                return
        self.reply({"error": "fixture route not found", "code": "not_found"}, 404)

    def do_PUT(self) -> None:  # noqa: N802
        path, body, _ = self.route()
        if path == "/api/v1/me/apps/selections":
            with STATE.lock:
                STATE.selected_apps = set(body.get("package_names", []))
                self.reply({"items": app_catalog()})
            return
        self.reply({"error": "fixture route not found", "code": "not_found"}, 404)

    def do_DELETE(self) -> None:  # noqa: N802
        path, _, _ = self.route()
        with STATE.lock:
            device_match = re.fullmatch(r"/api/v1/me/devices/([^/]+)", path)
            if device_match:
                STATE.revoked_devices.add(device_match.group(1))
                self.reply({"status": "revoked"})
                return
            if path == "/api/v1/me":
                self.reply({"status": "deleted"})
                return
        self.reply({"error": "fixture route not found", "code": "not_found"}, 404)

    @staticmethod
    def visible_runtimes() -> list[dict]:
        return [
            copy.deepcopy(item)
            for runtime_id, item in STATE.runtimes.items()
            if runtime_id not in STATE.deleted_runtimes
        ]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=18080)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"Virtroid UI QA fixture listening on http://127.0.0.1:{args.port}", flush=True)
    print(f"One-time QA bootstrap invitation: {QA_BOOTSTRAP_INVITE}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
