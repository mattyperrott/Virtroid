package config

import (
	"os"
	"strconv"
	"time"
)

type ServerConfig struct {
	AppEnv                      string
	BindAddr                    string
	DatabaseURL                 string
	PublicBaseURL               string
	PublicRelayURL              string
	NodeSharedSecret            string
	BootstrapToken              string
	BootstrapRateLimitPerMinute int
	BootstrapMaxBodyBytes       int64
}

type NodeConfig struct {
	NodeID             string
	NodeName           string
	BindAddr           string
	RelayPort          int
	ControlPlaneURL    string
	AdvertiseAddr      string
	ADBPath            string
	ADBHost            string
	BlobStoreKind      string
	RenterdWorkerURL   string
	RenterdPassword    string
	RenterdBucket      string
	RenterdMinShards   int
	RenterdTotalShards int
	RenterdContractSet string
	DockerNetworkName  string
	SharedSecret       string
	ViewerCryptPath    string
	HeartbeatInterval  time.Duration
	ReconcileInterval  time.Duration
	RuntimeRoot        string
}

func LoadServer() ServerConfig {
	bootstrapRateLimit, err := parseEnvInt("BOOTSTRAP_RATE_LIMIT_PER_MINUTE", 5)
	if err != nil {
		bootstrapRateLimit = 5
	}
	bootstrapMaxBodyBytes, err := parseEnvInt("BOOTSTRAP_MAX_BODY_BYTES", 32768)
	if err != nil {
		bootstrapMaxBodyBytes = 32768
	}

	return ServerConfig{
		AppEnv:                      envOrDefault("APP_ENV", "development"),
		BindAddr:                    envOrDefault("BIND_ADDR", ":8080"),
		DatabaseURL:                 envOrDefault("DATABASE_URL", "postgres://virtdroid:virtdroid@127.0.0.1:5432/virtdroid?sslmode=disable"),
		PublicBaseURL:               envOrDefault("PUBLIC_BASE_URL", "http://127.0.0.1:8080"),
		PublicRelayURL:              os.Getenv("PUBLIC_RELAY_URL"),
		NodeSharedSecret:            os.Getenv("NODE_SHARED_SECRET"),
		BootstrapToken:              os.Getenv("BOOTSTRAP_INVITE_TOKEN"),
		BootstrapRateLimitPerMinute: bootstrapRateLimit,
		BootstrapMaxBodyBytes:       int64(bootstrapMaxBodyBytes),
	}
}

func LoadNode() NodeConfig {
	hostname, _ := os.Hostname()
	defaultHeartbeat, _ := time.ParseDuration("30s")
	defaultReconcile, _ := time.ParseDuration("10s")

	heartbeatInterval, err := time.ParseDuration(envOrDefault("NODE_HEARTBEAT_INTERVAL", "30s"))
	if err != nil {
		heartbeatInterval = defaultHeartbeat
	}

	reconcileInterval, err := time.ParseDuration(envOrDefault("NODE_RECONCILE_INTERVAL", "10s"))
	if err != nil {
		reconcileInterval = defaultReconcile
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

	return NodeConfig{
		NodeID:             envOrDefault("NODE_ID", hostname),
		NodeName:           envOrDefault("NODE_NAME", hostname),
		BindAddr:           envOrDefault("NODE_BIND_ADDR", ":8090"),
		RelayPort:          relayPort,
		ControlPlaneURL:    envOrDefault("CONTROL_PLANE_URL", "http://127.0.0.1:8080"),
		AdvertiseAddr:      envOrDefault("NODE_ADVERTISE_ADDR", hostname),
		ADBPath:            envOrDefault("NODE_ADB_PATH", "adb"),
		ADBHost:            envOrDefault("NODE_ADB_HOST", ""),
		BlobStoreKind:      envOrDefault("NODE_BLOB_STORE_KIND", "local-disk"),
		RenterdWorkerURL:   envOrDefault("NODE_SIA_RENTERD_WORKER_URL", ""),
		RenterdPassword:    os.Getenv("NODE_SIA_RENTERD_PASSWORD"),
		RenterdBucket:      envOrDefault("NODE_SIA_RENTERD_BUCKET", "virtdroid"),
		RenterdMinShards:   renterdMinShards,
		RenterdTotalShards: renterdTotalShards,
		RenterdContractSet: envOrDefault("NODE_SIA_RENTERD_CONTRACT_SET", ""),
		DockerNetworkName:  envOrDefault("NODE_DOCKER_NETWORK", ""),
		SharedSecret:       os.Getenv("NODE_SHARED_SECRET"),
		ViewerCryptPath:    envOrDefault("NODE_VIEWER_CRYPT_PATH", "/usr/local/bin/virtdroid-viewercrypt"),
		HeartbeatInterval:  heartbeatInterval,
		ReconcileInterval:  reconcileInterval,
		RuntimeRoot:        envOrDefault("NODE_RUNTIME_ROOT", "/srv/virtdroid/runtimes"),
	}
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
