package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/nodeauth"
)

var errContainerNotFound = errors.New("container not found")
var errInstalledPackageMissing = errors.New("installed artifact did not provide the expected package")
var appPackageNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var digestedImageReferencePattern = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-fA-F]{64}$`)
var dockerNetworkNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

const (
	maxAPKMFiles          = 64
	maxAPKMArchiveEntries = 256
	maxAPKMFileBytes      = int64(512 * 1024 * 1024)
	maxAPKMTotalBytes     = int64(1024 * 1024 * 1024)

	zipDirectoryHeaderSignature = 0x02014b50
	zipDirectoryEndSignature    = 0x06054b50
	zipDirectoryHeaderLength    = 46
	zipDirectoryEndLength       = 22
	zipMaximumCommentLength     = 1<<16 - 1
)

var builtInTrustedAppCatalog = map[string]runtimeApp{
	"org.fdroid.basic": {
		PackageName:  "org.fdroid.basic",
		DisplayName:  "F-Droid Basic",
		APKURL:       "https://f-droid.org/repo/org.fdroid.basic_2000009.apk",
		APKSHA256:    "1aa1931bf61e11382c2b225581d4adcd2d9803697a144a84c6eb4db04f67cafb",
		APKSizeBytes: 11391212,
	},
}

//go:embed assets/scrcpy-server.jar
var scrcpyServerJar []byte

const scrcpyServerMountPath = "/opt/virtroid/scrcpy-server.jar"
const viewerCryptMountPath = "/opt/virtroid/virtroid-viewercrypt"
const viewerScriptMountPath = "/vendor/bin/virtroid-viewer.sh"
const viewerInitMountPath = "/vendor/etc/init/virtroid-viewer.rc"
const viewerPublicKeyPath = "/data/local/tmp/virtroid-viewer-public-key"

const (
	scrcpyPlainPort     = 7007
	encryptedViewerPort = 7017
)

const viewerScriptContent = `#!/system/bin/sh
set -eu

PATH=/product/bin:/apex/com.android.runtime/bin:/apex/com.android.art/bin:/system_ext/bin:/system/bin:/system/xbin:/odm/bin:/vendor/bin:/vendor/xbin
SRC=/opt/virtroid/scrcpy-server.jar
DST=/data/local/tmp/scrcpy-server.jar
LOG=/data/local/tmp/virtroid-viewer.log
VIEWERCRYPT=/opt/virtroid/virtroid-viewercrypt
PUBLIC_KEY=/data/local/tmp/virtroid-viewer-public-key
IP=$(getprop virtroid.viewer.client_ip)
SIZE=$(getprop virtroid.viewer.max_size)
BITRATE=$(getprop virtroid.viewer.bit_rate)

if [ -z "$IP" ]; then
  IP=127.0.0.1
fi
if [ -z "$SIZE" ]; then
  SIZE=1600
fi
if [ -z "$BITRATE" ]; then
  BITRATE=4000000
fi
exec >>"$LOG" 2>&1
echo "viewer-start $(date +%s) ip=$IP size=$SIZE bitrate=$BITRATE"
rm -f "$DST"
rm -f "$PUBLIC_KEY"
cp "$SRC" "$DST"
chown shell:shell "$DST" || true
chmod 0644 "$DST" || true
restorecon "$DST" >/dev/null 2>&1 || true
env CLASSPATH="$DST" PATH="$PATH" app_process / org.server.scrcpy.Server "/$IP" "$SIZE" "$BITRATE" &
SERVER_PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
  if ss -ltn 2>/dev/null | grep -q ':7007'; then
    break
  fi
  sleep 1
done
if ! ss -ltn 2>/dev/null | grep -q ':7007'; then
  echo "scrcpy plaintext listener did not open"
  kill "$SERVER_PID" >/dev/null 2>&1 || true
  exit 1
fi
chmod 0755 "$VIEWERCRYPT" >/dev/null 2>&1 || true
"$VIEWERCRYPT" -listen "127.0.0.1:7017" -upstream "127.0.0.1:7007" -public-key-file "$PUBLIC_KEY"
STATUS=$?
kill "$SERVER_PID" >/dev/null 2>&1 || true
exit "$STATUS"
`

const viewerInitContent = `service virtroid_viewer /system/bin/sh /vendor/bin/virtroid-viewer.sh
    class late_start
    user shell
    group shell log graphics input audio video inet
    oneshot
    disabled
    seclabel u:r:shell:s0
`

const androidInteractiveProbeScript = `PATH=/product/bin:/apex/com.android.runtime/bin:/apex/com.android.art/bin:/system_ext/bin:/system/bin:/system/xbin:/odm/bin:/vendor/bin:/vendor/xbin:/bin
set +e

not_ready() {
  echo "virtroid_ready=0 reason=$1"
  exit 0
}

sys_boot="$(getprop sys.boot_completed 2>/dev/null)"
dev_boot="$(getprop dev.bootcomplete 2>/dev/null)"
if [ "$sys_boot" != "1" ] && [ "$dev_boot" != "1" ]; then
  not_ready booting
fi

for service_name in activity package window; do
  service_output="$(service check "$service_name" 2>&1)"
  echo "service_${service_name}=${service_output}"
  echo "$service_output" | grep -q "found" || not_ready "${service_name}_service_unavailable"
done

focus_line="$(dumpsys window 2>/dev/null | grep -m 1 "mCurrentFocus=" || true)"
awake_line="$(dumpsys window 2>/dev/null | grep -m 1 "mAwake=" || true)"
echo "$focus_line"
echo "$awake_line"
if echo "$focus_line" | grep -q "mCurrentFocus=Window" && echo "$awake_line" | grep -q "mAwake=true"; then
  echo "virtroid_ready=1 reason=focused"
  exit 0
fi

svc power stayon true >/dev/null 2>&1 || true
input keyevent 224 >/dev/null 2>&1 || input keyevent KEYCODE_WAKEUP >/dev/null 2>&1 || true
input keyevent 82 >/dev/null 2>&1 || true
wm dismiss-keyguard >/dev/null 2>&1 || true

home_output="$(am start -W -a android.intent.action.MAIN -c android.intent.category.HOME 2>&1)"
home_status=$?
echo "$home_output"
echo "$home_output" | grep -qi "Too early" && not_ready activity_manager_starting
if [ "$home_status" -ne 0 ]; then
  not_ready home_start_failed
fi

sleep 1
focus_line="$(dumpsys window 2>/dev/null | grep -m 1 "mCurrentFocus=" || true)"
awake_line="$(dumpsys window 2>/dev/null | grep -m 1 "mAwake=" || true)"
echo "$focus_line"
echo "$awake_line"
if echo "$focus_line" | grep -q "mCurrentFocus=Window" && echo "$awake_line" | grep -q "mAwake=true"; then
  echo "virtroid_ready=1 reason=home_started"
  exit 0
fi

not_ready no_focused_window
`

type runtimeAssignment struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	Status              string       `json:"status"`
	DesiredState        string       `json:"desired_state"`
	ConnectionStatus    string       `json:"connection_status"`
	PersonaVersion      int          `json:"persona_version"`
	AndroidImage        string       `json:"android_image"`
	AndroidVersion      string       `json:"android_version"`
	WidthPx             int          `json:"width_px"`
	HeightPx            int          `json:"height_px"`
	DensityDpi          int          `json:"density_dpi"`
	BlobAutoSnapshot    bool         `json:"blob_auto_snapshot"`
	BlobStoreKind       *string      `json:"blob_store_kind"`
	BlobManifestJSON    *string      `json:"blob_manifest_json"`
	ADBPort             *int         `json:"adb_port"`
	ViewerPort          *int         `json:"viewer_port"`
	WipeRequested       bool         `json:"wipe_requested"`
	CleanupPending      bool         `json:"cleanup_pending"`
	OperationGeneration int64        `json:"operation_generation"`
	LastError           *string      `json:"last_error"`
	SelectedApps        []runtimeApp `json:"selected_apps"`
}

type runtimeApp struct {
	PackageName  string `json:"package_name"`
	DisplayName  string `json:"display_name"`
	APKURL       string `json:"apk_url"`
	APKSHA256    string `json:"apk_sha256"`
	APKSizeBytes int64  `json:"apk_size_bytes"`
	Artifact     string `json:"-"`
	InstallMode  string `json:"-"`
	SetAsHome    bool   `json:"-"`
	HomeActivity string `json:"-"`
}

type appInstallManifest struct {
	Version         int                `json:"version"`
	DefaultPackages []string           `json:"default_packages"`
	Apps            []appManifestEntry `json:"apps"`
}

type appManifestEntry struct {
	PackageName  string `json:"package_name"`
	DisplayName  string `json:"display_name"`
	Artifact     string `json:"artifact"`
	InstallMode  string `json:"install_mode"`
	SHA256       string `json:"sha256"`
	SourceURL    string `json:"source_url"`
	APKSizeBytes int64  `json:"apk_size_bytes"`
	Default      bool   `json:"default"`
	SetAsHome    bool   `json:"set_as_home"`
	HomeActivity string `json:"home_activity"`
}

type trustedAppCatalog struct {
	apps            map[string]runtimeApp
	defaultPackages []string
}

type relayTarget struct {
	SessionID  string `json:"session_id"`
	RuntimeID  string `json:"runtime_id"`
	HostID     string `json:"host_id"`
	ViewerPort int    `json:"viewer_port"`
}

type dockerInspectResponse struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running   bool   `json:"Running"`
		Status    string `json:"Status"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
		Networks  map[string]struct {
			IPAddress string `json:"IPAddress"`
			Gateway   string `json:"Gateway"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type dockerNetworkInspectResponse struct {
	Name       string            `json:"Name"`
	Labels     map[string]string `json:"Labels"`
	Containers map[string]struct {
		Name string `json:"Name"`
	} `json:"Containers"`
}

const (
	viewerPrepareTimeout = 90 * time.Second
	viewerDefaultMaxSize = 720
	viewerMinMaxSize     = 240
	viewerMaxMaxSize     = 1600
	viewerDefaultBitRate = 8_000_000
	viewerMinBitRate     = 250_000
	viewerMaxBitRate     = 32_000_000
	viewerPrewarmBitRate = 4_000_000
)

type nodeAgent struct {
	cfg                 config.NodeConfig
	controlPlane        *http.Client
	docker              *http.Client
	nodePrivateKey      *ecdsa.PrivateKey
	nodePublicKey       string
	blobPreflightMu     sync.Mutex
	blobPreflightReport blobPreflightReport
	blobPreflightAt     time.Time
	runtimeBlobKeyMu    sync.Mutex
	runtimeBlobKeys     map[string][]byte
}

type runtimeContainerResources struct {
	MemoryBytes int64
	NanoCPUs    int64
	PidsLimit   int64
	ShmBytes    int64
}

func main() {
	cfg := config.LoadNode()
	nodePrivateKey, nodePublicKey, err := nodeauth.LoadPrivateKey(cfg.PrivateKey)
	if err != nil {
		log.Fatalf("load node private key: %v", err)
	}
	if nodePrivateKey == nil {
		log.Printf("node private key is not configured; production control planes will reject unsigned node requests")
	}
	node := &nodeAgent{
		cfg:            cfg,
		nodePrivateKey: nodePrivateKey,
		nodePublicKey:  nodePublicKey,
		controlPlane: &http.Client{
			Timeout: 20 * time.Second,
		},
		docker: dockerHTTPClient(),
	}
	defer node.clearAllCachedRuntimeBlobKeys()
	if os.Getenv("NODE_BLOB_SMOKE_TEST") == "1" {
		if err := node.runBlobSmokeTest(context.Background()); err != nil {
			log.Fatalf("blob smoke test failed: %v", err)
		}
		log.Printf("blob smoke test ok: store=%s", cfg.BlobStoreKind)
		return
	}
	if os.Getenv("NODE_BLOB_PREFLIGHT") == "1" {
		report := node.runBlobPreflight(context.Background())
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			log.Fatalf("encode blob preflight report: %v", err)
		}
		if !report.OK {
			os.Exit(1)
		}
		return
	}

	if err := os.MkdirAll(cfg.RuntimeRoot, 0o755); err != nil {
		log.Fatalf("prepare runtime root: %v", err)
	}
	if err := node.ensureAssets(); err != nil {
		log.Fatalf("prepare node assets: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"role": "virtnoded",
			"id":   cfg.NodeID,
		})
	})
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, node.capabilities(r.Context(), false))
	})
	mux.HandleFunc("POST /api/v1/internal/viewer/prepare", node.handlePrepareViewer)
	mux.HandleFunc("POST /api/v1/internal/blob-key/verify", node.handleVerifyBlobKeyEnvelope)
	mux.HandleFunc("CONNECT /api/v1/relay/{id}", node.handleRelaySession)
	mux.HandleFunc("GET /api/v1/relay/{id}", node.handleRelaySession)

	server := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go node.heartbeatLoop(ctx)
	go node.reconcileLoop(ctx)
	go func() {
		log.Printf("virtnoded listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (n *nodeAgent) signControlPlaneRequest(req *http.Request, body []byte, includeRegistration bool) error {
	if n.cfg.SharedSecret != "" {
		req.Header.Set("X-Virtroid-Node-Secret", n.cfg.SharedSecret)
	}
	if n.nodePrivateKey == nil {
		return nil
	}

	nonceBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		return err
	}
	registrationSecret := ""
	if includeRegistration {
		registrationSecret = n.cfg.RegistrationSecret
	}
	return nodeauth.ApplySignedHeaders(
		req,
		n.nodePrivateKey,
		n.cfg.NodeID,
		body,
		n.nodePublicKey,
		registrationSecret,
		strconv.FormatInt(time.Now().Unix(), 10),
		hex.EncodeToString(nonceBytes),
	)
}

func (n *nodeAgent) assetDir() string {
	return filepath.Join(filepath.Dir(n.cfg.RuntimeRoot), "assets")
}

func (n *nodeAgent) scrcpyServerPath() string {
	return filepath.Join(n.assetDir(), "scrcpy-server.jar")
}

func (n *nodeAgent) viewerCryptPath() string {
	return filepath.Join(n.assetDir(), "virtroid-viewercrypt")
}

func (n *nodeAgent) viewerScriptPath() string {
	return filepath.Join(n.assetDir(), "virtroid-viewer.sh")
}

func (n *nodeAgent) viewerInitPath() string {
	return filepath.Join(n.assetDir(), "virtroid-viewer.rc")
}

func (n *nodeAgent) runBlobSmokeTest(ctx context.Context) error {
	store, err := n.blobStore(n.cfg.BlobStoreKind)
	if err != nil {
		return err
	}

	root, err := os.MkdirTemp("", "virtroid-blob-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	sourceDir := filepath.Join(root, "source")
	restoreDir := filepath.Join(root, "restore")
	if err := os.MkdirAll(filepath.Join(sourceDir, "misc", "profile"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "settings.json"), []byte(`{"ok":true,"purpose":"blob-smoke"}`), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "misc", "profile", "state.txt"), []byte("virtroid blob smoke\n"), 0o600); err != nil {
		return err
	}

	masterKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		return err
	}
	runtimeID := "blob-smoke-" + time.Now().UTC().Format("20060102T150405Z")

	manifest, err := store.persistFromDir(ctx, runtimeID, sourceDir, masterKey)
	if err != nil {
		return err
	}
	if manifest == nil || len(manifest.Chunks) == 0 {
		return errors.New("blob smoke manifest has no chunks")
	}
	defer func() {
		if cleaner, ok := store.(interface {
			deleteManifest(context.Context, *blobManifest) error
		}); ok {
			_ = cleaner.deleteManifest(context.Background(), manifest)
			return
		}
		_ = store.clearRuntime(context.Background(), runtimeID)
	}()

	if err := store.restoreToDir(ctx, runtimeID, manifest, restoreDir, masterKey); err != nil {
		return err
	}
	return compareDirectoryContents(sourceDir, restoreDir)
}

func compareDirectoryContents(sourceDir, restoreDir string) error {
	sourceFiles := map[string][]byte{}
	if err := filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relativePath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sourceFiles[filepath.ToSlash(relativePath)] = payload
		return nil
	}); err != nil {
		return err
	}

	restoredFiles := map[string][]byte{}
	if err := filepath.WalkDir(restoreDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relativePath, err := filepath.Rel(restoreDir, path)
		if err != nil {
			return err
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		restoredFiles[filepath.ToSlash(relativePath)] = payload
		return nil
	}); err != nil {
		return err
	}

	if len(sourceFiles) != len(restoredFiles) {
		return fmt.Errorf("restored file count mismatch: source=%d restored=%d", len(sourceFiles), len(restoredFiles))
	}
	for key, sourcePayload := range sourceFiles {
		restoredPayload, ok := restoredFiles[key]
		if !ok {
			return fmt.Errorf("restored file missing: %s", key)
		}
		if !bytes.Equal(sourcePayload, restoredPayload) {
			return fmt.Errorf("restored file mismatch: %s", key)
		}
	}
	return nil
}

func (n *nodeAgent) ensureAssets() error {
	if err := os.MkdirAll(n.assetDir(), 0o755); err != nil {
		return err
	}
	if err := writeAssetFile(n.scrcpyServerPath(), scrcpyServerJar, 0o644); err != nil {
		return err
	}
	viewerCryptPayload, err := os.ReadFile(n.cfg.ViewerCryptPath)
	if err != nil {
		return fmt.Errorf("read viewer encryption proxy %s: %w", n.cfg.ViewerCryptPath, err)
	}
	if err := writeAssetFile(n.viewerCryptPath(), viewerCryptPayload, 0o755); err != nil {
		return err
	}
	if err := writeAssetFile(n.viewerScriptPath(), []byte(viewerScriptContent), 0o755); err != nil {
		return err
	}
	if err := writeAssetFile(n.viewerInitPath(), []byte(viewerInitContent), 0o644); err != nil {
		return err
	}
	return nil
}

func writeAssetFile(path string, payload []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".asset-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (n *nodeAgent) capabilities(ctx context.Context, includePreflight bool) map[string]any {
	blobStoreKind := strings.TrimSpace(n.cfg.BlobStoreKind)
	if blobStoreKind == "" {
		blobStoreKind = blobStoreLocal
	}
	capabilities := map[string]any{
		"id":                  n.cfg.NodeID,
		"name":                n.cfg.NodeName,
		"public_key":          n.nodePublicKey,
		"advertise_addr":      n.cfg.AdvertiseAddr,
		"relay_port":          n.cfg.RelayPort,
		"docker_socket":       dockerSocketAvailable(),
		"binder":              binderAvailable(),
		"blob_store_kind":     blobStoreKind,
		"renterd_configured":  strings.TrimSpace(n.cfg.RenterdWorkerURL) != "" && strings.TrimSpace(n.cfg.RenterdPassword) != "",
		"renterd_bucket":      defaultBlobBucket(n.cfg.RenterdBucket),
		"renterd_contractset": strings.TrimSpace(n.cfg.RenterdContractSet),
	}
	if !includePreflight {
		return capabilities
	}
	report, at := n.cachedStoragePreflight(ctx)
	if !at.IsZero() {
		status := storagePreflightStatus(report)
		capabilities["storage_preflight_kind"] = report.Store
		capabilities["storage_preflight_status"] = status
		capabilities["storage_preflight_at"] = at.Format(time.RFC3339Nano)
		capabilities["storage_preflight_json"] = mustJSON(report)
		if strings.TrimSpace(report.WalletAddress) != "" {
			capabilities["storage_wallet_address"] = strings.TrimSpace(report.WalletAddress)
		}
	}
	return capabilities
}

func (n *nodeAgent) cachedStoragePreflight(ctx context.Context) (blobPreflightReport, time.Time) {
	interval := n.cfg.BlobPreflightInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	n.blobPreflightMu.Lock()
	defer n.blobPreflightMu.Unlock()
	now := time.Now().UTC()
	if !n.blobPreflightAt.IsZero() && now.Sub(n.blobPreflightAt) < interval {
		return n.blobPreflightReport, n.blobPreflightAt
	}
	kind := strings.TrimSpace(n.cfg.BlobStoreKind)
	preflightCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	report := n.runBlobPreflightForKind(preflightCtx, kind)
	n.blobPreflightReport = report
	n.blobPreflightAt = now
	return report, now
}

func storagePreflightStatus(report blobPreflightReport) string {
	if report.OK {
		return "ready"
	}
	if strings.EqualFold(report.Store, blobStoreRenterd) {
		for _, check := range report.Checks {
			if (check.Name == "worker_url" || check.Name == "api_password") && check.Status == "fail" {
				return "error"
			}
			if check.Name == "consensus_state" && check.Status == "fail" {
				if strings.Contains(strings.ToLower(check.Detail), "not synced") {
					return "syncing"
				}
				return "error"
			}
		}
		for _, check := range report.Checks {
			if check.Name == "wallet" && check.Status == "warn" {
				return "funding_required"
			}
			if check.Name == "active_contracts" && check.Status == "fail" {
				return "contracts_required"
			}
		}
	}
	return "error"
}

func mustJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(payload)
}

func (n *nodeAgent) handlePrepareViewer(w http.ResponseWriter, r *http.Request) {
	if n.cfg.SharedSecret != "" && r.Header.Get("X-Virtroid-Node-Secret") != n.cfg.SharedSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid node secret"})
		return
	}

	var req struct {
		RuntimeID string `json:"runtime_id"`
		MaxSize   int    `json:"max_size"`
		BitRate   int    `json:"bit_rate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.RuntimeID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "runtime_id is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), viewerPrepareTimeout)
	defer cancel()

	assignments, err := n.fetchAssignments(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	var runtime *runtimeAssignment
	for i := range assignments {
		if assignments[i].ID == req.RuntimeID {
			runtime = &assignments[i]
			break
		}
	}
	if runtime == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "runtime is not assigned to this node"})
		return
	}
	if runtime.Status != "running" || runtime.ViewerPort == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "runtime is not ready for viewer sessions"})
		return
	}

	maxSize, bitRate, err := normalizeViewerPrepareParams(req.MaxSize, req.BitRate, *runtime)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	viewerPublicKey, err := n.prepareViewer(ctx, *runtime, maxSize, bitRate)
	if err != nil {
		logCtx, logCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = n.appendRuntimeLog(logCtx, runtime.ID, "node", "error", fmt.Sprintf("Viewer prepare failed: %v.", err))
		logCancel()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":                true,
		"viewer_port":       runtime.ViewerPort,
		"viewer_public_key": viewerPublicKey,
	})
}

func (n *nodeAgent) handleVerifyBlobKeyEnvelope(w http.ResponseWriter, r *http.Request) {
	if n.cfg.SharedSecret != "" && r.Header.Get("X-Virtroid-Node-Secret") != n.cfg.SharedSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid node secret"})
		return
	}

	var req struct {
		BlobKeyEnvelope blobKeyEnvelopePayload `json:"blob_key_envelope"`
		BlobKeyVerifier string                 `json:"blob_key_verifier"`
		BlobKeyExpires  time.Time              `json:"blob_key_expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if _, err := n.decryptBlobKeyEnvelope(req.BlobKeyEnvelope, req.BlobKeyVerifier, req.BlobKeyExpires); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (n *nodeAgent) handleRelaySession(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("id"))
	relayToken := strings.TrimSpace(r.Header.Get("X-Virtroid-Relay-Token"))
	if sessionID == "" || relayToken == "" {
		http.Error(w, "missing relay session details", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	target, err := n.fetchRelayTarget(ctx, sessionID, relayToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if target.HostID != n.cfg.NodeID {
		http.Error(w, "session is assigned to a different node", http.StatusNotFound)
		return
	}

	inspect, err := n.inspectContainer(ctx, containerNameForRuntime(target.RuntimeID))
	if err != nil {
		http.Error(w, fmt.Sprintf("inspect runtime container: %v", err), http.StatusBadGateway)
		return
	}
	if !inspect.State.Running {
		http.Error(w, "runtime container is not running", http.StatusBadGateway)
		return
	}

	upstream, err := n.openViewerTunnel(ctx, containerNameForRuntime(target.RuntimeID))
	if err != nil {
		http.Error(w, fmt.Sprintf("open viewer tunnel: %v", err), http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "relay hijacking unsupported", http.StatusInternalServerError)
		return
	}

	clientConn, rw, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}

	statusLine := "HTTP/1.1 200 Connection Established\r\n\r\n"
	if r.Method != http.MethodConnect {
		statusLine = "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: virtroid-relay\r\n\r\n"
	}
	if _, err := rw.WriteString(statusLine); err != nil {
		clientConn.Close()
		upstream.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		clientConn.Close()
		upstream.Close()
		return
	}

	go func() {
		_, _ = io.Copy(upstream, clientConn)
	}()
	_, _ = io.Copy(clientConn, upstream)
	clientConn.Close()
	upstream.Close()
}

func (n *nodeAgent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(n.cfg.HeartbeatInterval)
	defer ticker.Stop()

	n.sendHeartbeat(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.sendHeartbeat(ctx)
		}
	}
}

func (n *nodeAgent) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(n.cfg.ReconcileInterval)
	defer ticker.Stop()

	n.reconcileOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.reconcileOnce(ctx)
		}
	}
}

func (n *nodeAgent) sendHeartbeat(ctx context.Context) {
	body, err := json.Marshal(n.capabilities(ctx, true))
	if err != nil {
		log.Printf("marshal heartbeat: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		n.cfg.ControlPlaneURL+"/api/v1/internal/hosts/heartbeat",
		bytes.NewReader(body),
	)
	if err != nil {
		log.Printf("new heartbeat request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	if err := n.signControlPlaneRequest(req, body, true); err != nil {
		log.Printf("sign heartbeat request: %v", err)
		return
	}

	resp, err := n.controlPlane.Do(req)
	if err != nil {
		log.Printf("heartbeat failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("heartbeat rejected: status=%d body=%s", resp.StatusCode, string(payload))
		return
	}

	log.Printf("heartbeat ok: node=%s", n.cfg.NodeID)
}

func (n *nodeAgent) reconcileOnce(ctx context.Context) {
	assignments, err := n.fetchAssignments(ctx)
	if err != nil {
		log.Printf("fetch assignments: %v", err)
		return
	}

	for _, runtime := range assignments {
		if err := n.reconcileRuntime(ctx, runtime); err != nil {
			message := fmt.Sprintf("Reconcile failed: %v", err)
			log.Printf("runtime %s: %s", runtime.ID, message)
			_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "error", message)
			lastError := message
			_ = n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
				Status:           "error",
				ConnectionStatus: "offline",
				LastError:        &lastError,
			})
		}
	}
}

func (n *nodeAgent) reconcileRuntime(ctx context.Context, runtime runtimeAssignment) error {
	switch {
	case runtime.DesiredState == "deleted":
		return n.deleteRuntime(ctx, runtime)
	case runtime.WipeRequested || runtime.Status == "wiping":
		return n.wipeRuntime(ctx, runtime)
	case runtime.DesiredState == "running":
		return n.ensureRuntimeRunning(ctx, runtime)
	default:
		return n.ensureRuntimeStopped(ctx, runtime, false)
	}
}

func (n *nodeAgent) ensureRuntimeProvisioned(ctx context.Context, runtime runtimeAssignment) error {
	runtimeImage, err := runtimeImageForAssignment(runtime.AndroidImage)
	if err != nil {
		return err
	}
	runtime.AndroidImage = runtimeImage
	containerName := containerNameForRuntime(runtime.ID)
	adbPort := adbPortForRuntime(runtime.ID)
	inspect, err := n.inspectContainer(ctx, containerName)
	if err != nil && !errors.Is(err, errContainerNotFound) {
		return err
	}
	if err == nil {
		if inspect.State.Running {
			return n.ensureRuntimeStopped(ctx, runtime, false)
		}
		persona := buildSessionPersona(runtime)
		personaJSON := marshalSessionPersona(persona)
		return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
			Status:            "provisioned",
			ConnectionStatus:  "offline",
			ContainerName:     stringPtr(containerName),
			ADBPort:           &adbPort,
			LastError:         stringPtr(""),
			ActivePersonaJSON: stringPtr(personaJSON),
		})
	}

	dataDir := filepath.Join(n.cfg.RuntimeRoot, runtime.ID, "data")
	if err := os.RemoveAll(dataDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear offline runtime data dir: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create offline runtime data dir: %w", err)
	}
	if err := n.ensureImage(ctx, runtime.AndroidImage); err != nil {
		return fmt.Errorf("pull runtime image: %w", err)
	}
	persona := buildSessionPersona(runtime)
	personaJSON := marshalSessionPersona(persona)
	if err := n.createContainer(ctx, containerName, runtime, dataDir, adbPort, persona); err != nil {
		return fmt.Errorf("create offline container: %w", err)
	}
	_ = n.appendRuntimeLog(
		ctx,
		runtime.ID,
		"node",
		"info",
		fmt.Sprintf("Runtime container %s provisioned offline with persona %s.", containerName, personaSummary(persona)),
	)
	return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
		Status:            "provisioned",
		ConnectionStatus:  "offline",
		ContainerName:     stringPtr(containerName),
		ADBPort:           &adbPort,
		LastError:         stringPtr(""),
		ActivePersonaJSON: stringPtr(personaJSON),
	})
}

func (n *nodeAgent) ensureRuntimeRunning(ctx context.Context, runtime runtimeAssignment) error {
	runtimeImage, err := runtimeImageForAssignment(runtime.AndroidImage)
	if err != nil {
		return err
	}
	runtime.AndroidImage = runtimeImage
	containerName := containerNameForRuntime(runtime.ID)
	adbPort := adbPortForRuntime(runtime.ID)
	if runtime.ViewerPort == nil {
		return errors.New("runtime has no viewer port assigned")
	}
	inspect, err := n.inspectContainer(ctx, containerName)
	if err != nil && !errors.Is(err, errContainerNotFound) {
		return err
	}
	if err == nil && runtimeUsesPerRuntimeNetwork() {
		usesExpectedNetwork, networkErr := n.containerUsesExpectedRuntimeNetwork(runtime.ID, inspect)
		if networkErr != nil {
			return networkErr
		}
		if !usesExpectedNetwork {
			_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", "Migrating runtime from a shared Docker network to an isolated per-runtime bridge.")
			return n.ensureRuntimeStopped(ctx, runtime, false)
		}
		// The node container may have been recreated while the guest survived.
		// Reconcile its endpoint on every pass so ADB remains reachable without
		// ever putting the guest back on the shared Compose bridge.
		if _, networkErr := n.ensureRuntimeNetwork(ctx, runtime.ID); networkErr != nil {
			return fmt.Errorf("reconnect node agent to isolated runtime network: %w", networkErr)
		}
	}
	hadContainer := err == nil

	if err == nil && inspect.State.Running {
		ready, readyErr := n.androidBootCompleted(ctx, containerName)
		if readyErr != nil {
			return readyErr
		}
		if !ready {
			return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
				Status:           "starting",
				ConnectionStatus: "connecting",
				ContainerName:    stringPtr(containerName),
				ADBPort:          &adbPort,
				LastError:        stringPtr(""),
			})
		}
		if !strings.EqualFold(runtime.Status, "running") || !strings.EqualFold(runtime.ConnectionStatus, "online") {
			interactive, detail, interactiveErr := n.ensureAndroidInteractive(ctx, containerName)
			if interactiveErr != nil {
				return fmt.Errorf("probe Android interactive readiness: %w", interactiveErr)
			}
			if !interactive {
				log.Printf("runtime %s Android UI not ready: %s", runtime.ID, detail)
				return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
					Status:           "starting",
					ConnectionStatus: "connecting",
					ContainerName:    stringPtr(containerName),
					ADBPort:          &adbPort,
					LastError:        stringPtr(""),
				})
			}
			if installErr := n.ensureSelectedAppsInstalled(ctx, runtime, inspect); installErr != nil {
				if errors.Is(installErr, errInstalledPackageMissing) {
					cleanupErr := n.discardTaintedRuntime(ctx, runtime, containerName)
					return errors.Join(fmt.Errorf("selected app package identity verification failed: %w", installErr), cleanupErr)
				}
				_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", fmt.Sprintf("Selected app install failed: %v.", installErr))
			}
			prewarmMaxSize := max(runtime.WidthPx, runtime.HeightPx)
			if prewarmMaxSize <= 0 {
				prewarmMaxSize = viewerDefaultMaxSize
			}
			_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", "Prewarming encrypted viewer bridge.")
			if err := n.startViewerService(ctx, containerName, "127.0.0.1", prewarmMaxSize, viewerPrewarmBitRate); err != nil {
				_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", fmt.Sprintf("Viewer prewarm failed: %v.", err))
			} else {
				_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", "Encrypted viewer bridge prewarmed.")
			}
		}
		return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
			Status:           "running",
			ConnectionStatus: "online",
			ContainerName:    stringPtr(containerName),
			ADBPort:          &adbPort,
		})
	}
	if hadContainer {
		_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", "Removing stale offline container before persona rotation.")
		if err := n.stopAndRemoveContainer(ctx, containerName); err != nil && !errors.Is(err, errContainerNotFound) {
			return fmt.Errorf("remove stale offline container: %w", err)
		}
		if err := n.removeRuntimeNetwork(ctx, runtime.ID); err != nil {
			return fmt.Errorf("remove stale isolated runtime network: %w", err)
		}
		hadContainer = false
	}

	restoredSnapshot, err := n.prepareSessionData(ctx, runtime)
	if err != nil {
		return fmt.Errorf("prepare session data: %w", err)
	}
	dataDir := filepath.Join(n.cfg.RuntimeRoot, runtime.ID, "data")

	persona := buildSessionPersona(runtime)
	personaJSON := marshalSessionPersona(persona)
	if !hadContainer {
		if err := n.ensureImage(ctx, runtime.AndroidImage); err != nil {
			return fmt.Errorf("pull runtime image: %w", err)
		}
		if err := n.createContainer(ctx, containerName, runtime, dataDir, adbPort, persona); err != nil {
			return fmt.Errorf("create container: %w", err)
		}
	}
	if err := n.startContainer(ctx, containerName); err != nil {
		return fmt.Errorf("start container: %w", err)
	}

	restoreMessage := "fresh session container"
	if restoredSnapshot {
		restoreMessage = "fresh session container restored from encrypted userdata blob"
	}
	_ = n.appendRuntimeLog(
		ctx,
		runtime.ID,
		"node",
		"info",
		fmt.Sprintf(
			"Runtime container %s started on port %d with persona %s and %s.",
			containerName,
			adbPort,
			personaSummary(persona),
			restoreMessage,
		),
	)
	return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
		Status:            "starting",
		ConnectionStatus:  "connecting",
		ContainerName:     stringPtr(containerName),
		ADBPort:           &adbPort,
		LastError:         stringPtr(""),
		ActivePersonaJSON: stringPtr(personaJSON),
	})
}

func (n *nodeAgent) discardTaintedRuntime(ctx context.Context, runtime runtimeAssignment, containerName string) error {
	var cleanupErrors []error
	if err := n.stopAndRemoveContainer(ctx, containerName); err != nil && !errors.Is(err, errContainerNotFound) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove tainted runtime container: %w", err))
	}
	if err := n.removeRuntimeNetwork(ctx, runtime.ID); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove tainted runtime network: %w", err))
	}
	dataDir := filepath.Join(n.cfg.RuntimeRoot, runtime.ID, "data")
	if err := os.RemoveAll(dataDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove tainted runtime userdata: %w", err))
	}
	n.clearCachedRuntimeBlobKey(runtime.ID)
	return errors.Join(cleanupErrors...)
}

func (n *nodeAgent) ensureRuntimeStopped(ctx context.Context, runtime runtimeAssignment, clearWipe bool) error {
	containerName := containerNameForRuntime(runtime.ID)
	_, inspectErr := n.inspectContainer(ctx, containerName)
	hadContainer := inspectErr == nil
	if inspectErr != nil && !errors.Is(inspectErr, errContainerNotFound) {
		return inspectErr
	}
	persisted := &persistedBlob{}
	dataDir := filepath.Join(n.cfg.RuntimeRoot, runtime.ID, "data")
	// A stopped assignment is the durable intermediate state of the cleanup
	// protocol. Its manifest is already committed, so retries must resume cleanup
	// instead of creating another snapshot generation.
	cleanupPending := runtime.CleanupPending || strings.EqualFold(strings.TrimSpace(runtime.Status), "stopped")
	shouldPersistOrClearBlob := !cleanupPending && (hadContainer || directoryHasEntries(dataDir))
	var persistencePlan *sessionPersistencePlan
	if shouldPersistOrClearBlob {
		var err error
		persistencePlan, err = n.prepareSessionPersistence(ctx, runtime)
		if err != nil {
			return fmt.Errorf("prepare encrypted userdata persistence before stopping container: %w", err)
		}
		defer clearBytes(persistencePlan.masterKey)
	}

	if hadContainer {
		if err := n.stopContainer(ctx, containerName); err != nil && !errors.Is(err, errContainerNotFound) {
			return fmt.Errorf("quiesce runtime container before persistence: %w", err)
		}
	}
	if shouldPersistOrClearBlob {
		var err error
		persisted, err = n.persistSessionData(ctx, runtime, persistencePlan)
		if err != nil {
			return fmt.Errorf("persist encrypted userdata blob while retaining plaintext recovery state: %w", err)
		}
	}

	if hadContainer {
		if err := n.removeContainer(ctx, containerName); err != nil && !errors.Is(err, errContainerNotFound) {
			cleanupErr := n.discardUncommittedBlob(persisted)
			return errors.Join(fmt.Errorf("remove runtime container after durable persistence: %w", err), cleanupErr)
		}
	}
	if persisted != nil && persisted.SnapshotAt != nil {
		_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", "Encrypted userdata blob updated for the stopped session.")
	}
	if persisted != nil && persisted.Manifest != nil {
		_ = n.appendRuntimeLog(
			ctx,
			runtime.ID,
			"node",
			"info",
			fmt.Sprintf(
				"Blob manifest prepared: store=%s snapshot=%s chunks=%d bytes=%d.",
				persisted.Manifest.Store,
				persisted.Manifest.SnapshotID,
				len(persisted.Manifest.Chunks),
				persisted.Manifest.TotalBytes,
			),
		)
	}
	status := runtimeStatusUpdate{
		Status:             "stopped",
		ConnectionStatus:   "offline",
		ContainerName:      nil,
		ADBPort:            nil,
		LastError:          stringPtr(""),
		BlobLastSnapshotAt: persisted.SnapshotAt,
		ClearWipeRequested: clearWipe,
		ClearActivePersona: true,
	}
	if persisted != nil && persisted.Manifest != nil {
		status.BlobStoreKind = stringPtr(persisted.Manifest.Store)
		status.BlobManifestJSON = stringPtr(marshalBlobManifest(persisted.Manifest))
	}
	if persisted != nil && persisted.ClearExisting {
		status.ClearBlobManifest = true
	}
	if err := n.reportRuntimeStatus(ctx, runtime, status); err != nil {
		cleanupErr := n.discardUncommittedBlob(persisted)
		return errors.Join(fmt.Errorf("commit stopped runtime status: %w", err), cleanupErr)
	}
	if err := os.RemoveAll(dataDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove plaintext userdata after manifest commit: %w", err)
	}
	if err := n.removeRuntimeNetwork(ctx, runtime.ID); err != nil {
		return fmt.Errorf("remove isolated runtime network after manifest commit: %w", err)
	}

	retainedManifest := persisted.Manifest
	if cleanupPending && retainedManifest == nil {
		var err error
		retainedManifest, err = parseBlobManifest(runtime.BlobManifestJSON)
		if err != nil {
			return fmt.Errorf("parse committed blob manifest during cleanup retry: %w", err)
		}
	}
	if (shouldPersistOrClearBlob && persisted != nil) || cleanupPending {
		if err := n.cleanupBlobStorage(runtime, retainedManifest); err != nil {
			return fmt.Errorf("cleanup stale blob generations after manifest commit: %w", err)
		}
	}
	n.clearCachedRuntimeBlobKey(runtime.ID)

	status.CleanupComplete = true
	if err := n.reportRuntimeStatus(ctx, runtime, status); err != nil {
		return fmt.Errorf("acknowledge stopped runtime cleanup: %w", err)
	}
	return nil
}

func (n *nodeAgent) discardUncommittedBlob(persisted *persistedBlob) error {
	if persisted == nil || persisted.Manifest == nil {
		return nil
	}
	store, err := n.blobStoreForManifest(persisted.Manifest)
	if err != nil {
		return fmt.Errorf("resolve uncommitted blob store: %w", err)
	}
	if err := store.deleteManifest(context.Background(), persisted.Manifest); err != nil {
		return fmt.Errorf("delete uncommitted blob manifest: %w", err)
	}
	return nil
}

func (n *nodeAgent) wipeRuntime(ctx context.Context, runtime runtimeAssignment) error {
	masterKey, err := n.runtimeBlobKeyWithContext(ctx, runtime)
	if err != nil {
		return fmt.Errorf("verify runtime blob key before wipe: %w", err)
	}
	clearBytes(masterKey)
	containerName := containerNameForRuntime(runtime.ID)
	if err := n.stopAndRemoveContainer(ctx, containerName); err != nil && !errors.Is(err, errContainerNotFound) {
		return fmt.Errorf("remove runtime container before wipe: %w", err)
	}
	if err := n.removeRuntimeNetwork(ctx, runtime.ID); err != nil {
		return fmt.Errorf("remove isolated runtime network before wipe: %w", err)
	}

	runtimeRoot := filepath.Join(n.cfg.RuntimeRoot, runtime.ID)
	dataDir := filepath.Join(runtimeRoot, "data")
	if err := os.RemoveAll(dataDir); err != nil {
		return fmt.Errorf("remove runtime data dir: %w", err)
	}
	if err := n.clearSnapshot(runtime); err != nil {
		return fmt.Errorf("remove encrypted userdata blob: %w", err)
	}
	n.clearCachedRuntimeBlobKey(runtime.ID)

	now := time.Now().UTC()
	_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", "Runtime data directory and encrypted userdata blob wiped.")
	return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
		Status:             "stopped",
		ConnectionStatus:   "offline",
		ContainerName:      nil,
		ADBPort:            nil,
		ClearWipeRequested: true,
		ClearBlobManifest:  true,
		CleanupComplete:    true,
		BlobLastSnapshotAt: &now,
		LastError:          nil,
		ClearActivePersona: true,
	})
}

func (n *nodeAgent) deleteRuntime(ctx context.Context, runtime runtimeAssignment) error {
	containerName := containerNameForRuntime(runtime.ID)
	if err := n.stopAndRemoveContainer(ctx, containerName); err != nil && !errors.Is(err, errContainerNotFound) {
		return err
	}
	if err := n.removeRuntimeNetwork(ctx, runtime.ID); err != nil {
		return fmt.Errorf("remove isolated runtime network: %w", err)
	}

	runtimeRoot := filepath.Join(n.cfg.RuntimeRoot, runtime.ID)
	if err := os.RemoveAll(runtimeRoot); err != nil {
		return fmt.Errorf("remove runtime root: %w", err)
	}
	if err := n.clearSnapshot(runtime); err != nil {
		return fmt.Errorf("remove runtime blob data: %w", err)
	}
	n.clearCachedRuntimeBlobKey(runtime.ID)

	_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", "Runtime removed from node storage.")
	return n.reportRuntimeStatus(ctx, runtime, runtimeStatusUpdate{
		Deleted: true,
	})
}

func (n *nodeAgent) fetchAssignments(ctx context.Context) ([]runtimeAssignment, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/api/v1/internal/hosts/%s/assignments", n.cfg.ControlPlaneURL, url.PathEscape(n.cfg.NodeID)),
		nil,
	)
	if err != nil {
		return nil, err
	}
	if err := n.signControlPlaneRequest(req, nil, false); err != nil {
		return nil, err
	}

	resp, err := n.controlPlane.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("assignments rejected: status=%d body=%s", resp.StatusCode, string(payload))
	}

	var payload struct {
		Items []runtimeAssignment `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Items, nil
}

func (n *nodeAgent) fetchRelayTarget(ctx context.Context, sessionID, relayToken string) (relayTarget, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		n.cfg.ControlPlaneURL+"/api/v1/internal/sessions/"+url.PathEscape(sessionID)+"/relay",
		nil,
	)
	if err != nil {
		return relayTarget{}, err
	}
	if err := n.signControlPlaneRequest(req, nil, false); err != nil {
		return relayTarget{}, err
	}
	req.Header.Set("X-Virtroid-Relay-Token", relayToken)

	resp, err := n.controlPlane.Do(req)
	if err != nil {
		return relayTarget{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return relayTarget{}, fmt.Errorf("resolve relay target: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	var target relayTarget
	if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
		return relayTarget{}, err
	}
	return target, nil
}

type runtimeStatusUpdate struct {
	Status             string     `json:"status,omitempty"`
	ConnectionStatus   string     `json:"connection_status,omitempty"`
	ContainerName      *string    `json:"container_name,omitempty"`
	ADBPort            *int       `json:"adb_port,omitempty"`
	LastError          *string    `json:"last_error,omitempty"`
	BlobStoreKind      *string    `json:"blob_store_kind,omitempty"`
	BlobManifestJSON   *string    `json:"blob_manifest_json,omitempty"`
	BlobLastSnapshotAt *time.Time `json:"blob_last_snapshot_at,omitempty"`
	LoadAverage        *float64   `json:"load_average,omitempty"`
	ClearWipeRequested bool       `json:"clear_wipe_requested,omitempty"`
	ActivePersonaJSON  *string    `json:"active_persona_json,omitempty"`
	ClearActivePersona bool       `json:"clear_active_persona,omitempty"`
	ClearBlobManifest  bool       `json:"clear_blob_manifest,omitempty"`
	CleanupComplete    bool       `json:"cleanup_complete,omitempty"`
	Deleted            bool       `json:"deleted,omitempty"`
}

func (n *nodeAgent) reportRuntimeStatus(ctx context.Context, runtime runtimeAssignment, update runtimeStatusUpdate) error {
	body := map[string]any{
		"host_id":              n.cfg.NodeID,
		"operation_generation": runtime.OperationGeneration,
		"deleted":              update.Deleted,
		"container_name":       update.ContainerName,
		"adb_port":             update.ADBPort,
		"last_error":           update.LastError,
	}
	if update.LoadAverage != nil {
		body["load_average"] = update.LoadAverage
	} else if load, ok := nodeLoadAverage(); ok {
		body["load_average"] = load
	}
	if update.Status != "" {
		body["status"] = update.Status
	}
	if update.ConnectionStatus != "" {
		body["connection_status"] = update.ConnectionStatus
	}
	if update.BlobLastSnapshotAt != nil {
		body["blob_last_snapshot_at"] = update.BlobLastSnapshotAt
	}
	if update.BlobStoreKind != nil {
		body["blob_store_kind"] = update.BlobStoreKind
	}
	if update.BlobManifestJSON != nil {
		body["blob_manifest_json"] = update.BlobManifestJSON
	}
	if update.ClearWipeRequested {
		body["clear_wipe_requested"] = true
	}
	if update.ActivePersonaJSON != nil {
		body["active_persona_json"] = update.ActivePersonaJSON
	}
	if update.ClearActivePersona {
		body["clear_active_persona"] = true
	}
	if update.ClearBlobManifest {
		body["clear_blob_manifest"] = true
	}
	if update.CleanupComplete {
		body["cleanup_complete"] = true
	}

	return n.postControlPlane(ctx, fmt.Sprintf("/api/v1/internal/runtimes/%s/status", url.PathEscape(runtime.ID)), body)
}

func nodeLoadAverage() (*float64, bool) {
	payload, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, false
	}
	fields := strings.Fields(string(payload))
	if len(fields) == 0 {
		return nil, false
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, false
	}
	return &value, true
}

func (n *nodeAgent) appendRuntimeLog(ctx context.Context, runtimeID, source, level, message string) error {
	return n.postControlPlane(ctx, fmt.Sprintf("/api/v1/internal/runtimes/%s/logs", url.PathEscape(runtimeID)), map[string]any{
		"source":  source,
		"level":   level,
		"message": message,
	})
}

func (n *nodeAgent) postControlPlane(ctx context.Context, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.ControlPlaneURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := n.signControlPlaneRequest(req, payload, false); err != nil {
		return err
	}

	resp, err := n.controlPlane.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("control plane rejected request: status=%d body=%s", resp.StatusCode, string(payload))
	}
	return nil
}

func (n *nodeAgent) ensureImage(ctx context.Context, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("empty image")
	}

	inspectReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://docker/images/"+url.PathEscape(image)+"/json",
		nil,
	)
	if err != nil {
		return err
	}

	inspectResp, err := n.docker.Do(inspectReq)
	if err != nil {
		return err
	}
	defer inspectResp.Body.Close()

	if inspectResp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, inspectResp.Body)
		return nil
	}
	if inspectResp.StatusCode != http.StatusNotFound {
		payload, _ := io.ReadAll(io.LimitReader(inspectResp.Body, 4096))
		return fmt.Errorf("docker image inspect failed: status=%d body=%s", inspectResp.StatusCode, string(payload))
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://docker/images/create?fromImage="+url.QueryEscape(image),
		nil,
	)
	if err != nil {
		return err
	}

	resp, err := n.docker.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker image pull failed: status=%d body=%s", resp.StatusCode, string(payload))
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (n *nodeAgent) inspectContainer(ctx context.Context, containerName string) (dockerInspectResponse, error) {
	var inspect dockerInspectResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(containerName)+"/json", nil)
	if err != nil {
		return inspect, err
	}

	resp, err := n.docker.Do(req)
	if err != nil {
		return inspect, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return inspect, errContainerNotFound
	}
	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return inspect, fmt.Errorf("docker inspect failed: status=%d body=%s", resp.StatusCode, string(payload))
	}

	if err := json.NewDecoder(resp.Body).Decode(&inspect); err != nil {
		return inspect, err
	}
	return inspect, nil
}

func (n *nodeAgent) runtimeDockerNetworkName(runtimeID string) (string, error) {
	mode := runtimeNetworkMode()
	base := strings.TrimSpace(n.cfg.DockerNetworkName)
	if mode == "shared" {
		return base, nil
	}
	if mode != "per-runtime" {
		return "", fmt.Errorf("unsupported NODE_RUNTIME_NETWORK_MODE %q", mode)
	}
	if !dockerNetworkNamePattern.MatchString(base) {
		return "", errors.New("NODE_DOCKER_NETWORK must be a simple base name in per-runtime mode")
	}
	digest := sha256.Sum256([]byte(runtimeID))
	suffix := "-" + hex.EncodeToString(digest[:12])
	maxBaseLength := 63 - len(suffix)
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], ".-")
	}
	if base == "" {
		return "", errors.New("NODE_DOCKER_NETWORK base name is empty after normalization")
	}
	return base + suffix, nil
}

func runtimeUsesPerRuntimeNetwork() bool {
	return runtimeNetworkMode() == "per-runtime"
}

func runtimeNetworkMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NODE_RUNTIME_NETWORK_MODE")))
	if mode == "" {
		return "per-runtime"
	}
	return mode
}

func (n *nodeAgent) containerUsesExpectedRuntimeNetwork(runtimeID string, inspect dockerInspectResponse) (bool, error) {
	expectedNetwork, err := n.runtimeDockerNetworkName(runtimeID)
	if err != nil {
		return false, err
	}
	_, found := inspect.NetworkSettings.Networks[expectedNetwork]
	return found && len(inspect.NetworkSettings.Networks) == 1, nil
}

func (n *nodeAgent) ensureRuntimeNetwork(ctx context.Context, runtimeID string) (string, error) {
	networkName, err := n.runtimeDockerNetworkName(runtimeID)
	if err != nil || !runtimeUsesPerRuntimeNetwork() {
		return networkName, err
	}
	agentContainer := strings.TrimSpace(os.Getenv("NODE_AGENT_CONTAINER_NAME"))
	if agentContainer == "" {
		return "", errors.New("NODE_AGENT_CONTAINER_NAME is required in per-runtime network mode")
	}

	inspect, found, err := n.inspectRuntimeNetwork(ctx, networkName)
	if err != nil {
		return "", err
	}
	if !found {
		payload, marshalErr := json.Marshal(map[string]any{
			"Name":           networkName,
			"Driver":         "bridge",
			"Internal":       false,
			"Attachable":     false,
			"CheckDuplicate": true,
			"Labels": map[string]string{
				"io.virtroid.managed": "true",
				"io.virtroid.runtime": runtimeID,
			},
		})
		if marshalErr != nil {
			return "", marshalErr
		}
		status, responseBody, requestErr := n.doDockerRequest(ctx, http.MethodPost, "http://docker/networks/create", payload)
		if requestErr != nil {
			return "", requestErr
		}
		if status == http.StatusConflict {
			inspect, found, err = n.inspectRuntimeNetwork(ctx, networkName)
			if err != nil || !found {
				return "", errors.Join(fmt.Errorf("runtime network create raced: %s", strings.TrimSpace(string(responseBody))), err)
			}
		} else if status < 200 || status >= 300 {
			return "", fmt.Errorf("docker network create failed: status=%d body=%s", status, strings.TrimSpace(string(responseBody)))
		} else {
			inspect = dockerNetworkInspectResponse{
				Name: networkName,
				Labels: map[string]string{
					"io.virtroid.managed": "true",
					"io.virtroid.runtime": runtimeID,
				},
				Containers: make(map[string]struct {
					Name string `json:"Name"`
				}),
			}
		}
	}
	if inspect.Labels["io.virtroid.managed"] != "true" || inspect.Labels["io.virtroid.runtime"] != runtimeID {
		return "", fmt.Errorf("refusing unmanaged or mismatched Docker network %q", networkName)
	}
	for _, container := range inspect.Containers {
		if strings.TrimPrefix(container.Name, "/") == strings.TrimPrefix(agentContainer, "/") {
			return networkName, nil
		}
	}
	payload, err := json.Marshal(map[string]any{
		"Container": agentContainer,
	})
	if err != nil {
		return "", err
	}
	status, responseBody, err := n.doDockerRequest(ctx, http.MethodPost, "http://docker/networks/"+url.PathEscape(networkName)+"/connect", payload)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("connect node agent to runtime network: status=%d body=%s", status, strings.TrimSpace(string(responseBody)))
	}
	return networkName, nil
}

func (n *nodeAgent) inspectRuntimeNetwork(ctx context.Context, networkName string) (dockerNetworkInspectResponse, bool, error) {
	var inspect dockerNetworkInspectResponse
	status, responseBody, err := n.doDockerRequest(ctx, http.MethodGet, "http://docker/networks/"+url.PathEscape(networkName), nil)
	if err != nil {
		return inspect, false, err
	}
	if status == http.StatusNotFound {
		return inspect, false, nil
	}
	if status < 200 || status >= 300 {
		return inspect, false, fmt.Errorf("docker network inspect failed: status=%d body=%s", status, strings.TrimSpace(string(responseBody)))
	}
	if err := json.Unmarshal(responseBody, &inspect); err != nil {
		return inspect, false, fmt.Errorf("decode Docker network inspect: %w", err)
	}
	return inspect, true, nil
}

func (n *nodeAgent) removeRuntimeNetwork(ctx context.Context, runtimeID string) error {
	if !runtimeUsesPerRuntimeNetwork() {
		return nil
	}
	networkName, err := n.runtimeDockerNetworkName(runtimeID)
	if err != nil {
		return err
	}
	agentContainer := strings.TrimSpace(os.Getenv("NODE_AGENT_CONTAINER_NAME"))
	if agentContainer == "" {
		return errors.New("NODE_AGENT_CONTAINER_NAME is required in per-runtime network mode")
	}
	inspect, found, err := n.inspectRuntimeNetwork(ctx, networkName)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if inspect.Labels["io.virtroid.managed"] != "true" || inspect.Labels["io.virtroid.runtime"] != runtimeID {
		return fmt.Errorf("refusing to remove unmanaged or mismatched Docker network %q", networkName)
	}
	agentAttached := false
	for _, container := range inspect.Containers {
		if strings.TrimPrefix(container.Name, "/") == strings.TrimPrefix(agentContainer, "/") {
			agentAttached = true
			break
		}
	}
	if agentAttached {
		payload, err := json.Marshal(map[string]any{
			"Container": agentContainer,
			"Force":     true,
		})
		if err != nil {
			return err
		}
		status, responseBody, err := n.doDockerRequest(ctx, http.MethodPost, "http://docker/networks/"+url.PathEscape(networkName)+"/disconnect", payload)
		if err != nil {
			return err
		}
		alreadyDisconnected := (status == http.StatusConflict || status == http.StatusForbidden) &&
			strings.Contains(strings.ToLower(string(responseBody)), "not connected")
		if status != http.StatusNotFound && !alreadyDisconnected && (status < 200 || status >= 300) {
			return fmt.Errorf("disconnect node agent from runtime network: status=%d body=%s", status, strings.TrimSpace(string(responseBody)))
		}
	}
	var status int
	var responseBody []byte
	status, responseBody, err = n.doDockerRequest(ctx, http.MethodDelete, "http://docker/networks/"+url.PathEscape(networkName), nil)
	if err != nil {
		return err
	}
	if status != http.StatusNotFound && (status < 200 || status >= 300) {
		return fmt.Errorf("remove runtime network: status=%d body=%s", status, strings.TrimSpace(string(responseBody)))
	}
	return nil
}

func (n *nodeAgent) doDockerRequest(ctx context.Context, method, requestURL string, payload []byte) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := n.docker.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, responseBody, nil
}

func runtimeImageForAssignment(assignedImage string) (string, error) {
	image := strings.TrimSpace(os.Getenv("NODE_RUNTIME_IMAGE"))
	if image == "" {
		image = strings.TrimSpace(assignedImage)
	}
	if image == "" {
		return "", errors.New("runtime image is required")
	}
	requireDigestRaw := strings.TrimSpace(os.Getenv("NODE_REQUIRE_DIGESTED_RUNTIME_IMAGE"))
	if requireDigestRaw == "" {
		requireDigestRaw = "false"
	}
	requireDigest, err := strconv.ParseBool(requireDigestRaw)
	if err != nil {
		return "", fmt.Errorf("parse NODE_REQUIRE_DIGESTED_RUNTIME_IMAGE: %w", err)
	}
	if requireDigest && !digestedImageReferencePattern.MatchString(image) {
		return "", fmt.Errorf("runtime image %q must be an immutable @sha256 reference", image)
	}
	return image, nil
}

func runtimeResourcesFromEnv() (runtimeContainerResources, error) {
	memoryBytes, err := runtimeLimitFromEnv("NODE_RUNTIME_MEMORY_BYTES", 4<<30, 512<<20, 32<<30)
	if err != nil {
		return runtimeContainerResources{}, err
	}
	nanoCPUs, err := runtimeLimitFromEnv("NODE_RUNTIME_NANO_CPUS", 2_000_000_000, 250_000_000, 16_000_000_000)
	if err != nil {
		return runtimeContainerResources{}, err
	}
	pidsLimit, err := runtimeLimitFromEnv("NODE_RUNTIME_PIDS_LIMIT", 4096, 256, 32768)
	if err != nil {
		return runtimeContainerResources{}, err
	}
	shmBytes, err := runtimeLimitFromEnv("NODE_RUNTIME_SHM_BYTES", 256<<20, 64<<20, 2<<30)
	if err != nil {
		return runtimeContainerResources{}, err
	}
	return runtimeContainerResources{
		MemoryBytes: memoryBytes,
		NanoCPUs:    nanoCPUs,
		PidsLimit:   pidsLimit,
		ShmBytes:    shmBytes,
	}, nil
}

func runtimeLimitFromEnv(name string, fallback, minimum, maximum int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("%s=%d is outside safe range %d..%d", name, value, minimum, maximum)
	}
	return value, nil
}

func (n *nodeAgent) createContainer(ctx context.Context, containerName string, runtime runtimeAssignment, dataDir string, adbPort int, persona sessionPersona) (err error) {
	runtimeImage, err := runtimeImageForAssignment(runtime.AndroidImage)
	if err != nil {
		return err
	}
	resources, err := runtimeResourcesFromEnv()
	if err != nil {
		return err
	}
	networkName, err := n.ensureRuntimeNetwork(ctx, runtime.ID)
	if err != nil {
		return err
	}
	if runtimeUsesPerRuntimeNetwork() {
		defer func() {
			if err != nil {
				err = errors.Join(err, n.removeRuntimeNetwork(context.WithoutCancel(ctx), runtime.ID))
			}
		}()
	}
	targetFPS := 15
	gpuMode := "guest"
	if gpuAccelerationAvailable() {
		gpuMode = "auto"
		targetFPS = 30
	}
	cmd := []string{
		"androidboot.use_memfd=1",
		"androidboot.redroid_gpu_mode=" + gpuMode,
		"androidboot.redroid_gpu_node=auto",
		fmt.Sprintf("androidboot.redroid_fps=%d", targetFPS),
		fmt.Sprintf("androidboot.redroid_width=%d", runtime.WidthPx),
		fmt.Sprintf("androidboot.redroid_height=%d", runtime.HeightPx),
		fmt.Sprintf("androidboot.redroid_dpi=%d", runtime.DensityDpi),
	}
	cmd = append(cmd, personaOverrideProps(persona)...)

	body := map[string]any{
		"Image": runtimeImage,
		"Cmd":   cmd,
		"ExposedPorts": map[string]any{
			"5555/tcp": map[string]any{},
		},
		"HostConfig": map[string]any{
			"Privileged":     true,
			"Memory":         resources.MemoryBytes,
			"MemorySwap":     resources.MemoryBytes,
			"NanoCpus":       resources.NanoCPUs,
			"PidsLimit":      resources.PidsLimit,
			"ShmSize":        resources.ShmBytes,
			"OomKillDisable": false,
			"LogConfig": map[string]any{
				"Type": "local",
				"Config": map[string]string{
					"max-size": "10m",
					"max-file": "3",
				},
			},
			"Binds": []string{
				dataDir + ":/data",
				n.scrcpyServerPath() + ":" + scrcpyServerMountPath + ":ro",
				n.viewerCryptPath() + ":" + viewerCryptMountPath + ":ro",
				n.viewerScriptPath() + ":" + viewerScriptMountPath + ":ro",
				n.viewerInitPath() + ":" + viewerInitMountPath + ":ro",
			},
			"PortBindings": map[string]any{
				"5555/tcp": []map[string]string{
					{
						"HostIp":   "127.0.0.1",
						"HostPort": strconv.Itoa(adbPort),
					},
				},
			},
		},
	}
	if networkName = strings.TrimSpace(networkName); networkName != "" {
		body["HostConfig"].(map[string]any)["NetworkMode"] = networkName
		body["NetworkingConfig"] = map[string]any{
			"EndpointsConfig": map[string]any{
				networkName: map[string]any{},
			},
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://docker/containers/create?name="+url.QueryEscape(containerName),
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.docker.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker create failed: status=%d body=%s", resp.StatusCode, string(payload))
	}

	return nil
}

func (n *nodeAgent) startContainer(ctx context.Context, containerName string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/containers/"+url.PathEscape(containerName)+"/start", nil)
	if err != nil {
		return err
	}

	resp, err := n.docker.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker start failed: status=%d body=%s", resp.StatusCode, string(payload))
	}

	return nil
}

func (n *nodeAgent) prepareViewer(ctx context.Context, runtime runtimeAssignment, maxSize, bitRate int) (string, error) {
	containerName := containerNameForRuntime(runtime.ID)
	inspect, err := n.inspectContainer(ctx, containerName)
	if err != nil {
		return "", err
	}
	if !inspect.State.Running {
		return "", errors.New("runtime container is not running")
	}
	ready, readyErr := n.androidBootCompleted(ctx, containerName)
	if readyErr != nil {
		return "", readyErr
	}
	if !ready {
		return "", errors.New("android runtime is still booting")
	}
	interactive, detail, interactiveErr := n.ensureAndroidInteractive(ctx, containerName)
	if interactiveErr != nil {
		return "", fmt.Errorf("probe Android interactive readiness: %w", interactiveErr)
	}
	if !interactive {
		return "", fmt.Errorf("android runtime UI is still starting: %s", detail)
	}

	clientIP := "127.0.0.1"
	_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", "Preparing viewer via in-guest init service.")
	if err := n.startViewerService(ctx, containerName, clientIP, maxSize, bitRate); err != nil {
		return "", fmt.Errorf("start viewer service: %w", err)
	}
	if err := n.waitForViewerPort(ctx, runtime, containerName); err != nil {
		return "", err
	}
	viewerPublicKey, err := n.readViewerPublicKey(ctx, containerName)
	if err != nil {
		return "", err
	}

	_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", fmt.Sprintf("Encrypted viewer proxy prepared on guest port %d.", encryptedViewerPort))
	return viewerPublicKey, nil
}

func (n *nodeAgent) readViewerPublicKey(ctx context.Context, containerName string) (string, error) {
	output, err := n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
		{"/system/bin/cat", viewerPublicKeyPath},
		{"cat", viewerPublicKeyPath},
		{"toybox", "cat", viewerPublicKeyPath},
	})
	if err != nil {
		return "", fmt.Errorf("read viewer public key: %w", err)
	}
	viewerPublicKey := strings.TrimSpace(output)
	if viewerPublicKey == "" {
		return "", errors.New("viewer public key is empty")
	}
	return viewerPublicKey, nil
}

func (n *nodeAgent) viewerCommandLogPath(runtimeID string) string {
	return filepath.Join(n.cfg.RuntimeRoot, runtimeID, "viewer-adb.log")
}

func (n *nodeAgent) adbSerialForRuntime(runtime runtimeAssignment, inspect dockerInspectResponse) (string, error) {
	if runtime.ADBPort != nil && *runtime.ADBPort > 0 {
		if host := strings.TrimSpace(n.cfg.ADBHost); host != "" {
			return net.JoinHostPort(host, strconv.Itoa(*runtime.ADBPort)), nil
		}
	}

	preferredNetwork, err := n.runtimeDockerNetworkName(runtime.ID)
	if err != nil {
		return "", err
	}
	if containerIP := containerIPAddress(inspect, preferredNetwork); containerIP != "" {
		return net.JoinHostPort(containerIP, "5555"), nil
	}

	if runtime.ADBPort != nil && *runtime.ADBPort > 0 {
		if gateway := defaultGatewayIP(); gateway != "" {
			return net.JoinHostPort(gateway, strconv.Itoa(*runtime.ADBPort)), nil
		}
	}

	return "", errors.New("runtime container has no reachable ADB address")
}

func normalizeViewerPrepareParams(requestedMaxSize, requestedBitRate int, runtime runtimeAssignment) (int, int, error) {
	maxSize := requestedMaxSize
	if maxSize <= 0 {
		maxSize = max(runtime.WidthPx, runtime.HeightPx)
	}
	if maxSize <= 0 {
		maxSize = viewerDefaultMaxSize
	}
	if maxSize < viewerMinMaxSize || maxSize > viewerMaxMaxSize {
		return 0, 0, fmt.Errorf("max_size must be between %d and %d", viewerMinMaxSize, viewerMaxMaxSize)
	}

	bitRate := requestedBitRate
	if bitRate <= 0 {
		bitRate = viewerDefaultBitRate
	}
	if bitRate < viewerMinBitRate || bitRate > viewerMaxBitRate {
		return 0, 0, fmt.Errorf("bit_rate must be between %d and %d", viewerMinBitRate, viewerMaxBitRate)
	}
	return maxSize, bitRate, nil
}

func (n *nodeAgent) adbConnect(ctx context.Context, serial string) error {
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(connectCtx, n.cfg.ADBPath, "connect", serial)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}

	_, err = n.adbShellCapture(ctx, serial, "true")
	return err
}

func (n *nodeAgent) adbPush(ctx context.Context, serial string, localPath string, remotePath string) error {
	cmd := exec.CommandContext(ctx, n.cfg.ADBPath, "-s", serial, "push", localPath, remotePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (n *nodeAgent) adbShellCapture(ctx context.Context, serial string, shellCmd string) (string, error) {
	cmd := exec.CommandContext(ctx, n.cfg.ADBPath, "-s", serial, "shell", "sh", "-c", shellCmd)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed != "" {
			return trimmed, fmt.Errorf("%w: %s", err, trimmed)
		}
		return trimmed, err
	}
	return trimmed, nil
}

func (n *nodeAgent) ensureSelectedAppsInstalled(ctx context.Context, runtime runtimeAssignment, inspect dockerInspectResponse) error {
	apps := n.runtimeAppsToInstall(runtime)
	if len(apps) == 0 {
		return nil
	}
	serial, err := n.adbSerialForRuntime(runtime, inspect)
	if err != nil {
		return err
	}
	if err := n.adbConnect(ctx, serial); err != nil {
		return fmt.Errorf("connect adb for app install: %w", err)
	}

	installed := 0
	var failures []error
	for _, app := range apps {
		packageName := strings.TrimSpace(app.PackageName)
		if !appPackageNamePattern.MatchString(packageName) {
			failures = append(failures, fmt.Errorf("%s: invalid package name", packageName))
			continue
		}
		if n.androidPackageInstalled(ctx, serial, packageName) {
			if err := n.applyRuntimeAppPolicy(ctx, serial, app); err != nil {
				message := fmt.Sprintf("Apply selected app policy %s failed: %v.", packageName, err)
				_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", message)
				failures = append(failures, fmt.Errorf("%s policy: %w", packageName, err))
			}
			continue
		}
		if err := n.installRuntimeApp(ctx, serial, app); err != nil {
			message := fmt.Sprintf("Install selected app %s failed: %v.", packageName, err)
			_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", message)
			failures = append(failures, fmt.Errorf("%s: %w", packageName, err))
			continue
		}
		if !n.androidPackageInstalled(ctx, serial, packageName) {
			identityErr := fmt.Errorf("%w: %s", errInstalledPackageMissing, packageName)
			message := fmt.Sprintf("Installed selected app artifact did not expose expected package %s; discarding the tainted session.", packageName)
			_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "error", message)
			failures = append(failures, identityErr)
			continue
		}
		if err := n.applyRuntimeAppPolicy(ctx, serial, app); err != nil {
			message := fmt.Sprintf("Apply selected app policy %s failed: %v.", packageName, err)
			_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", message)
			failures = append(failures, fmt.Errorf("%s policy: %w", packageName, err))
			continue
		}
		installed++
		displayName := strings.TrimSpace(app.DisplayName)
		if displayName == "" {
			displayName = packageName
		}
		_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", fmt.Sprintf("Installed selected app %s.", displayName))
	}
	if installed > 0 {
		_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", fmt.Sprintf("Installed %d selected app package(s).", installed))
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

func (n *nodeAgent) applyRuntimeAppPolicy(ctx context.Context, serial string, app runtimeApp) error {
	if !app.SetAsHome {
		return nil
	}
	return n.setDefaultHomeActivity(ctx, serial, app.HomeActivity)
}

func (n *nodeAgent) runtimeAppsToInstall(runtime runtimeAssignment) []runtimeApp {
	catalog := n.trustedAppCatalog()
	defaultPackages := append([]string{}, n.cfg.DefaultAppPackages...)
	defaultPackages = append(defaultPackages, catalog.defaultPackages...)

	seen := make(map[string]bool, len(defaultPackages)+len(runtime.SelectedApps))
	apps := make([]runtimeApp, 0, len(defaultPackages)+len(runtime.SelectedApps))
	for _, packageName := range defaultPackages {
		packageName = strings.TrimSpace(packageName)
		if packageName == "" || seen[packageName] {
			continue
		}
		app, ok := catalog.apps[packageName]
		if !ok {
			app = runtimeApp{
				PackageName: packageName,
				DisplayName: packageName,
			}
		}
		seen[packageName] = true
		apps = append(apps, app)
	}
	for _, app := range runtime.SelectedApps {
		packageName := strings.TrimSpace(app.PackageName)
		if packageName == "" || seen[packageName] {
			continue
		}
		seen[packageName] = true
		if catalogApp, ok := catalog.apps[packageName]; ok {
			app = mergeRuntimeApp(app, catalogApp)
		}
		apps = append(apps, app)
	}
	return apps
}

func mergeRuntimeApp(selection runtimeApp, trusted runtimeApp) runtimeApp {
	if strings.TrimSpace(trusted.PackageName) != "" {
		selection.PackageName = trusted.PackageName
	}
	if strings.TrimSpace(trusted.DisplayName) != "" {
		selection.DisplayName = trusted.DisplayName
	}
	if strings.TrimSpace(trusted.Artifact) != "" {
		selection.Artifact = trusted.Artifact
	}
	if strings.TrimSpace(trusted.InstallMode) != "" {
		selection.InstallMode = trusted.InstallMode
	}
	if strings.TrimSpace(trusted.APKSHA256) != "" {
		selection.APKSHA256 = trusted.APKSHA256
	}
	if trusted.APKSizeBytes > 0 {
		selection.APKSizeBytes = trusted.APKSizeBytes
	}
	if trusted.SetAsHome {
		selection.SetAsHome = true
	}
	if strings.TrimSpace(trusted.HomeActivity) != "" {
		selection.HomeActivity = trusted.HomeActivity
	}
	return selection
}

func (n *nodeAgent) trustedAppCatalog() trustedAppCatalog {
	catalog := trustedAppCatalog{
		apps: make(map[string]runtimeApp, len(builtInTrustedAppCatalog)),
	}
	for packageName, app := range builtInTrustedAppCatalog {
		catalog.apps[packageName] = app
	}

	manifestPath := strings.TrimSpace(n.cfg.AppManifestPath)
	if manifestPath == "" {
		manifestPath = filepath.Join(n.appAPKRoot(), "manifest.json")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("read app install manifest: %v", err)
		}
		return catalog
	}
	defer file.Close()

	var manifest appInstallManifest
	if err := json.NewDecoder(io.LimitReader(file, 2*1024*1024)).Decode(&manifest); err != nil {
		log.Printf("parse app install manifest: %v", err)
		return catalog
	}
	if manifest.Version != 0 && manifest.Version != 1 {
		log.Printf("app install manifest has unsupported version %d", manifest.Version)
		return catalog
	}
	catalog.defaultPackages = append(catalog.defaultPackages, manifest.DefaultPackages...)
	for _, entry := range manifest.Apps {
		app, err := runtimeAppFromManifestEntry(entry)
		if err != nil {
			log.Printf("skip app manifest entry: %v", err)
			continue
		}
		catalog.apps[app.PackageName] = app
		if entry.Default {
			catalog.defaultPackages = append(catalog.defaultPackages, app.PackageName)
		}
	}
	return catalog
}

func runtimeAppFromManifestEntry(entry appManifestEntry) (runtimeApp, error) {
	packageName := strings.TrimSpace(entry.PackageName)
	if !appPackageNamePattern.MatchString(packageName) {
		return runtimeApp{}, fmt.Errorf("invalid app package %q", packageName)
	}
	pin, err := normalizeSHA256Pin(entry.SHA256)
	if err != nil {
		return runtimeApp{}, fmt.Errorf("%s: %w", packageName, err)
	}
	artifact := strings.TrimSpace(entry.Artifact)
	if artifact == "" {
		return runtimeApp{}, fmt.Errorf("%s: artifact is required", packageName)
	}
	mode, err := normalizeInstallMode(entry.InstallMode, artifact)
	if err != nil {
		return runtimeApp{}, fmt.Errorf("%s: %w", packageName, err)
	}
	displayName := strings.TrimSpace(entry.DisplayName)
	if displayName == "" {
		displayName = packageName
	}
	homeActivity, err := normalizeHomeActivity(packageName, entry.HomeActivity, entry.SetAsHome)
	if err != nil {
		return runtimeApp{}, fmt.Errorf("%s: %w", packageName, err)
	}
	return runtimeApp{
		PackageName:  packageName,
		DisplayName:  displayName,
		Artifact:     artifact,
		InstallMode:  mode,
		APKSHA256:    pin,
		APKSizeBytes: entry.APKSizeBytes,
		SetAsHome:    entry.SetAsHome,
		HomeActivity: homeActivity,
	}, nil
}

func (n *nodeAgent) androidPackageInstalled(ctx context.Context, serial, packageName string) bool {
	out, err := n.adbShellCapture(ctx, serial, fmt.Sprintf("pm path %s >/dev/null 2>&1 && echo installed || true", shellQuote(packageName)))
	return err == nil && strings.Contains(out, "installed")
}

func (n *nodeAgent) setDefaultHomeActivity(ctx context.Context, serial, component string) error {
	component = strings.TrimSpace(component)
	if component == "" {
		return errors.New("home activity is required")
	}
	commands := []string{
		fmt.Sprintf("cmd package set-home-activity --user 0 %s", shellQuote(component)),
		fmt.Sprintf("cmd package set-home-activity %s", shellQuote(component)),
	}
	var lastErr error
	for _, command := range commands {
		if _, err := n.adbShellCapture(ctx, serial, command); err == nil {
			_, _ = n.adbShellCapture(ctx, serial, "am start -a android.intent.action.MAIN -c android.intent.category.HOME >/dev/null 2>&1 || true")
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("set default home activity %s: %w", component, lastErr)
}

func (n *nodeAgent) adbInstallAPK(ctx context.Context, serial, apkPath string) error {
	installCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(installCtx, n.cfg.ADBPath, "-s", serial, "install", "-r", "-g", apkPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" && !strings.Contains(strings.ToLower(trimmed), "success") {
		return fmt.Errorf("adb install returned unexpected output: %s", trimmed)
	}
	return nil
}

func (n *nodeAgent) adbInstallMultipleAPKs(ctx context.Context, serial string, apkPaths []string) error {
	if len(apkPaths) == 0 {
		return errors.New("no APK split files found")
	}
	sort.Strings(apkPaths)
	args := append([]string{"-s", serial, "install-multiple", "-r", "-g"}, apkPaths...)
	installCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(installCtx, n.cfg.ADBPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" && !strings.Contains(strings.ToLower(trimmed), "success") {
		return fmt.Errorf("adb install-multiple returned unexpected output: %s", trimmed)
	}
	return nil
}

func (n *nodeAgent) installRuntimeApp(ctx context.Context, serial string, app runtimeApp) error {
	packageName := strings.TrimSpace(app.PackageName)
	if strings.TrimSpace(app.Artifact) != "" {
		artifactPath, err := n.appArtifactPath(app.Artifact)
		if err != nil {
			return err
		}
		if err := verifyAPKFile(artifactPath, app.APKSHA256); err != nil {
			return err
		}
		mode, err := normalizeInstallMode(app.InstallMode, artifactPath)
		if err != nil {
			return err
		}
		switch mode {
		case "single":
			return n.adbInstallAPK(ctx, serial, artifactPath)
		case "apkm":
			apkPaths, err := extractAPKM(artifactPath, filepath.Join(n.appAPKRoot(), "cache", packageName+"-apkm"))
			if err != nil {
				return err
			}
			return n.adbInstallMultipleAPKs(ctx, serial, apkPaths)
		default:
			return fmt.Errorf("unsupported install mode %q", mode)
		}
	}

	apkPath, err := n.apkPathForSelectedApp(ctx, app)
	if err != nil {
		return err
	}
	return n.adbInstallAPK(ctx, serial, apkPath)
}

func (n *nodeAgent) apkPathForSelectedApp(ctx context.Context, app runtimeApp) (string, error) {
	packageName := strings.TrimSpace(app.PackageName)
	if !appPackageNamePattern.MatchString(packageName) {
		return "", fmt.Errorf("invalid package name %q", packageName)
	}

	apkURL := strings.TrimSpace(app.APKURL)
	if apkURL == "" {
		return "", errors.New("no trusted artifact or download URL is configured")
	}
	pin, err := normalizeSHA256Pin(app.APKSHA256)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(apkURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "f-droid.org") || parsed.User != nil {
		return "", fmt.Errorf("unsupported APK URL")
	}

	cachePath := filepath.Join(n.appAPKRoot(), "cache", pin+".apk")
	if regularFileExists(cachePath) {
		if err := verifyAPKFile(cachePath, app.APKSHA256); err != nil {
			_ = os.Remove(cachePath)
		} else {
			return cachePath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return "", err
	}
	tmpPath := cachePath + ".tmp"
	if err := n.downloadAPK(ctx, apkURL, tmpPath, app.APKSizeBytes); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := verifyAPKFile(tmpPath, app.APKSHA256); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return cachePath, nil
}

func (n *nodeAgent) appAPKRoot() string {
	root := strings.TrimSpace(n.cfg.AppAPKDir)
	if root == "" {
		return "/srv/virtroid/apks"
	}
	return root
}

func (n *nodeAgent) appArtifactPath(artifact string) (string, error) {
	artifact = strings.TrimSpace(artifact)
	if artifact == "" {
		return "", errors.New("app artifact path is required")
	}
	if filepath.IsAbs(artifact) {
		return "", errors.New("app artifact path must be relative to NODE_APP_APK_DIR")
	}
	cleaned := filepath.Clean(artifact)
	if cleaned == "." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return "", errors.New("app artifact path escapes NODE_APP_APK_DIR")
	}
	path := filepath.Join(n.appAPKRoot(), cleaned)
	if !regularFileExists(path) {
		return "", fmt.Errorf("trusted app artifact not found: %s", cleaned)
	}
	return path, nil
}

func normalizeInstallMode(mode, artifact string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		switch strings.ToLower(filepath.Ext(artifact)) {
		case ".apk":
			mode = "single"
		case ".apkm":
			mode = "apkm"
		default:
			return "", errors.New("install_mode is required for non-APK artifacts")
		}
	}
	switch mode {
	case "single", "apkm":
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported install_mode %q", mode)
	}
}

func normalizeHomeActivity(packageName, activity string, setAsHome bool) (string, error) {
	activity = strings.TrimSpace(activity)
	if !setAsHome {
		return "", nil
	}
	if activity == "" {
		return "", errors.New("home_activity is required when set_as_home is true")
	}
	if strings.ContainsAny(activity, " \t\r\n'\"`;&|<>$\\") {
		return "", errors.New("home_activity contains unsupported shell characters")
	}
	componentPackage := packageName
	componentActivity := activity
	if pkg, act, ok := strings.Cut(activity, "/"); ok {
		componentPackage = strings.TrimSpace(pkg)
		componentActivity = strings.TrimSpace(act)
	}
	if componentPackage != packageName {
		return "", errors.New("home_activity package must match package_name")
	}
	if strings.HasPrefix(componentActivity, ".") {
		componentActivity = packageName + componentActivity
	}
	if !appPackageNamePattern.MatchString(componentPackage) || !appPackageNamePattern.MatchString(componentActivity) {
		return "", errors.New("home_activity must be an Android component")
	}
	return componentPackage + "/" + componentActivity, nil
}

func extractAPKM(apkmPath, extractDir string) ([]string, error) {
	return extractAPKMWithLimits(
		apkmPath,
		extractDir,
		maxAPKMFiles,
		maxAPKMArchiveEntries,
		maxAPKMFileBytes,
		maxAPKMTotalBytes,
	)
}

func extractAPKMWithLimits(apkmPath, extractDir string, maxFiles, maxEntries int, maxFileBytes, maxTotalBytes int64) ([]string, error) {
	if maxFiles <= 0 || maxEntries <= 0 || maxFileBytes <= 0 || maxTotalBytes <= 0 {
		return nil, errors.New("APKM extraction limits must be positive")
	}
	if maxEntries < maxFiles {
		return nil, errors.New("APKM archive-entry limit must be at least the APK-file limit")
	}
	// archive/zip materializes every central-directory entry while opening an
	// archive. Inspect that directory with bounded memory first so an archive
	// containing millions of empty or non-APK entries cannot exhaust the node
	// before the post-parse len(reader.File) check runs.
	archive, err := os.Open(apkmPath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	archiveInfo, err := archive.Stat()
	if err != nil {
		return nil, err
	}
	if err := validateZipCentralDirectoryEntryLimit(archive, archiveInfo.Size(), maxEntries); err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(archive, archiveInfo.Size())
	if err != nil {
		return nil, err
	}
	if len(reader.File) > maxEntries {
		return nil, fmt.Errorf("APKM contains more than %d archive entries", maxEntries)
	}

	if err := os.RemoveAll(extractDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return nil, err
	}
	extractRoot, err := os.OpenRoot(extractDir)
	if err != nil {
		return nil, err
	}
	extracted := false
	defer func() {
		if extractRoot != nil {
			_ = extractRoot.Close()
		}
		if !extracted {
			_ = os.RemoveAll(extractDir)
		}
	}()

	type apkmArchiveEntry struct {
		file *zip.File
		name string
	}
	apkFiles := make([]apkmArchiveEntry, 0, min(len(reader.File), maxFiles))
	seenNames := make(map[string]struct{})
	var declaredTotal int64
	for _, file := range reader.File {
		isDirectory := file.FileInfo().IsDir()
		entryName, err := canonicalArchiveRelativePath(file.Name, isDirectory)
		if err != nil {
			return nil, fmt.Errorf("APKM entry %q has an invalid path: %w", file.Name, err)
		}
		nameKey := entryName
		if _, exists := seenNames[nameKey]; exists {
			return nil, fmt.Errorf("APKM contains duplicate archive path %q", file.Name)
		}
		seenNames[nameKey] = struct{}{}

		modeType := file.Mode() & os.ModeType
		if isDirectory {
			if modeType != os.ModeDir {
				return nil, fmt.Errorf("APKM entry %q has an invalid directory type", file.Name)
			}
			continue
		}
		if modeType != 0 {
			return nil, fmt.Errorf("APKM entry %q must be a regular file", file.Name)
		}
		if !strings.HasSuffix(strings.ToLower(entryName), ".apk") {
			continue
		}
		if len(apkFiles) >= maxFiles {
			return nil, fmt.Errorf("APKM contains more than %d APK files", maxFiles)
		}
		if file.UncompressedSize64 > uint64(maxFileBytes) {
			return nil, fmt.Errorf("APKM entry %q exceeds %d-byte limit", file.Name, maxFileBytes)
		}
		fileBytes := int64(file.UncompressedSize64)
		if fileBytes > maxTotalBytes-declaredTotal {
			return nil, fmt.Errorf("APKM APK contents exceed %d-byte total limit", maxTotalBytes)
		}
		declaredTotal += fileBytes
		apkFiles = append(apkFiles, apkmArchiveEntry{file: file, name: entryName})
	}
	if len(apkFiles) == 0 {
		return nil, errors.New("APKM contains no APK files")
	}
	sort.Slice(apkFiles, func(left, right int) bool {
		return apkFiles[left].name < apkFiles[right].name
	})

	apkPaths := make([]string, 0, len(apkFiles))
	var extractedTotal int64
	for index, archiveEntry := range apkFiles {
		outputName := fmt.Sprintf("apk-%03d.apk", index)
		targetPath := filepath.Join(extractDir, outputName)
		source, err := archiveEntry.file.Open()
		if err != nil {
			return nil, err
		}
		target, err := extractRoot.OpenFile(outputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			source.Close()
			return nil, err
		}
		remainingTotal := maxTotalBytes - extractedTotal
		readLimit := min(maxFileBytes, remainingTotal)
		written, copyErr := io.Copy(target, io.LimitReader(source, readLimit+1))
		closeErr := target.Close()
		sourceCloseErr := source.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if sourceCloseErr != nil {
			return nil, sourceCloseErr
		}
		if written > maxFileBytes {
			return nil, fmt.Errorf("APKM entry %q exceeds %d-byte limit", archiveEntry.file.Name, maxFileBytes)
		}
		if written > maxTotalBytes-extractedTotal {
			return nil, fmt.Errorf("APKM APK contents exceed %d-byte total limit", maxTotalBytes)
		}
		extractedTotal += written
		apkPaths = append(apkPaths, targetPath)
	}
	if err := extractRoot.Close(); err != nil {
		extractRoot = nil
		return nil, err
	}
	extractRoot = nil
	extracted = true
	return apkPaths, nil
}

func validateZipCentralDirectoryEntryLimit(archive io.ReaderAt, archiveSize int64, maxEntries int) error {
	if archive == nil {
		return errors.New("ZIP archive reader is required")
	}
	if maxEntries <= 0 {
		return errors.New("ZIP archive-entry limit must be positive")
	}
	if archiveSize < zipDirectoryEndLength {
		return zip.ErrFormat
	}
	tailLength := min(archiveSize, int64(zipDirectoryEndLength+zipMaximumCommentLength))
	tail := make([]byte, int(tailLength))
	if _, err := archive.ReadAt(tail, archiveSize-tailLength); err != nil {
		return err
	}

	directoryEndIndex := -1
	for index := len(tail) - zipDirectoryEndLength; index >= 0; index-- {
		if binary.LittleEndian.Uint32(tail[index:index+4]) != zipDirectoryEndSignature {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(tail[index+20 : index+22]))
		if index+zipDirectoryEndLength+commentLength <= len(tail) {
			directoryEndIndex = index
			break
		}
	}
	if directoryEndIndex < 0 {
		return zip.ErrFormat
	}

	directoryEndOffset := archiveSize - tailLength + int64(directoryEndIndex)
	directoryEnd := tail[directoryEndIndex : directoryEndIndex+zipDirectoryEndLength]
	diskNumber := uint32(binary.LittleEndian.Uint16(directoryEnd[4:6]))
	directoryDiskNumber := uint32(binary.LittleEndian.Uint16(directoryEnd[6:8]))
	recordsOnDisk := uint64(binary.LittleEndian.Uint16(directoryEnd[8:10]))
	recordsTotal := uint64(binary.LittleEndian.Uint16(directoryEnd[10:12]))
	directorySize := uint64(binary.LittleEndian.Uint32(directoryEnd[12:16]))
	directoryOffset := uint64(binary.LittleEndian.Uint32(directoryEnd[16:20]))

	// Keep this decision exactly aligned with archive/zip.readDirectoryEnd.
	// These sentinel fields make archive/zip consult ZIP64 metadata before it
	// allocates File records. APKM limits never require ZIP64, so rejecting it is
	// both simpler and safer than maintaining a second, subtly divergent parser.
	if recordsTotal == ^uint64(0)>>48 ||
		directorySize == ^uint64(0)>>48 ||
		directoryOffset == uint64(^uint32(0)) {
		return errors.New("ZIP64 APKM archives are not supported")
	}
	if diskNumber != 0 || directoryDiskNumber != 0 || recordsOnDisk != recordsTotal {
		return errors.New("multi-disk ZIP archives are not supported")
	}
	if recordsTotal > uint64(maxEntries) {
		return fmt.Errorf("APKM contains more than %d archive entries", maxEntries)
	}
	if directorySize > uint64(directoryEndOffset) || directoryOffset > uint64(^uint64(0)>>1) {
		return zip.ErrFormat
	}

	baseOffset := directoryEndOffset - int64(directorySize) - int64(directoryOffset)
	if baseOffset < 0 {
		return zip.ErrFormat
	}
	directoryStart := baseOffset + int64(directoryOffset)
	if baseOffset > 0 && directoryOffset <= uint64(archiveSize-4) {
		var signature [4]byte
		if _, err := archive.ReadAt(signature[:], int64(directoryOffset)); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(signature[:]) == zipDirectoryHeaderSignature {
			// Match archive/zip's compatibility behavior for archives whose offset
			// is already absolute. Reject a gap here because archive/zip would scan
			// beyond the declared directory and could allocate unbounded entries.
			if directoryOffset+directorySize != uint64(directoryEndOffset) {
				return errors.New("ZIP central-directory bounds are inconsistent")
			}
			directoryStart = int64(directoryOffset)
		}
	}
	if directoryStart < 0 || directoryStart+int64(directorySize) != directoryEndOffset {
		return zip.ErrFormat
	}

	directory := io.NewSectionReader(archive, directoryStart, int64(directorySize))
	remaining := int64(directorySize)
	entryCount := 0
	for remaining > 0 {
		if remaining < zipDirectoryHeaderLength {
			return zip.ErrFormat
		}
		var header [zipDirectoryHeaderLength]byte
		if _, err := io.ReadFull(directory, header[:]); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header[0:4]) != zipDirectoryHeaderSignature {
			return zip.ErrFormat
		}
		entryCount++
		if entryCount > maxEntries {
			return fmt.Errorf("APKM contains more than %d archive entries", maxEntries)
		}
		variableLength := int64(binary.LittleEndian.Uint16(header[28:30])) +
			int64(binary.LittleEndian.Uint16(header[30:32])) +
			int64(binary.LittleEndian.Uint16(header[32:34]))
		remaining -= zipDirectoryHeaderLength
		if variableLength > remaining {
			return zip.ErrFormat
		}
		if _, err := directory.Seek(variableLength, io.SeekCurrent); err != nil {
			return err
		}
		remaining -= variableLength
	}
	if uint64(entryCount) != recordsTotal {
		return errors.New("ZIP central-directory entry count is inconsistent")
	}
	return nil
}

func (n *nodeAgent) downloadAPK(ctx context.Context, apkURL, targetPath string, expectedSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apkURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("download APK failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	limit := int64(512 * 1024 * 1024)
	if expectedSize > 0 && expectedSize+1024*1024 < limit {
		limit = expectedSize + 1024*1024
	}
	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	written, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if written > limit {
		return fmt.Errorf("APK exceeds configured download limit")
	}
	return nil
}

func verifyAPKFile(path, expectedSHA256 string) error {
	expected, err := normalizeSHA256Pin(expectedSHA256)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("APK hash mismatch for %s", filepath.Base(path))
	}
	return nil
}

func normalizeSHA256Pin(expectedSHA256 string) (string, error) {
	expected := strings.ToLower(strings.TrimSpace(expectedSHA256))
	if expected == "" {
		return "", errors.New("APK SHA-256 pin is required")
	}
	if len(expected) != sha256.Size*2 {
		return "", errors.New("APK SHA-256 pin has invalid length")
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return "", errors.New("APK SHA-256 pin is not valid hex")
	}
	return expected, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (n *nodeAgent) adbLogcatCapture(ctx context.Context, serial string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	return n.adbShellCapture(ctx, serial, fmt.Sprintf("logcat -b all -d -t %d 2>/dev/null", lines))
}

func (n *nodeAgent) startViewerServer(runtimeID string, adbSerial string, maxSize int, bitRate int) error {
	logPath := n.viewerCommandLogPath(runtimeID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	shellCmd := fmt.Sprintf(
		"CLASSPATH=/data/local/tmp/scrcpy-server.jar app_process / org.server.scrcpy.Server /0.0.0.0 %d %d false",
		maxSize,
		bitRate,
	)
	cmd := exec.Command(n.cfg.ADBPath, "-s", adbSerial, "shell", shellCmd)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}

	go func() {
		defer logFile.Close()
		if err := cmd.Wait(); err != nil {
			log.Printf("viewer adb command exited for runtime %s: %v", runtimeID, err)
			return
		}
		log.Printf("viewer adb command exited cleanly for runtime %s", runtimeID)
	}()

	return nil
}

func (n *nodeAgent) setContainerProp(ctx context.Context, containerName, key, value string) error {
	_, err := n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
		{"setprop", key, value},
		{"/system/bin/setprop", key, value},
		{"toybox", "setprop", key, value},
		{"/system/bin/toybox", "setprop", key, value},
	})
	return err
}

func (n *nodeAgent) startViewerService(ctx context.Context, containerName, clientIP string, maxSize, bitRate int) error {
	clientIP = strings.TrimSpace(clientIP)
	if clientIP == "" {
		clientIP = "127.0.0.1"
	}
	shellCmd := fmt.Sprintf(
		"desired_ip=%s; desired_size=%d; desired_bitrate=%d; public_key=%s; "+
			"current_ip=$(getprop virtroid.viewer.client_ip); "+
			"current_size=$(getprop virtroid.viewer.max_size); "+
			"current_bitrate=$(getprop virtroid.viewer.bit_rate); "+
			"svc=$(getprop init.svc.virtroid_viewer); "+
			"if [ \"$svc\" = \"running\" ] && [ \"$current_ip\" = \"$desired_ip\" ] && [ \"$current_size\" = \"$desired_size\" ] && [ \"$current_bitrate\" = \"$desired_bitrate\" ] && [ -s \"$public_key\" ] && ss -ltn 2>/dev/null | grep -q ':%d'; then exit 0; fi; "+
			"rm -f /data/local/tmp/virtroid-viewer.log \"$public_key\"; "+
			"setprop virtroid.viewer.client_ip %s; "+
			"setprop virtroid.viewer.max_size %d; "+
			"setprop virtroid.viewer.bit_rate %d; "+
			"if [ \"$(getprop init.svc.virtroid_viewer)\" = \"running\" ]; then "+
			"setprop ctl.stop virtroid_viewer >/dev/null 2>&1 || true; "+
			"for i in 1 2 3 4 5 6 7 8 9 10; do "+
			"svc=$(getprop init.svc.virtroid_viewer); "+
			"[ \"$svc\" = \"stopped\" ] && break; "+
			"sleep 1; "+
			"done; "+
			"fi; "+
			"setprop ctl.start virtroid_viewer; "+
			"for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do "+
			"svc=$(getprop init.svc.virtroid_viewer); "+
			"if [ \"$svc\" = \"running\" ] && ss -ltn 2>/dev/null | grep -q ':%d'; then exit 0; fi; "+
			"sleep 1; "+
			"done; "+
			"echo \"service=$svc\"; "+
			"ss -ltn 2>/dev/null || true; "+
			"cat /data/local/tmp/virtroid-viewer.log 2>/dev/null || true; "+
			"exit 1",
		shellEscape(clientIP),
		maxSize,
		bitRate,
		shellEscape(viewerPublicKeyPath),
		encryptedViewerPort,
		shellEscape(clientIP),
		maxSize,
		bitRate,
		encryptedViewerPort,
	)
	output, err := n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
		{"/system/bin/sh", "-c", shellCmd},
		{"sh", "-c", shellCmd},
	})
	if err != nil {
		if trimmed := strings.TrimSpace(output); trimmed != "" {
			return fmt.Errorf("%w: %s", err, trimmed)
		}
		return err
	}
	return nil
}

func (n *nodeAgent) startViewerProcessInContainer(ctx context.Context, containerName string, maxSize, bitRate int) error {
	shellCmd := fmt.Sprintf(
		"setprop ctl.stop virtroid_viewer >/dev/null 2>&1 || true; "+
			"if [ -f /data/local/tmp/virtroid-viewer.pid ]; then kill $(cat /data/local/tmp/virtroid-viewer.pid) >/dev/null 2>&1 || true; rm -f /data/local/tmp/virtroid-viewer.pid; fi; "+
			"setprop virtroid.viewer.client_ip 127.0.0.1; "+
			"setprop virtroid.viewer.max_size %d; "+
			"setprop virtroid.viewer.bit_rate %d; "+
			"echo $$ > /data/local/tmp/virtroid-viewer.pid; "+
			"exec /system/bin/sh %s",
		maxSize,
		bitRate,
		shellEscape(viewerScriptMountPath),
	)
	return n.execInContainerDetachedAny(ctx, containerName, "", nil, [][]string{
		{"/system/bin/sh", "-c", shellCmd},
		{"sh", "-c", shellCmd},
	})
}

func (n *nodeAgent) stopAndRemoveContainer(ctx context.Context, containerName string) error {
	if err := n.stopContainer(ctx, containerName); err != nil && !errors.Is(err, errContainerNotFound) {
		return err
	}
	return n.removeContainer(ctx, containerName)
}

func (n *nodeAgent) stopContainer(ctx context.Context, containerName string) error {
	inspect, err := n.inspectContainer(ctx, containerName)
	if err != nil {
		return err
	}

	if inspect.State.Running {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/containers/"+url.PathEscape(containerName)+"/stop?t=20", nil)
		if err != nil {
			return err
		}
		resp, err := n.docker.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified && resp.StatusCode != http.StatusNotFound {
			payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("docker stop failed: status=%d body=%s", resp.StatusCode, string(payload))
		}
	}
	return nil
}

func (n *nodeAgent) removeContainer(ctx context.Context, containerName string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://docker/containers/"+url.PathEscape(containerName)+"?force=true", nil)
	if err != nil {
		return err
	}
	resp, err := n.docker.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errContainerNotFound
	}
	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker remove failed: status=%d body=%s", resp.StatusCode, string(payload))
	}

	return nil
}

func (n *nodeAgent) execInContainerDetached(ctx context.Context, containerName string, user string, env []string, cmd []string) error {
	payload, err := json.Marshal(map[string]any{
		"AttachStdout": false,
		"AttachStderr": false,
		"Cmd":          cmd,
		"Env":          env,
		"User":         user,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://docker/containers/"+url.PathEscape(containerName)+"/exec",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.docker.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker exec create failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return err
	}
	if created.ID == "" {
		return errors.New("docker exec create returned empty id")
	}

	startPayload, err := json.Marshal(map[string]any{
		"Detach": true,
		"Tty":    false,
	})
	if err != nil {
		return err
	}

	startReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://docker/exec/"+url.PathEscape(created.ID)+"/start",
		bytes.NewReader(startPayload),
	)
	if err != nil {
		return err
	}
	startReq.Header.Set("Content-Type", "application/json")

	startResp, err := n.docker.Do(startReq)
	if err != nil {
		return err
	}
	defer startResp.Body.Close()

	if startResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(startResp.Body, 4096))
		return fmt.Errorf("docker exec start failed: status=%d body=%s", startResp.StatusCode, string(body))
	}

	return nil
}

func (n *nodeAgent) createDockerExec(ctx context.Context, containerName string, user string, env []string, attachStdin bool, attachStderr bool, tty bool, cmd []string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"AttachStdout": true,
		"AttachStderr": attachStderr,
		"AttachStdin":  attachStdin,
		"Cmd":          cmd,
		"Env":          env,
		"Tty":          tty,
		"User":         user,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://docker/containers/"+url.PathEscape(containerName)+"/exec",
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.docker.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("docker exec create failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("docker exec create returned empty id")
	}
	return created.ID, nil
}

type dockerAttachConn struct {
	net.Conn
	reader *bufio.Reader
	buf    bytes.Buffer
}

func (c *dockerAttachConn) Read(p []byte) (int, error) {
	for c.buf.Len() == 0 {
		header := make([]byte, 8)
		if _, err := io.ReadFull(c.reader, header); err != nil {
			return 0, err
		}

		streamType := header[0]
		frameSize := int(header[4])<<24 | int(header[5])<<16 | int(header[6])<<8 | int(header[7])
		if frameSize < 0 {
			return 0, fmt.Errorf("invalid docker attach frame size: %d", frameSize)
		}
		if frameSize == 0 {
			continue
		}

		payload := make([]byte, frameSize)
		if _, err := io.ReadFull(c.reader, payload); err != nil {
			return 0, err
		}

		switch streamType {
		case 1:
			c.buf.Write(payload)
		case 2:
			log.Printf("viewer tunnel stderr from %s", strings.TrimSpace(string(payload)))
		default:
			log.Printf("viewer tunnel ignored docker stream type=%d size=%d", streamType, frameSize)
		}
	}
	return c.buf.Read(p)
}

func (n *nodeAgent) openViewerTunnel(ctx context.Context, containerName string) (net.Conn, error) {
	execID, err := n.createDockerExec(
		ctx,
		containerName,
		"",
		nil,
		true,
		false,
		false,
		[]string{"/system/bin/sh", "-c", fmt.Sprintf("toybox nc 127.0.0.1 %[1]d || /system/bin/toybox nc 127.0.0.1 %[1]d || nc 127.0.0.1 %[1]d", encryptedViewerPort)},
	)
	if err != nil {
		return nil, err
	}

	unixConn, err := net.DialTimeout("unix", "/var/run/docker.sock", 5*time.Second)
	if err != nil {
		return nil, err
	}

	body := `{"Detach":false,"Tty":false}`
	request := fmt.Sprintf(
		"POST /v1.50/exec/%s/start HTTP/1.1\r\nHost: docker\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		url.PathEscape(execID),
		len(body),
		body,
	)
	if _, err := unixConn.Write([]byte(request)); err != nil {
		unixConn.Close()
		return nil, err
	}

	reader := bufio.NewReader(unixConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		unixConn.Close()
		return nil, err
	}
	if !strings.Contains(statusLine, "101") && !strings.Contains(statusLine, "200") {
		headers, _ := io.ReadAll(io.LimitReader(reader, 4096))
		unixConn.Close()
		return nil, fmt.Errorf("docker exec start rejected: %s %s", strings.TrimSpace(statusLine), strings.TrimSpace(string(headers)))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			unixConn.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}

	return &dockerAttachConn{Conn: unixConn, reader: reader}, nil
}

func (n *nodeAgent) execInContainerDetachedAny(ctx context.Context, containerName string, user string, env []string, candidates [][]string) error {
	if len(candidates) == 0 {
		return errors.New("no exec command variants provided")
	}

	var lastErr error
	for _, cmd := range candidates {
		if err := n.execInContainerDetached(ctx, containerName, user, env, cmd); err == nil {
			return nil
		} else {
			lastErr = fmt.Errorf("%s: %w", strings.Join(cmd, " "), err)
		}
	}

	return lastErr
}

func (n *nodeAgent) execInContainerCapture(ctx context.Context, containerName string, user string, env []string, cmd []string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
		"Env":          env,
		"Tty":          true,
		"User":         user,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://docker/containers/"+url.PathEscape(containerName)+"/exec",
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.docker.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("docker exec create failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("docker exec create returned empty id")
	}

	startPayload, err := json.Marshal(map[string]any{
		"Detach": false,
		"Tty":    true,
	})
	if err != nil {
		return "", err
	}

	startReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://docker/exec/"+url.PathEscape(created.ID)+"/start",
		bytes.NewReader(startPayload),
	)
	if err != nil {
		return "", err
	}
	startReq.Header.Set("Content-Type", "application/json")

	startResp, err := n.docker.Do(startReq)
	if err != nil {
		return "", err
	}
	defer startResp.Body.Close()

	output, readErr := io.ReadAll(io.LimitReader(startResp.Body, 64*1024))
	if startResp.StatusCode >= 300 {
		return string(output), fmt.Errorf("docker exec start failed: status=%d body=%s", startResp.StatusCode, string(output))
	}
	if readErr != nil {
		return string(output), readErr
	}

	inspectReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://docker/exec/"+url.PathEscape(created.ID)+"/json",
		nil,
	)
	if err != nil {
		return string(output), err
	}

	inspectResp, err := n.docker.Do(inspectReq)
	if err != nil {
		return string(output), err
	}
	defer inspectResp.Body.Close()

	if inspectResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(inspectResp.Body, 4096))
		return string(output), fmt.Errorf("docker exec inspect failed: status=%d body=%s", inspectResp.StatusCode, string(body))
	}

	var inspect struct {
		ExitCode int  `json:"ExitCode"`
		Running  bool `json:"Running"`
	}
	if err := json.NewDecoder(inspectResp.Body).Decode(&inspect); err != nil {
		return string(output), err
	}
	if inspect.Running {
		return string(output), errors.New("docker exec capture still running")
	}
	if inspect.ExitCode != 0 {
		return string(output), fmt.Errorf("docker exec exited with code %d", inspect.ExitCode)
	}

	return string(output), nil
}

func (n *nodeAgent) execInContainerCaptureAny(ctx context.Context, containerName string, user string, env []string, candidates [][]string) (string, error) {
	if len(candidates) == 0 {
		return "", errors.New("no exec command variants provided")
	}

	var lastErr error
	var lastOutput string
	for _, cmd := range candidates {
		output, err := n.execInContainerCapture(ctx, containerName, user, env, cmd)
		if err == nil {
			return output, nil
		}
		lastErr = fmt.Errorf("%s: %w", strings.Join(cmd, " "), err)
		lastOutput = output
	}

	return lastOutput, lastErr
}

func (n *nodeAgent) copyFileToContainer(ctx context.Context, containerName string, targetPath string, contents []byte, mode int64) error {
	parentDir := path.Dir(targetPath)
	fileName := path.Base(targetPath)

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{
		Name: fileName,
		Mode: mode,
		Size: int64(len(contents)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(contents); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		"http://docker/containers/"+url.PathEscape(containerName)+"/archive?path="+url.QueryEscape(parentDir),
		bytes.NewReader(archive.Bytes()),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")

	resp, err := n.docker.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker archive copy failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	return nil
}

func (n *nodeAgent) waitForViewerPort(ctx context.Context, runtime runtimeAssignment, containerName string) error {
	const attempts = 30
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		output, err := n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
			{"ss", "-ltn"},
			{"/system/bin/ss", "-ltn"},
			{"/bin/ss", "-ltn"},
			{"toybox", "ss", "-ltn"},
			{"/system/bin/toybox", "ss", "-ltn"},
			{"netstat", "-ltn"},
			{"toybox", "netstat", "-ltn"},
			{"/system/bin/toybox", "netstat", "-ltn"},
		})
		if err == nil && strings.Contains(output, fmt.Sprintf(":%d", encryptedViewerPort)) {
			return nil
		}
		if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			attempt = attempts
		case <-time.After(1 * time.Second):
		}
	}

	var diagnostics []string
	if hostLog, err := os.ReadFile(n.viewerCommandLogPath(runtime.ID)); err == nil {
		if trimmed := strings.TrimSpace(string(hostLog)); trimmed != "" {
			diagnostics = append(diagnostics, "host viewer log: "+summarizeLogOutput(trimmed))
		}
	}
	if guestLog, guestErr := n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
		{"cat", "/data/local/tmp/virtroid-viewer.log"},
		{"/system/bin/cat", "/data/local/tmp/virtroid-viewer.log"},
		{"toybox", "cat", "/data/local/tmp/virtroid-viewer.log"},
		{"/system/bin/toybox", "cat", "/data/local/tmp/virtroid-viewer.log"},
	}); guestErr == nil && strings.TrimSpace(guestLog) != "" {
		diagnostics = append(diagnostics, "guest viewer log: "+summarizeLogOutput(guestLog))
	}
	if logOutput, logErr := n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
		{"logcat", "-b", "all", "-d", "-t", "200"},
		{"/system/bin/logcat", "-b", "all", "-d", "-t", "200"},
		{"toybox", "logcat", "-b", "all", "-d", "-t", "200"},
		{"/system/bin/toybox", "logcat", "-b", "all", "-d", "-t", "200"},
	}); logErr == nil && strings.TrimSpace(logOutput) != "" {
		diagnostics = append(diagnostics, "logcat: "+summarizeLogOutput(logOutput))
	}
	if len(diagnostics) > 0 {
		return fmt.Errorf("encrypted viewer port %d did not open after %d seconds: %s", encryptedViewerPort, attempts, strings.Join(diagnostics, " | "))
	}
	if lastErr != nil {
		return fmt.Errorf("encrypted viewer port %d did not open after %d seconds: %w", encryptedViewerPort, attempts, lastErr)
	}
	return fmt.Errorf("encrypted viewer port %d did not open after %d seconds", encryptedViewerPort, attempts)
}

func (n *nodeAgent) androidBootCompleted(ctx context.Context, containerName string) (bool, error) {
	sysBootCompleted, sysErr := n.getProp(ctx, containerName, "sys.boot_completed")
	if strings.TrimSpace(sysBootCompleted) == "1" {
		return true, nil
	}

	devBootCompleted, devErr := n.getProp(ctx, containerName, "dev.bootcomplete")
	if strings.TrimSpace(devBootCompleted) == "1" {
		return true, nil
	}

	if sysErr != nil && devErr != nil {
		return false, fmt.Errorf("read Android boot properties: %v | %v", sysErr, devErr)
	}

	return false, nil
}

func (n *nodeAgent) ensureAndroidInteractive(ctx context.Context, containerName string) (bool, string, error) {
	output, err := n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
		{"/system/bin/sh", "-c", androidInteractiveProbeScript},
		{"/bin/sh", "-c", androidInteractiveProbeScript},
		{"sh", "-c", androidInteractiveProbeScript},
	})
	ready, detail := parseAndroidInteractiveProbe(output)
	if err != nil {
		return false, detail, err
	}
	return ready, detail, nil
}

func parseAndroidInteractiveProbe(output string) (bool, string) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false, "empty readiness probe output"
	}
	detail := summarizeLogOutput(trimmed)
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "virtroid_ready=1"):
			return true, line
		case strings.HasPrefix(line, "virtroid_ready=0"):
			return false, line
		}
	}
	return false, detail
}

func (n *nodeAgent) getProp(ctx context.Context, containerName string, prop string) (string, error) {
	return n.execInContainerCaptureAny(ctx, containerName, "", nil, [][]string{
		{"getprop", prop},
		{"/system/bin/getprop", prop},
		{"/bin/getprop", prop},
		{"toybox", "getprop", prop},
		{"/system/bin/toybox", "getprop", prop},
	})
}

func summarizeLogOutput(output string) string {
	const maxLen = 2000
	trimmed := strings.TrimSpace(output)
	if len(trimmed) <= maxLen {
		return trimmed
	}
	return trimmed[len(trimmed)-maxLen:]
}

func shellEscape(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func containerIPAddress(inspect dockerInspectResponse, preferredNetwork string) string {
	if preferredNetwork = strings.TrimSpace(preferredNetwork); preferredNetwork != "" {
		if network, ok := inspect.NetworkSettings.Networks[preferredNetwork]; ok {
			if ip := strings.TrimSpace(network.IPAddress); ip != "" {
				return ip
			}
		}
	}
	if ip := strings.TrimSpace(inspect.NetworkSettings.IPAddress); ip != "" {
		return ip
	}
	for _, network := range inspect.NetworkSettings.Networks {
		if ip := strings.TrimSpace(network.IPAddress); ip != "" {
			return ip
		}
	}
	return ""
}

func containerGateway(inspect dockerInspectResponse, preferredNetwork string) string {
	if preferredNetwork = strings.TrimSpace(preferredNetwork); preferredNetwork != "" {
		if network, ok := inspect.NetworkSettings.Networks[preferredNetwork]; ok {
			if gateway := strings.TrimSpace(network.Gateway); gateway != "" {
				return gateway
			}
		}
	}
	for _, network := range inspect.NetworkSettings.Networks {
		if gateway := strings.TrimSpace(network.Gateway); gateway != "" {
			return gateway
		}
	}
	return ""
}

func defaultGatewayIP() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}

		raw, err := hex.DecodeString(fields[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String()
	}

	return ""
}

func dockerHTTPClient() *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   5 * time.Minute,
	}
}

func containerNameForRuntime(runtimeID string) string {
	sanitized := strings.ReplaceAll(runtimeID, "-", "")
	if len(sanitized) > 16 {
		sanitized = sanitized[:16]
	}
	return "virtroid-" + sanitized
}

func adbPortForRuntime(runtimeID string) int {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(runtimeID))
	return 20000 + int(hasher.Sum32()%20000)
}

func gpuAccelerationAvailable() bool {
	if fileExists("/dev/nvidia0") {
		return true
	}
	matches, err := filepath.Glob("/dev/dri/renderD*")
	return err == nil && len(matches) > 0
}

func dockerSocketAvailable() bool {
	return fileExists("/var/run/docker.sock")
}

func binderAvailable() bool {
	candidates := []string{
		"/dev/binder",
		"/dev/binderfs",
		"/dev/binderfs/binder",
	}

	for _, path := range candidates {
		if fileExists(path) {
			return true
		}
	}

	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func stringPtr(value string) *string {
	return &value
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
