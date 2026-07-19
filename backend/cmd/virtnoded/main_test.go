package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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

func TestExtractAPKMExtractsBoundedRegularAPKFiles(t *testing.T) {
	apkmPath := writeTestAPKM(t, map[string]string{
		"base.apk":          "base",
		"splits/config.apk": "split",
		"metadata.json":     "ignored",
	})
	extractDir := filepath.Join(t.TempDir(), "extracted")
	paths, err := extractAPKMWithLimits(apkmPath, extractDir, 2, 8, 9)
	if err != nil {
		t.Fatalf("extractAPKMWithLimits: %v", err)
	}
	if len(paths) != 2 || filepath.Base(paths[0]) != "base.apk" || filepath.Base(paths[1]) != "config.apk" {
		t.Fatalf("extracted paths = %v, want sorted base.apk and config.apk", paths)
	}
}

func TestExtractAPKMRejectsDuplicateFlattenedNamesAndCleansOutput(t *testing.T) {
	apkmPath := writeTestAPKM(t, map[string]string{
		"one/base.apk": "one",
		"two/base.apk": "two",
	})
	extractDir := filepath.Join(t.TempDir(), "extracted")
	_, err := extractAPKMWithLimits(apkmPath, extractDir, 2, 8, 16)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("extractAPKMWithLimits error = %v, want duplicate-name rejection", err)
	}
	if _, statErr := os.Stat(extractDir); !os.IsNotExist(statErr) {
		t.Fatalf("failed extraction directory still exists: %v", statErr)
	}
}

func TestExtractAPKMRejectsFileAndTotalExpansionLimits(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		maxFiles  int
		maxFile   int64
		maxTotal  int64
		wantError string
	}{
		{name: "file count", files: map[string]string{"a.apk": "a", "b.apk": "b"}, maxFiles: 1, maxFile: 8, maxTotal: 16, wantError: "more than"},
		{name: "single file", files: map[string]string{"a.apk": "12345"}, maxFiles: 1, maxFile: 4, maxTotal: 8, wantError: "entry"},
		{name: "total", files: map[string]string{"a.apk": "123", "b.apk": "456"}, maxFiles: 2, maxFile: 4, maxTotal: 5, wantError: "total"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apkmPath := writeTestAPKM(t, tt.files)
			_, err := extractAPKMWithLimits(apkmPath, filepath.Join(t.TempDir(), "extracted"), tt.maxFiles, tt.maxFile, tt.maxTotal)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("extractAPKMWithLimits error = %v, want %q rejection", err, tt.wantError)
			}
		})
	}
}

func TestExtractAPKMRejectsSpecialFileEntry(t *testing.T) {
	root := t.TempDir()
	apkmPath := filepath.Join(root, "special.apkm")
	archive, err := os.Create(apkmPath)
	if err != nil {
		t.Fatalf("create APKM: %v", err)
	}
	writer := zip.NewWriter(archive)
	header := &zip.FileHeader{Name: "base.apk", Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("create symlink entry: %v", err)
	}
	if _, err := entry.Write([]byte("target.apk")); err != nil {
		t.Fatalf("write symlink entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP writer: %v", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close APKM: %v", err)
	}

	_, err = extractAPKMWithLimits(apkmPath, filepath.Join(root, "extracted"), 1, 16, 16)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("extractAPKMWithLimits error = %v, want special-file rejection", err)
	}
}

func writeTestAPKM(t *testing.T, files map[string]string) string {
	t.Helper()
	apkmPath := filepath.Join(t.TempDir(), "test.apkm")
	archive, err := os.Create(apkmPath)
	if err != nil {
		t.Fatalf("create APKM: %v", err)
	}
	writer := zip.NewWriter(archive)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create APKM entry %q: %v", name, err)
		}
		if _, err := entry.Write([]byte(files[name])); err != nil {
			t.Fatalf("write APKM entry %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP writer: %v", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close APKM: %v", err)
	}
	return apkmPath
}

func TestContainerUsesExpectedRuntimeNetworkRejectsAdditionalSharedBridge(t *testing.T) {
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "per-runtime")
	node := &nodeAgent{cfg: config.NodeConfig{DockerNetworkName: "virtroid-guests"}}
	runtimeID := "11111111-1111-1111-1111-111111111111"
	expected, err := node.runtimeDockerNetworkName(runtimeID)
	if err != nil {
		t.Fatalf("runtimeDockerNetworkName: %v", err)
	}
	inspect := dockerInspectResponse{}
	inspect.NetworkSettings.Networks = map[string]struct {
		IPAddress string `json:"IPAddress"`
		Gateway   string `json:"Gateway"`
	}{
		expected:          {},
		"virtroid-guests": {},
	}
	usesExpected, err := node.containerUsesExpectedRuntimeNetwork(runtimeID, inspect)
	if err != nil {
		t.Fatalf("containerUsesExpectedRuntimeNetwork: %v", err)
	}
	if usesExpected {
		t.Fatal("runtime attached to both isolated and shared networks was accepted")
	}
}

func TestCreateContainerUsesBoundedLocalLogging(t *testing.T) {
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "shared")
	t.Setenv("NODE_RUNTIME_IMAGE", "redroid/redroid:test@sha256:"+strings.Repeat("a", 64))
	t.Setenv("NODE_REQUIRE_DIGESTED_RUNTIME_IMAGE", "true")

	node := &nodeAgent{cfg: config.NodeConfig{
		DockerNetworkName: "virtroid-test",
		RuntimeRoot:       t.TempDir(),
	}}
	var requestBody map[string]any
	node.docker = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/containers/create" {
			t.Fatalf("Docker request = %s %s, want POST /containers/create", req.Method, req.URL.Path)
		}
		if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode Docker create request: %v", err)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}

	err := node.createContainer(context.Background(), "virtroid-runtime-test", runtimeAssignment{
		ID:         "11111111-1111-1111-1111-111111111111",
		WidthPx:    720,
		HeightPx:   1280,
		DensityDpi: 320,
	}, filepath.Join(t.TempDir(), "data"), 5555, sessionPersona{})
	if err != nil {
		t.Fatalf("createContainer: %v", err)
	}
	hostConfig, ok := requestBody["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("HostConfig = %#v, want object", requestBody["HostConfig"])
	}
	logConfig, ok := hostConfig["LogConfig"].(map[string]any)
	if !ok || logConfig["Type"] != "local" {
		t.Fatalf("LogConfig = %#v, want local driver", hostConfig["LogConfig"])
	}
	options, ok := logConfig["Config"].(map[string]any)
	if !ok || options["max-size"] != "10m" || options["max-file"] != "3" {
		t.Fatalf("LogConfig.Config = %#v, want bounded rotation", logConfig["Config"])
	}
}

func TestEnsureRuntimeNetworkReconnectsRecreatedNodeAgent(t *testing.T) {
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "per-runtime")
	t.Setenv("NODE_AGENT_CONTAINER_NAME", "virtnoded")
	runtimeID := "11111111-1111-1111-1111-111111111111"
	node := &nodeAgent{cfg: config.NodeConfig{DockerNetworkName: "virtroid-guests"}}
	networkName, err := node.runtimeDockerNetworkName(runtimeID)
	if err != nil {
		t.Fatalf("runtimeDockerNetworkName: %v", err)
	}
	requests := 0
	node.docker = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			payload, _ := json.Marshal(dockerNetworkInspectResponse{
				Name: networkName,
				Labels: map[string]string{
					"io.virtroid.managed": "true",
					"io.virtroid.runtime": runtimeID,
				},
				Containers: map[string]struct {
					Name string `json:"Name"`
				}{"guest-id": {Name: containerNameForRuntime(runtimeID)}},
			})
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(payload))), Header: make(http.Header)}, nil
		case 2:
			if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/connect") {
				t.Fatalf("second Docker request = %s %s, want network connect", req.Method, req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		default:
			t.Fatalf("unexpected Docker request %d: %s %s", requests, req.Method, req.URL.Path)
			return nil, nil
		}
	})}

	got, err := node.ensureRuntimeNetwork(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("ensureRuntimeNetwork: %v", err)
	}
	if got != networkName || requests != 2 {
		t.Fatalf("ensureRuntimeNetwork = %q with %d requests, want %q with inspect+connect", got, requests, networkName)
	}
}

func TestRemoveRuntimeNetworkRefusesUnmanagedCollision(t *testing.T) {
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "per-runtime")
	t.Setenv("NODE_AGENT_CONTAINER_NAME", "virtnoded")
	runtimeID := "11111111-1111-1111-1111-111111111111"
	node := &nodeAgent{cfg: config.NodeConfig{DockerNetworkName: "virtroid-guests"}}
	requests := 0
	node.docker = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		payload := `{"Name":"collision","Labels":{"io.virtroid.managed":"false"},"Containers":{}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})}

	err := node.removeRuntimeNetwork(context.Background(), runtimeID)
	if err == nil || !strings.Contains(err.Error(), "refusing to remove unmanaged") {
		t.Fatalf("removeRuntimeNetwork error = %v, want unmanaged collision refusal", err)
	}
	if requests != 1 {
		t.Fatalf("removeRuntimeNetwork made %d Docker requests after unmanaged collision, want inspect only", requests)
	}
}
