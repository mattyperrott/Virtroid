package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/config"
)

// fakeAndroidADB stands in for adb: installs and uninstalls are files in
// state/installed, and appops commands are appended to state/appops.
const fakeAndroidADB = `#!/bin/sh
set -eu
state="$ADB_TEST_STATE"
case "$1" in connect|disconnect) exit 0 ;; esac
[ "$1" = "-s" ] || exit 1
shift 2
case "$1" in
reconnect) exit 0 ;;
install)
  pkg=$(basename "$4" .apk)
  : >"$state/installed/$pkg"
  echo Success ;;
shell)
  cmd="$2"
  case "$cmd" in
  "pm path "*)
    pkg=$(echo "$cmd" | cut -d' ' -f3 | tr -d "'")
    [ -f "$state/installed/$pkg" ] && echo installed || true ;;
  "appops "*) echo "$cmd" >>"$state/appops" ;;
  esac ;;
esac
`

func TestRuntimeAppsInstallOnceAndStoresMayInstall(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "adb-state")
	if err := os.MkdirAll(filepath.Join(state, "installed"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADB_TEST_STATE", state)
	adbPath := filepath.Join(root, "adb")
	if err := os.WriteFile(adbPath, []byte(fakeAndroidADB), 0o700); err != nil {
		t.Fatal(err)
	}

	// Two pinned local apps: a store every runtime gets, and one to select.
	apkDir := filepath.Join(root, "apks")
	if err := os.MkdirAll(apkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pin := func(name string) string {
		t.Helper()
		body := []byte("apk " + name)
		if err := os.WriteFile(filepath.Join(apkDir, name+".apk"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		return hex.EncodeToString(sum[:])
	}
	manifest, _ := json.Marshal(appInstallManifest{Version: 1, Apps: []appManifestEntry{
		{PackageName: "org.example.store", Artifact: "org.example.store.apk", SHA256: pin("org.example.store"), Default: true, AllowInstalls: true},
		{PackageName: "org.example.notes", Artifact: "org.example.notes.apk", SHA256: pin("org.example.notes")},
	}})
	if err := os.WriteFile(filepath.Join(apkDir, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	// The node logs installs to the control plane.
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer controlPlane.Close()
	newNode := func() *nodeAgent {
		return &nodeAgent{
			cfg: config.NodeConfig{
				ADBPath:         adbPath,
				ADBHost:         "127.0.0.1",
				RuntimeRoot:     filepath.Join(root, "runtimes"),
				AppAPKDir:       apkDir,
				AppManifestPath: filepath.Join(apkDir, "manifest.json"),
				ControlPlaneURL: controlPlane.URL,
			},
			controlPlane: controlPlane.Client(),
		}
	}
	port := 5555
	firstSelection := time.Date(2026, time.October, 14, 1, 0, 0, 0, time.UTC)
	runtime := runtimeAssignment{
		ID:           "runtime-apps",
		ADBPort:      &port,
		SelectedApps: []runtimeApp{{PackageName: "org.example.notes", SelectedAt: firstSelection}},
	}
	installed := func(pkg string) bool {
		_, err := os.Stat(filepath.Join(state, "installed", pkg))
		return err == nil
	}
	ensure := func() {
		t.Helper()
		// A fresh agent each time, as after a restart or on the next VM: what
		// it knows comes from the runtime's data.
		if err := newNode().ensureSelectedAppsInstalled(context.Background(), runtime, dockerInspectResponse{}); err != nil {
			t.Fatalf("ensureSelectedAppsInstalled: %v", err)
		}
	}

	ensure()
	if !installed("org.example.store") || !installed("org.example.notes") {
		t.Fatal("default and selected apps were not installed")
	}
	appops, _ := os.ReadFile(filepath.Join(state, "appops"))
	if !strings.Contains(string(appops), "appops set 'org.example.store' REQUEST_INSTALL_PACKAGES allow") ||
		strings.Contains(string(appops), "org.example.notes") {
		t.Fatalf("appops = %q, want only the store allowed to install", appops)
	}

	// Uninstalled in Android: both stay uninstalled.
	for _, pkg := range []string{"org.example.store", "org.example.notes"} {
		if err := os.Remove(filepath.Join(state, "installed", pkg)); err != nil {
			t.Fatal(err)
		}
	}
	ensure()
	if installed("org.example.store") || installed("org.example.notes") {
		t.Fatal("an app the user uninstalled was installed again")
	}

	// Selecting it again brings it back.
	runtime.SelectedApps[0].SelectedAt = firstSelection.Add(time.Hour)
	ensure()
	if !installed("org.example.notes") || installed("org.example.store") {
		t.Fatal("re-selecting reinstalled the wrong apps")
	}

	// A wiped runtime has no record, so it gets everything again.
	if err := os.RemoveAll(filepath.Join(root, "runtimes", runtime.ID, "data")); err != nil {
		t.Fatal(err)
	}
	ensure()
	if !installed("org.example.store") {
		t.Fatal("a wiped runtime did not get its default apps")
	}
}

func TestInstalledAppRecordRejectsUnknownVersionsAndPackages(t *testing.T) {
	node := &nodeAgent{cfg: config.NodeConfig{RuntimeRoot: t.TempDir()}}
	path := node.installedAppRecordPath("runtime-1")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"version":2,"apps":{"org.example.app":{}}}`)
	if record := node.loadInstalledAppRecord("runtime-1"); len(record.Apps) != 0 {
		t.Fatalf("future record version read: %+v", record)
	}
	write(`{"version":1,"apps":{"org.example.app":{},"../escape":{}}}`)
	record := node.loadInstalledAppRecord("runtime-1")
	if _, ok := record.Apps["org.example.app"]; !ok || len(record.Apps) != 1 {
		t.Fatalf("record = %+v, want only the valid package", record)
	}
	write(`not json`)
	if record := node.loadInstalledAppRecord("runtime-1"); len(record.Apps) != 0 {
		t.Fatalf("corrupt record read: %+v", record)
	}
}
