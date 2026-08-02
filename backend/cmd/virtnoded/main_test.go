package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/callbackauth"
	"virtroid/backend/internal/config"
	"virtroid/backend/internal/nodeauth"
)

func TestRequireControlPlaneCallbackVerifiesBodyAndRejectsReplay(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	publicKey, err := nodeauth.PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("PublicKeyMaterial: %v", err)
	}
	node := &nodeAgent{cfg: config.NodeConfig{ControlPlaneCallbackPublicKey: publicKey}}
	body := []byte(`{"runtime_id":"runtime-1"}`)
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/viewer/prepare", bytes.NewReader(body))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	if err := callbackauth.ApplySignedHeaders(req, privateKey, body, timestamp, "0123456789abcdef"); err != nil {
		t.Fatalf("ApplySignedHeaders: %v", err)
	}
	if !node.requireControlPlaneCallback(response, req) {
		t.Fatalf("requireControlPlaneCallback rejected valid callback: status=%d body=%s", response.Code, response.Body.String())
	}
	restoredBody, err := io.ReadAll(req.Body)
	if err != nil || !bytes.Equal(restoredBody, body) {
		t.Fatalf("restored callback body = %q, err=%v", restoredBody, err)
	}

	replay := httptest.NewRequest(http.MethodPost, "/api/v1/internal/viewer/prepare", bytes.NewReader(body))
	if err := callbackauth.ApplySignedHeaders(replay, privateKey, body, timestamp, "0123456789abcdef"); err != nil {
		t.Fatalf("ApplySignedHeaders replay: %v", err)
	}
	replayResponse := httptest.NewRecorder()
	if node.requireControlPlaneCallback(replayResponse, replay) || replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replayed callback accepted: status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
}

func TestRecoverNodeHTTPContainsHandlerPanic(t *testing.T) {
	handler := recoverNodeHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("sensitive panic detail")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "sensitive panic detail") {
		t.Fatalf("panic detail leaked in response: %q", response.Body.String())
	}
}

func TestSafeRuntimeImportFilenameRejectsPackageArtifacts(t *testing.T) {
	for _, name := range []string{"payload.apk", "bundle.APKS", "classes.dex", "client.jar"} {
		if _, err := safeRuntimeImportFilename(name); err == nil {
			t.Fatalf("safeRuntimeImportFilename(%q) accepted an installable artifact", name)
		}
	}
	got, err := safeRuntimeImportFilename("holiday photo (1).jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got != "holiday photo (1).jpg" {
		t.Fatalf("normalized name = %q", got)
	}
}

func TestValidateRuntimeImportContentRejectsRenamedAndroidPackage(t *testing.T) {
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	manifest, err := writer.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Write([]byte("binary manifest")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeImportContent(payload.Bytes()); err == nil {
		t.Fatal("accepted an Android package whose filename could be disguised")
	}
	if err := validateRuntimeImportContent([]byte("ordinary document")); err != nil {
		t.Fatalf("ordinary document rejected: %v", err)
	}
}

func TestValidateRuntimeImportContentRejectsExcessiveZipEntries(t *testing.T) {
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	for index := 0; index <= maxRuntimeImportZipEntries; index++ {
		entry, err := writer.Create(fmt.Sprintf("entry-%04d.txt", index))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeImportContent(payload.Bytes()); err == nil {
		t.Fatal("accepted an archive with excessive entry cardinality")
	}
}

func TestNodeCallbackBodyLimitIsRouteSpecific(t *testing.T) {
	if got := nodeCallbackBodyLimit("/api/v1/internal/runtimes/id/files"); got != maxRuntimeFileImportBytes {
		t.Fatalf("file callback limit = %d", got)
	}
	if got := nodeCallbackBodyLimit("/api/v1/internal/runtimes/id/photos"); got != maxRuntimePhotoImportBytes {
		t.Fatalf("photo callback limit = %d", got)
	}
	if got := nodeCallbackBodyLimit("/api/v1/internal/viewer/prepare"); got != 2<<20 {
		t.Fatalf("default callback limit = %d", got)
	}
}

func TestParseContainerOwnership(t *testing.T) {
	uid, gid, err := parseContainerOwnership("10079:1023\n")
	if err != nil {
		t.Fatal(err)
	}
	if uid != 10079 || gid != 1023 {
		t.Fatalf("ownership = %d:%d", uid, gid)
	}
	for _, invalid := range []string{"", "10079", "root:media_rw", "-1:1023", "10079:-1", "10079:1023:7"} {
		if _, _, err := parseContainerOwnership(invalid); err == nil {
			t.Fatalf("accepted invalid ownership %q", invalid)
		}
	}
}

func TestRuntimePhotoValidation(t *testing.T) {
	var photo bytes.Buffer
	if err := jpeg.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimePhotoContent(photo.Bytes()); err != nil {
		t.Fatalf("valid JPEG rejected: %v", err)
	}
	if got, err := safeRuntimePhotoFilename("Virtroid-20260802.jpg"); err != nil || got != "Virtroid-20260802.jpg" {
		t.Fatalf("safe photo name = %q, %v", got, err)
	}
	for _, name := range []string{"../capture.jpg", "capture.png", "capture.jpg/other"} {
		if _, err := safeRuntimePhotoFilename(name); err == nil {
			t.Fatalf("unsafe photo name %q was accepted", name)
		}
	}
}

func TestRelaySlotsRejectExcessConcurrency(t *testing.T) {
	node := &nodeAgent{relaySlots: make(chan struct{}, 1)}
	if !node.acquireRelaySlot() {
		t.Fatal("first relay slot was rejected")
	}
	if node.acquireRelaySlot() {
		t.Fatal("relay slot limit was not enforced")
	}
	node.releaseRelaySlot()
	if !node.acquireRelaySlot() {
		t.Fatal("released relay slot was not reusable")
	}
	node.releaseRelaySlot()
}

func TestRunNodeBackgroundIterationContainsPanic(t *testing.T) {
	runNodeBackgroundIteration("test", func() {
		panic("background failure")
	})
}

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

func TestViewerServiceReuseRequiresEncryptedAndPlaintextListeners(t *testing.T) {
	condition := viewerServiceReuseListenerCondition()
	for _, port := range []int{encryptedViewerPort, scrcpyPlainPort} {
		if !strings.Contains(condition, fmt.Sprintf("grep -q ':%d'", port)) {
			t.Fatalf("viewer reuse condition %q does not require listener %d", condition, port)
		}
	}
	if count := strings.Count(condition, "ss -ltn"); count != 2 {
		t.Fatalf("viewer reuse condition checks %d listeners, want 2: %q", count, condition)
	}
}

func TestAndroidInteractiveProbeAcceptsFocusedSecondaryDisplay(t *testing.T) {
	if strings.Contains(androidInteractiveProbeScript, `grep -m 1 "mCurrentFocus="`) {
		t.Fatal("interactive probe stops at a null primary-display focus before checking later displays")
	}
	if count := strings.Count(androidInteractiveProbeScript, `grep -m 1 "mCurrentFocus=Window"`); count != 2 {
		t.Fatalf("interactive probe performs %d focused-window checks, want 2", count)
	}
	if count := strings.Count(androidInteractiveProbeScript, `grep -m 1 "mAwake=true"`); count != 2 {
		t.Fatalf("interactive probe performs %d awake-display checks, want 2", count)
	}
}

func TestViewerScriptUsesGuestScrcpyIPv6Loopback(t *testing.T) {
	if !strings.Contains(viewerScriptContent, `-upstream "[::1]:7007"`) {
		t.Fatal("viewer script must use the IPv6 loopback address exposed by the pinned ReDroid scrcpy listener")
	}
	if strings.Contains(viewerScriptContent, `-upstream "127.0.0.1:7007"`) {
		t.Fatal("viewer script must not use IPv4 for the pinned ReDroid scrcpy listener")
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

func TestADBShellArgsKeepsCompoundCommandInOneRemoteArgument(t *testing.T) {
	command := "pm path 'org.fdroid.basic' >/dev/null 2>&1 && echo installed || true"
	args := adbShellArgs("10.0.0.8:5555", command)
	want := []string{"-s", "10.0.0.8:5555", "shell", command}
	if len(args) != len(want) {
		t.Fatalf("adbShellArgs = %#v, want %#v", args, want)
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("adbShellArgs[%d] = %q, want %q", index, args[index], want[index])
		}
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

func TestStoragePreflightStatusReportsDeferredDeletionBacklog(t *testing.T) {
	status := storagePreflightStatus(blobPreflightReport{
		Store: blobStoreRenterd,
		OK:    true,
		Checks: []blobPreflightCheck{
			{Name: "pending_deletions", Status: "warn", Detail: "1 cleanup record remains"},
		},
	})
	if status != "degraded" {
		t.Fatalf("status = %q, want degraded", status)
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
	paths, err := extractAPKMWithLimits(apkmPath, extractDir, 2, 8, 8, 9)
	if err != nil {
		t.Fatalf("extractAPKMWithLimits: %v", err)
	}
	if len(paths) != 2 || filepath.Base(paths[0]) != "apk-000.apk" || filepath.Base(paths[1]) != "apk-001.apk" {
		t.Fatalf("extracted paths = %v, want deterministic generated names", paths)
	}
	for index, want := range []string{"base", "split"} {
		payload, err := os.ReadFile(paths[index])
		if err != nil {
			t.Fatalf("read extracted APK %d: %v", index, err)
		}
		if string(payload) != want {
			t.Fatalf("extracted APK %d = %q, want %q", index, payload, want)
		}
	}
}

func TestExtractAPKMGeneratedNamesAllowDuplicateUntrustedBasenames(t *testing.T) {
	apkmPath := writeTestAPKM(t, map[string]string{
		"one/base.apk": "one",
		"two/base.apk": "two",
	})
	extractDir := filepath.Join(t.TempDir(), "extracted")
	paths, err := extractAPKMWithLimits(apkmPath, extractDir, 2, 8, 8, 16)
	if err != nil {
		t.Fatalf("extractAPKMWithLimits: %v", err)
	}
	if len(paths) != 2 || filepath.Base(paths[0]) != "apk-000.apk" || filepath.Base(paths[1]) != "apk-001.apk" {
		t.Fatalf("extracted paths = %v, want unique generated names", paths)
	}
}

func TestExtractAPKMAllowsCaseDistinctCanonicalPaths(t *testing.T) {
	apkmPath := writeTestAPKMEntries(t, []testZipEntry{
		{name: "A.apk", body: "upper"},
		{name: "a.apk", body: "lower"},
	})
	extractDir := filepath.Join(t.TempDir(), "extracted")
	paths, err := extractAPKMWithLimits(apkmPath, extractDir, 2, 8, 8, 16)
	if err != nil {
		t.Fatalf("extractAPKMWithLimits: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("extracted paths = %v, want two case-distinct APK entries", paths)
	}
}

func TestExtractAPKMRejectsTooManyTotalArchiveEntries(t *testing.T) {
	apkmPath := writeTestAPKMEntries(t, []testZipEntry{
		{name: "base.apk", body: "apk"},
		{name: "icon.png", body: "icon"},
		{name: "metadata.json", body: "{}"},
	})
	extractDir := filepath.Join(t.TempDir(), "extracted")
	_, err := extractAPKMWithLimits(apkmPath, extractDir, 1, 2, 8, 8)
	if err == nil || !strings.Contains(err.Error(), "more than 2 archive entries") {
		t.Fatalf("extractAPKMWithLimits error = %v, want total archive-entry rejection", err)
	}
	if _, statErr := os.Stat(extractDir); !os.IsNotExist(statErr) {
		t.Fatalf("entry-count rejection created extraction directory: %v", statErr)
	}
}

func TestExtractAPKMRejectsUnderreportedCentralDirectoryCountBeforeParsing(t *testing.T) {
	apkmPath := writeTestAPKMEntries(t, []testZipEntry{
		{name: "base.apk", body: "apk"},
		{name: "icon.png", body: "icon"},
		{name: "metadata.json", body: "{}"},
	})
	payload, err := os.ReadFile(apkmPath)
	if err != nil {
		t.Fatalf("read APKM: %v", err)
	}
	directoryEndIndex := bytes.LastIndex(payload, []byte{'P', 'K', 0x05, 0x06})
	if directoryEndIndex < 0 {
		t.Fatal("test APKM is missing its central-directory end record")
	}
	// archive/zip parses central headers before checking this declared count.
	// Underreport it to prove our bounded preflight counts the actual headers.
	binary.LittleEndian.PutUint16(payload[directoryEndIndex+8:directoryEndIndex+10], 1)
	binary.LittleEndian.PutUint16(payload[directoryEndIndex+10:directoryEndIndex+12], 1)
	if err := os.WriteFile(apkmPath, payload, 0o600); err != nil {
		t.Fatalf("rewrite APKM: %v", err)
	}

	extractDir := filepath.Join(t.TempDir(), "extracted")
	_, err = extractAPKMWithLimits(apkmPath, extractDir, 1, 2, 8, 8)
	if err == nil || !strings.Contains(err.Error(), "more than 2 archive entries") {
		t.Fatalf("extractAPKMWithLimits error = %v, want pre-parse entry-count rejection", err)
	}
	if _, statErr := os.Stat(extractDir); !os.IsNotExist(statErr) {
		t.Fatalf("entry-count rejection created extraction directory: %v", statErr)
	}
}

func TestExtractAPKMRejectsRecordsOnDiskOnlyZIP64Sentinel(t *testing.T) {
	apkmPath := writeTestAPKMEntries(t, []testZipEntry{{name: "base.apk", body: "apk"}})
	payload, err := os.ReadFile(apkmPath)
	if err != nil {
		t.Fatalf("read APKM: %v", err)
	}
	directoryEndIndex := bytes.LastIndex(payload, []byte{'P', 'K', 0x05, 0x06})
	if directoryEndIndex < 0 {
		t.Fatal("test APKM is missing its central-directory end record")
	}
	// archive/zip does not treat the records-on-this-disk field alone as a
	// ZIP64 trigger. The bounded preflight must therefore reject this mismatch
	// under the same classic interpretation, before archive/zip allocates.
	binary.LittleEndian.PutUint16(payload[directoryEndIndex+8:directoryEndIndex+10], 0xffff)
	if err := os.WriteFile(apkmPath, payload, 0o600); err != nil {
		t.Fatalf("rewrite APKM: %v", err)
	}

	extractDir := filepath.Join(t.TempDir(), "extracted")
	_, err = extractAPKMWithLimits(apkmPath, extractDir, 1, 2, 8, 8)
	if err == nil || !strings.Contains(err.Error(), "multi-disk") {
		t.Fatalf("extractAPKMWithLimits error = %v, want classic-record mismatch rejection", err)
	}
	if _, statErr := os.Stat(extractDir); !os.IsNotExist(statErr) {
		t.Fatalf("record-mismatch rejection created extraction directory: %v", statErr)
	}
}

func TestExtractAPKMRejectsDuplicateCanonicalPathAndCleansOutput(t *testing.T) {
	apkmPath := writeTestAPKMEntries(t, []testZipEntry{
		{name: "base.apk", body: "one"},
		{name: "base.apk", body: "two"},
	})
	extractDir := filepath.Join(t.TempDir(), "extracted")
	_, err := extractAPKMWithLimits(apkmPath, extractDir, 2, 8, 8, 16)
	if err == nil || !strings.Contains(err.Error(), "duplicate archive path") {
		t.Fatalf("extractAPKMWithLimits error = %v, want duplicate path rejection", err)
	}
	if _, statErr := os.Stat(extractDir); !os.IsNotExist(statErr) {
		t.Fatalf("failed extraction directory still exists: %v", statErr)
	}
}

func TestExtractAPKMRejectsNonCanonicalAndEscapingPaths(t *testing.T) {
	tests := []string{
		"/absolute.apk",
		"../outside.apk",
		"../extracted-evil/base.apk",
		`..\outside.apk`,
		"C:/absolute.apk",
		"./base.apk",
		"splits/../base.apk",
		"splits//base.apk",
		strings.Repeat("a", maxArchivePathBytes-3) + ".apk",
	}
	for _, archivePath := range tests {
		t.Run(strings.ReplaceAll(archivePath, "/", "_"), func(t *testing.T) {
			apkmPath := writeTestAPKMEntries(t, []testZipEntry{{name: archivePath, body: "apk"}})
			extractDir := filepath.Join(t.TempDir(), "extracted")
			_, err := extractAPKMWithLimits(apkmPath, extractDir, 1, 8, 8, 8)
			if err == nil || !strings.Contains(err.Error(), "invalid path") {
				t.Fatalf("extractAPKMWithLimits(%q) error = %v, want invalid path rejection", archivePath, err)
			}
			if _, statErr := os.Stat(extractDir); !os.IsNotExist(statErr) {
				t.Fatalf("failed extraction directory still exists: %v", statErr)
			}
		})
	}
}

func TestExtractAPKMDoesNotFollowPreexistingExtractionSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	extractDir := filepath.Join(root, "extracted")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatalf("create outside directory: %v", err)
	}
	if err := os.Symlink(outside, extractDir); err != nil {
		t.Fatalf("create extraction symlink: %v", err)
	}
	apkmPath := writeTestAPKM(t, map[string]string{"base.apk": "apk"})
	paths, err := extractAPKMWithLimits(apkmPath, extractDir, 1, 8, 8, 8)
	if err != nil {
		t.Fatalf("extractAPKMWithLimits: %v", err)
	}
	if len(paths) != 1 || filepath.Dir(paths[0]) != extractDir {
		t.Fatalf("extracted paths = %v, want path under replacement directory", paths)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("extraction followed preexisting symlink: %v", entries)
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
			_, err := extractAPKMWithLimits(apkmPath, filepath.Join(t.TempDir(), "extracted"), tt.maxFiles, 8, tt.maxFile, tt.maxTotal)
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
	header := &zip.FileHeader{Name: "metadata.json", Method: zip.Store}
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

	_, err = extractAPKMWithLimits(apkmPath, filepath.Join(root, "extracted"), 1, 8, 16, 16)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("extractAPKMWithLimits error = %v, want special-file rejection", err)
	}
}

func writeTestAPKM(t *testing.T, files map[string]string) string {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]testZipEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, testZipEntry{name: name, body: files[name]})
	}
	return writeTestAPKMEntries(t, entries)
}

type testZipEntry struct {
	name string
	body string
	mode os.FileMode
}

func writeTestAPKMEntries(t *testing.T, entries []testZipEntry) string {
	t.Helper()
	apkmPath := filepath.Join(t.TempDir(), "test.apkm")
	archive, err := os.Create(apkmPath)
	if err != nil {
		t.Fatalf("create APKM: %v", err)
	}
	writer := zip.NewWriter(archive)
	for _, archiveEntry := range entries {
		header := &zip.FileHeader{Name: archiveEntry.name, Method: zip.Store}
		if archiveEntry.mode != 0 {
			header.SetMode(archiveEntry.mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create APKM entry %q: %v", archiveEntry.name, err)
		}
		if _, err := entry.Write([]byte(archiveEntry.body)); err != nil {
			t.Fatalf("write APKM entry %q: %v", archiveEntry.name, err)
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
