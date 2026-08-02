package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type ServerConfig struct {
	AppEnv                           string
	BindAddr                         string
	DatabaseURL                      string
	PublicBaseURL                    string
	PublicRelayURL                   string
	ControlPlaneCallbackPrivateKey   string
	NodeRegistrationSecret           string
	NodeDevelopmentEnrollmentEnabled bool
	NodeAllowedAdvertiseAddrs        []string
	BootstrapEnabled                 bool
	BootstrapRequireInvite           bool
	BootstrapAutoIssueInvite         bool
	BootstrapRateLimitPerMinute      int
	BootstrapMaxBodyBytes            int64
	TrustProxyHeaders                bool
	SecurityEventRateLimitPerMinute  int
	SecurityEventRetention           time.Duration
	RuntimeLogRetention              time.Duration
	SessionReaperInterval            time.Duration
	ActiveSessionTimeout             time.Duration
	RuntimeIdleTimeout               time.Duration
	AppCatalogSyncEnabled            bool
	AppCatalogSyncURL                string
	AppCatalogSyncSHA256             string
	AppCatalogSyncInterval           time.Duration
	AppCatalogSyncMaxApps            int
}

type NodeConfig struct {
	NodeID                        string
	NodeName                      string
	BindAddr                      string
	RelayPort                     int
	ControlPlaneURL               string
	AdvertiseAddr                 string
	ADBPath                       string
	ADBHost                       string
	BlobStoreKind                 string
	RenterdWorkerURL              string
	RenterdPassword               string
	RenterdBucket                 string
	RenterdWalletAddress          string
	RenterdMinShards              int
	RenterdTotalShards            int
	RenterdContractSet            string
	DockerNetworkName             string
	ControlPlaneCallbackPublicKey string
	RegistrationSecret            string
	PrivateKey                    string
	AppAPKDir                     string
	AppManifestPath               string
	DefaultAppPackages            []string
	ViewerCryptPath               string
	HeartbeatInterval             time.Duration
	ReconcileInterval             time.Duration
	BlobPreflightInterval         time.Duration
	RuntimeRoot                   string
	MinFreeDiskBytes              int64
	MinFreeDiskPercent            int
}

func LoadServer() ServerConfig {
	appEnv := envOrDefault("APP_ENV", "development")
	developmentEnrollmentEnabled := parseEnvBool("NODE_DEVELOPMENT_ENROLLMENT_ENABLED", strings.EqualFold(appEnv, "development"))
	if !strings.EqualFold(appEnv, "development") {
		// Shared-secret node enrollment is a local-development convenience only.
		// Production authorization always comes from the approved-node registry.
		developmentEnrollmentEnabled = false
	}
	bootstrapRequireInvite := parseEnvBool("BOOTSTRAP_REQUIRE_INVITE", true)
	if !strings.EqualFold(appEnv, "development") {
		// Production identity creation is never anonymous, even if an unsafe
		// environment override bypasses the deployment-script validation.
		bootstrapRequireInvite = true
	}
	bootstrapRateLimit, err := parseEnvInt("BOOTSTRAP_RATE_LIMIT_PER_MINUTE", 5)
	if err != nil {
		bootstrapRateLimit = 5
	}
	bootstrapMaxBodyBytes, err := parseEnvInt("BOOTSTRAP_MAX_BODY_BYTES", 32768)
	if err != nil {
		bootstrapMaxBodyBytes = 32768
	}
	securityEventRateLimit, err := parseEnvInt("SECURITY_EVENT_RATE_LIMIT_PER_MINUTE", 120)
	if err != nil {
		securityEventRateLimit = 120
	}
	securityEventRetention := parseEnvDuration("SECURITY_EVENT_RETENTION", 7*24*time.Hour)
	runtimeLogRetention := parseEnvDuration("RUNTIME_LOG_RETENTION", 30*24*time.Hour)
	sessionReaperInterval := parseEnvDuration("SESSION_REAPER_INTERVAL", 30*time.Second)
	activeSessionTimeout := parseEnvDuration("ACTIVE_SESSION_TIMEOUT", 2*time.Minute)
	runtimeIdleTimeout := parseEnvDuration("RUNTIME_IDLE_TIMEOUT", 3*time.Minute)
	appCatalogSyncInterval := parseEnvDuration("APP_CATALOG_SYNC_INTERVAL", 12*time.Hour)
	appCatalogSyncMaxApps, err := parseEnvInt("APP_CATALOG_SYNC_MAX_APPS", 1500)
	if err != nil {
		appCatalogSyncMaxApps = 1500
	}

	return ServerConfig{
		AppEnv:                           appEnv,
		BindAddr:                         envOrDefault("BIND_ADDR", ":8080"),
		DatabaseURL:                      envOrDefault("DATABASE_URL", "postgres://virtroid:virtroid@127.0.0.1:5432/virtroid?sslmode=disable"),
		PublicBaseURL:                    envOrDefault("PUBLIC_BASE_URL", "http://127.0.0.1:8080"),
		PublicRelayURL:                   os.Getenv("PUBLIC_RELAY_URL"),
		ControlPlaneCallbackPrivateKey:   strings.TrimSpace(os.Getenv("CONTROL_PLANE_CALLBACK_PRIVATE_KEY_B64")),
		NodeRegistrationSecret:           strings.TrimSpace(os.Getenv("NODE_REGISTRATION_SECRET")),
		NodeDevelopmentEnrollmentEnabled: developmentEnrollmentEnabled,
		NodeAllowedAdvertiseAddrs:        parseEnvCSV("NODE_ALLOWED_ADVERTISE_ADDRS", ""),
		BootstrapEnabled:                 parseEnvBool("BOOTSTRAP_ENABLED", true),
		BootstrapRequireInvite:           bootstrapRequireInvite,
		BootstrapAutoIssueInvite:         parseEnvBool("BOOTSTRAP_AUTO_ISSUE_INVITE", true),
		BootstrapRateLimitPerMinute:      bootstrapRateLimit,
		BootstrapMaxBodyBytes:            int64(bootstrapMaxBodyBytes),
		TrustProxyHeaders:                parseEnvBool("TRUST_PROXY_HEADERS", false),
		SecurityEventRateLimitPerMinute:  securityEventRateLimit,
		SecurityEventRetention:           securityEventRetention,
		RuntimeLogRetention:              runtimeLogRetention,
		SessionReaperInterval:            sessionReaperInterval,
		ActiveSessionTimeout:             activeSessionTimeout,
		RuntimeIdleTimeout:               runtimeIdleTimeout,
		AppCatalogSyncEnabled:            parseEnvBool("APP_CATALOG_SYNC_ENABLED", false),
		AppCatalogSyncURL:                envOrDefault("APP_CATALOG_SYNC_URL", "https://f-droid.org/repo/index-v2.json"),
		AppCatalogSyncSHA256:             strings.TrimSpace(os.Getenv("APP_CATALOG_SYNC_SHA256")),
		AppCatalogSyncInterval:           appCatalogSyncInterval,
		AppCatalogSyncMaxApps:            appCatalogSyncMaxApps,
	}
}

func LoadNode() NodeConfig {
	hostname, _ := os.Hostname()
	defaultHeartbeat, _ := time.ParseDuration("30s")
	defaultReconcile, _ := time.ParseDuration("10s")
	defaultBlobPreflight, _ := time.ParseDuration("5m")
	const defaultMinFreeDiskBytes int64 = 10 << 30
	const defaultMinFreeDiskPercent = 5

	heartbeatInterval, err := time.ParseDuration(envOrDefault("NODE_HEARTBEAT_INTERVAL", "30s"))
	if err != nil {
		heartbeatInterval = defaultHeartbeat
	}

	reconcileInterval, err := time.ParseDuration(envOrDefault("NODE_RECONCILE_INTERVAL", "10s"))
	if err != nil {
		reconcileInterval = defaultReconcile
	}
	blobPreflightInterval, err := time.ParseDuration(envOrDefault("NODE_BLOB_PREFLIGHT_INTERVAL", "5m"))
	if err != nil {
		blobPreflightInterval = defaultBlobPreflight
	}

	relayPort, err := parseEnvInt("NODE_RELAY_PORT", 8090)
	if err != nil {
		relayPort = 8090
	}
	renterdMinShards, err := parseEnvInt("NODE_SIA_RENTERD_MIN_SHARDS", 0)
	if err != nil {
		renterdMinShards = 0
	}
	renterdTotalShards, err := parseEnvInt("NODE_SIA_RENTERD_TOTAL_SHARDS", 0)
	if err != nil {
		renterdTotalShards = 0
	}
	minFreeDiskBytes, err := parseEnvInt64("NODE_MIN_FREE_DISK_BYTES", defaultMinFreeDiskBytes)
	if err != nil || minFreeDiskBytes < 0 {
		minFreeDiskBytes = defaultMinFreeDiskBytes
	}
	minFreeDiskPercent, err := parseEnvInt("NODE_MIN_FREE_DISK_PERCENT", defaultMinFreeDiskPercent)
	if err != nil || minFreeDiskPercent < 0 || minFreeDiskPercent > 100 {
		minFreeDiskPercent = defaultMinFreeDiskPercent
	}
	return NodeConfig{
		NodeID:                        envOrDefault("NODE_ID", hostname),
		NodeName:                      envOrDefault("NODE_NAME", hostname),
		BindAddr:                      envOrDefault("NODE_BIND_ADDR", ":8090"),
		RelayPort:                     relayPort,
		ControlPlaneURL:               envOrDefault("CONTROL_PLANE_URL", "http://127.0.0.1:8080"),
		AdvertiseAddr:                 envOrDefault("NODE_ADVERTISE_ADDR", hostname),
		ADBPath:                       envOrDefault("NODE_ADB_PATH", "adb"),
		ADBHost:                       envOrDefault("NODE_ADB_HOST", ""),
		BlobStoreKind:                 envOrDefault("NODE_BLOB_STORE_KIND", "local-disk"),
		RenterdWorkerURL:              envOrDefault("NODE_SIA_RENTERD_WORKER_URL", ""),
		RenterdPassword:               secretFileOrEnv("NODE_SIA_RENTERD_PASSWORD_FILE", "NODE_SIA_RENTERD_PASSWORD"),
		RenterdBucket:                 envOrDefault("NODE_SIA_RENTERD_BUCKET", "virtroid"),
		RenterdWalletAddress:          strings.TrimSpace(os.Getenv("NODE_SIA_RENTERD_WALLET_ADDRESS")),
		RenterdMinShards:              renterdMinShards,
		RenterdTotalShards:            renterdTotalShards,
		RenterdContractSet:            envOrDefault("NODE_SIA_RENTERD_CONTRACT_SET", ""),
		DockerNetworkName:             envOrDefault("NODE_DOCKER_NETWORK", ""),
		ControlPlaneCallbackPublicKey: strings.TrimSpace(os.Getenv("CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64")),
		RegistrationSecret:            strings.TrimSpace(os.Getenv("NODE_REGISTRATION_SECRET")),
		PrivateKey:                    os.Getenv("NODE_PRIVATE_KEY_B64"),
		AppAPKDir:                     envOrDefault("NODE_APP_APK_DIR", "/srv/virtroid/apks"),
		AppManifestPath:               envOrDefault("NODE_APP_MANIFEST_PATH", "/srv/virtroid/apks/manifest.json"),
		DefaultAppPackages:            parseEnvCSV("NODE_DEFAULT_APP_PACKAGES", "org.fdroid.basic"),
		ViewerCryptPath:               envOrDefault("NODE_VIEWER_CRYPT_PATH", "/usr/local/bin/virtroid-viewercrypt"),
		HeartbeatInterval:             heartbeatInterval,
		ReconcileInterval:             reconcileInterval,
		BlobPreflightInterval:         blobPreflightInterval,
		RuntimeRoot:                   envOrDefault("NODE_RUNTIME_ROOT", "/srv/virtroid/runtimes"),
		MinFreeDiskBytes:              minFreeDiskBytes,
		MinFreeDiskPercent:            minFreeDiskPercent,
	}
}

func secretFileOrEnv(fileKey, valueKey string) string {
	path := strings.TrimSpace(os.Getenv(fileKey))
	if path == "" {
		return os.Getenv(valueKey)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parseEnvInt(key string, fallback int) (int, error) {
	value := envOrDefault(key, "")
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback, err
	}
	return parsed, nil
}

func parseEnvInt64(key string, fallback int64) (int64, error) {
	value := envOrDefault(key, "")
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback, err
	}
	return parsed, nil
}

func parseEnvBool(key string, fallback bool) bool {
	value := envOrDefault(key, "")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseEnvDuration(key string, fallback time.Duration) time.Duration {
	value := envOrDefault(key, "")
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseEnvCSV(key, fallback string) []string {
	value := envOrDefault(key, fallback)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
