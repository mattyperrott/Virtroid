package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"virtroid/backend/internal/config"
)

func TestNormalizeViewerPrepareParamsDefaultsAndBounds(t *testing.T) {
	maxSize, bitRate, err := normalizeViewerPrepareParams(0, 0, runtimeAssignment{
		WidthPx:  720,
		HeightPx: 1600,
	})
	if err != nil {
		t.Fatalf("normalizeViewerPrepareParams returned error: %v", err)
	}
	if maxSize != 1600 {
		t.Fatalf("maxSize = %d, want 1600", maxSize)
	}
	if bitRate != viewerDefaultBitRate {
		t.Fatalf("bitRate = %d, want %d", bitRate, viewerDefaultBitRate)
	}
}

func TestNormalizeViewerPrepareParamsRejectsOutOfRangeValues(t *testing.T) {
	if _, _, err := normalizeViewerPrepareParams(viewerMaxMaxSize+1, viewerDefaultBitRate, runtimeAssignment{}); err == nil || !strings.Contains(err.Error(), "max_size") {
		t.Fatalf("oversized max_size error = %v, want max_size rejection", err)
	}
	if _, _, err := normalizeViewerPrepareParams(viewerDefaultMaxSize, viewerMaxBitRate+1, runtimeAssignment{}); err == nil || !strings.Contains(err.Error(), "bit_rate") {
		t.Fatalf("oversized bit_rate error = %v, want bit_rate rejection", err)
	}
}

func TestRuntimeAppsToInstallMergesDefaultsAndSelections(t *testing.T) {
	node := &nodeAgent{
		cfg: config.NodeConfig{
			DefaultAppPackages: []string{"org.fdroid.basic"},
		},
	}
	apps := node.runtimeAppsToInstall(runtimeAssignment{
		SelectedApps: []runtimeApp{
			{PackageName: "org.fdroid.basic", DisplayName: "F-Droid Basic"},
			{PackageName: "org.videolan.vlc", DisplayName: "VLC"},
		},
	})
	if len(apps) != 2 {
		t.Fatalf("len(apps) = %d, want 2: %+v", len(apps), apps)
	}
	if apps[0].PackageName != "org.fdroid.basic" || apps[0].APKURL == "" || apps[0].APKSHA256 == "" {
		t.Fatalf("first default app = %+v, want pinned F-Droid Basic metadata", apps[0])
	}
	if apps[1].PackageName != "org.videolan.vlc" {
		t.Fatalf("second app = %+v, want selected VLC after defaults", apps[1])
	}
}

func TestStoragePreflightStatusKeepsUnreachableRenterdAsError(t *testing.T) {
	status := storagePreflightStatus(blobPreflightReport{
		Store: blobStoreRenterd,
		OK:    false,
		Checks: []blobPreflightCheck{
			{Name: "worker_url", Status: "pass", Detail: "http://renterd:9980"},
			{Name: "api_password", Status: "pass", Detail: "configured"},
			{Name: "consensus_state", Status: "fail", Detail: "dial tcp: lookup renterd: server misbehaving"},
			{Name: "active_contracts", Status: "fail", Detail: "dial tcp: lookup renterd: server misbehaving"},
		},
	})
	if status != "error" {
		t.Fatalf("status = %q, want error", status)
	}
}

func TestStoragePreflightStatusReportsContractsAfterReachableConsensus(t *testing.T) {
	status := storagePreflightStatus(blobPreflightReport{
		Store: blobStoreRenterd,
		OK:    false,
		Checks: []blobPreflightCheck{
			{Name: "worker_url", Status: "pass", Detail: "http://renterd:9980"},
			{Name: "api_password", Status: "pass", Detail: "configured"},
			{Name: "consensus_state", Status: "pass", Detail: "synced"},
			{Name: "wallet", Status: "pass", Detail: "funded"},
			{Name: "active_contracts", Status: "fail", Detail: "no active renterd contracts"},
		},
	})
	if status != "contracts_required" {
		t.Fatalf("status = %q, want contracts_required", status)
	}
}

func TestRuntimeAppsToInstallLoadsManifestDefaults(t *testing.T) {
	pin := strings.Repeat("a", 64)
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := `{
	  "version": 1,
	  "apps": [
	    {
	      "package_name": "projekt.launcher",
	      "display_name": "hyperion launcher",
	      "artifact": "projekt.launcher.apkm",
	      "install_mode": "apkm",
	      "sha256": "` + pin + `",
	      "set_as_home": true,
	      "home_activity": "projekt.launcher.ProjektLauncher",
	      "default": true
	    }
	  ]
	}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	node := &nodeAgent{
		cfg: config.NodeConfig{
			AppAPKDir:       root,
			AppManifestPath: manifestPath,
		},
	}
	apps := node.runtimeAppsToInstall(runtimeAssignment{})
	if len(apps) != 1 {
		t.Fatalf("len(apps) = %d, want 1", len(apps))
	}
	if apps[0].PackageName != "projekt.launcher" ||
		apps[0].Artifact != "projekt.launcher.apkm" ||
		apps[0].InstallMode != "apkm" ||
		apps[0].APKSHA256 != pin ||
		!apps[0].SetAsHome ||
		apps[0].HomeActivity != "projekt.launcher/projekt.launcher.ProjektLauncher" {
		t.Fatalf("default app = %+v, want manifest-pinned Hyperion app", apps[0])
	}
}

func TestRuntimeAppsToInstallSkipsManifestDefaultsWithoutPin(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	manifest := `{
	  "version": 1,
	  "apps": [
	    {
	      "package_name": "projekt.launcher",
	      "display_name": "hyperion launcher",
	      "artifact": "projekt.launcher.apk",
	      "default": true
	    }
	  ]
	}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	node := &nodeAgent{
		cfg: config.NodeConfig{
			AppAPKDir:       root,
			AppManifestPath: manifestPath,
		},
	}
	if apps := node.runtimeAppsToInstall(runtimeAssignment{}); len(apps) != 0 {
		t.Fatalf("apps = %+v, want unpinned manifest default skipped", apps)
	}
}

func TestAPKPathForSelectedAppDoesNotTrustImplicitLocalPackageAPK(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "projekt.launcher.apk"), []byte("malicious"), 0o644); err != nil {
		t.Fatalf("write local apk: %v", err)
	}
	node := &nodeAgent{
		cfg: config.NodeConfig{
			AppAPKDir: root,
		},
	}
	_, err := node.apkPathForSelectedApp(context.Background(), runtimeApp{
		PackageName: "projekt.launcher",
		DisplayName: "hyperion launcher",
	})
	if err == nil || !strings.Contains(err.Error(), "no trusted artifact") {
		t.Fatalf("apkPathForSelectedApp error = %v, want no trusted artifact", err)
	}
}

func TestNormalizeHomeActivity(t *testing.T) {
	component, err := normalizeHomeActivity("projekt.launcher", ".ProjektLauncher", true)
	if err != nil {
		t.Fatalf("normalizeHomeActivity returned error: %v", err)
	}
	if component != "projekt.launcher/projekt.launcher.ProjektLauncher" {
		t.Fatalf("component = %q, want normalized Hyperion component", component)
	}
}

func TestNormalizeHomeActivityRejectsWrongPackage(t *testing.T) {
	_, err := normalizeHomeActivity("projekt.launcher", "other.launcher/other.launcher.Home", true)
	if err == nil || !strings.Contains(err.Error(), "package must match") {
		t.Fatalf("normalizeHomeActivity error = %v, want package mismatch", err)
	}
}

func TestVerifyAPKFileRequiresPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.apk")
	if err := os.WriteFile(path, []byte("apk"), 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}
	if err := verifyAPKFile(path, ""); err == nil || !strings.Contains(err.Error(), "pin is required") {
		t.Fatalf("verifyAPKFile empty pin error = %v, want required pin", err)
	}
}

func TestVerifyAPKFileRejectsHashMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.apk")
	if err := os.WriteFile(path, []byte("apk"), 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}
	if err := verifyAPKFile(path, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("verifyAPKFile mismatch error = %v, want hash mismatch", err)
	}
}
