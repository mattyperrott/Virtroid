package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapAndNodeAdvertiseDefaultsFailClosedInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("BOOTSTRAP_ENABLED", "")
	t.Setenv("BOOTSTRAP_REQUIRE_INVITE", "")
	t.Setenv("NODE_DEVELOPMENT_ENROLLMENT_ENABLED", "true")
	t.Setenv("NODE_ALLOWED_ADVERTISE_ADDRS", " virtnoded, node.internal,virtnoded ")

	cfg := LoadServer()
	if !cfg.BootstrapEnabled {
		t.Fatal("bootstrap endpoint disabled by default in production")
	}
	if !cfg.BootstrapRequireInvite {
		t.Fatal("anonymous bootstrap enabled by default in production")
	}
	if !cfg.BootstrapAutoIssueInvite {
		t.Fatal("automatic one-time bootstrap invitation disabled by default in production")
	}
	if cfg.NodeDevelopmentEnrollmentEnabled {
		t.Fatal("development node enrollment enabled in production")
	}
	if len(cfg.NodeAllowedAdvertiseAddrs) != 2 || cfg.NodeAllowedAdvertiseAddrs[0] != "virtnoded" || cfg.NodeAllowedAdvertiseAddrs[1] != "node.internal" {
		t.Fatalf("NodeAllowedAdvertiseAddrs = %#v, want trimmed unique entries", cfg.NodeAllowedAdvertiseAddrs)
	}
}

func TestNodeDevelopmentEnrollmentDefaultsToDevelopmentOnly(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("NODE_DEVELOPMENT_ENROLLMENT_ENABLED", "")
	if cfg := LoadServer(); !cfg.NodeDevelopmentEnrollmentEnabled {
		t.Fatal("development node enrollment disabled by default in development")
	}

	t.Setenv("NODE_DEVELOPMENT_ENROLLMENT_ENABLED", "false")
	if cfg := LoadServer(); cfg.NodeDevelopmentEnrollmentEnabled {
		t.Fatal("development node enrollment ignored explicit opt-out")
	}
}

func TestNodeRegistrationSecretNeverFallsBackToCallbackSharedSecret(t *testing.T) {
	t.Setenv("NODE_SHARED_SECRET", "callback-secret")
	t.Setenv("NODE_REGISTRATION_SECRET", "")
	if cfg := LoadServer(); cfg.NodeRegistrationSecret != "" {
		t.Fatalf("server registration secret = %q, want no shared-secret fallback", cfg.NodeRegistrationSecret)
	}
	if cfg := LoadNode(); cfg.RegistrationSecret != "" {
		t.Fatalf("node registration secret = %q, want no shared-secret fallback", cfg.RegistrationSecret)
	}

	t.Setenv("NODE_REGISTRATION_SECRET", "  explicit-development-secret  ")
	if cfg := LoadServer(); cfg.NodeRegistrationSecret != "explicit-development-secret" {
		t.Fatalf("server registration secret = %q, want explicit trimmed value", cfg.NodeRegistrationSecret)
	}
	if cfg := LoadNode(); cfg.RegistrationSecret != "explicit-development-secret" {
		t.Fatalf("node registration secret = %q, want explicit trimmed value", cfg.RegistrationSecret)
	}
}

func TestBootstrapDefaultsToInviteGatedProductionAndHonorsOverride(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("BOOTSTRAP_ENABLED", "")
	t.Setenv("BOOTSTRAP_REQUIRE_INVITE", "")
	t.Setenv("BOOTSTRAP_AUTO_ISSUE_INVITE", "")
	if cfg := LoadServer(); !cfg.BootstrapEnabled || !cfg.BootstrapRequireInvite {
		t.Fatal("bootstrap did not default to an enabled, invite-gated endpoint in development")
	}
	if cfg := LoadServer(); !cfg.BootstrapAutoIssueInvite {
		t.Fatal("bootstrap did not default to automatic invitation issuance")
	}

	t.Setenv("APP_ENV", "production")
	t.Setenv("BOOTSTRAP_ENABLED", "")
	t.Setenv("BOOTSTRAP_REQUIRE_INVITE", "false")
	if cfg := LoadServer(); !cfg.BootstrapEnabled || !cfg.BootstrapRequireInvite {
		t.Fatal("production bootstrap did not enforce an enabled, invite-gated endpoint")
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("BOOTSTRAP_ENABLED", "false")
	if cfg := LoadServer(); cfg.BootstrapEnabled {
		t.Fatal("bootstrap did not honor explicit development opt-out")
	}

	t.Setenv("BOOTSTRAP_ENABLED", "true")
	t.Setenv("BOOTSTRAP_REQUIRE_INVITE", "false")
	t.Setenv("BOOTSTRAP_AUTO_ISSUE_INVITE", "false")
	if cfg := LoadServer(); cfg.BootstrapRequireInvite {
		t.Fatal("development bootstrap invite requirement did not honor explicit opt-out")
	}
	if cfg := LoadServer(); cfg.BootstrapAutoIssueInvite {
		t.Fatal("development automatic invitation issuance did not honor explicit opt-out")
	}
}

func TestRuntimeLogRetentionDefaultsAndOverride(t *testing.T) {
	t.Setenv("RUNTIME_LOG_RETENTION", "")
	if cfg := LoadServer(); cfg.RuntimeLogRetention != 30*24*time.Hour {
		t.Fatalf("RuntimeLogRetention = %s, want 720h", cfg.RuntimeLogRetention)
	}

	t.Setenv("RUNTIME_LOG_RETENTION", "168h")
	if cfg := LoadServer(); cfg.RuntimeLogRetention != 7*24*time.Hour {
		t.Fatalf("RuntimeLogRetention = %s, want 168h", cfg.RuntimeLogRetention)
	}
}

func TestAppCatalogSyncIsFailClosedByDefaultAndLoadsExplicitPin(t *testing.T) {
	t.Setenv("APP_CATALOG_SYNC_ENABLED", "")
	t.Setenv("APP_CATALOG_SYNC_SHA256", "  abc123  ")
	cfg := LoadServer()
	if cfg.AppCatalogSyncEnabled {
		t.Fatal("app catalog sync enabled without an explicit opt-in")
	}
	if cfg.AppCatalogSyncSHA256 != "abc123" {
		t.Fatalf("AppCatalogSyncSHA256 = %q, want trimmed value", cfg.AppCatalogSyncSHA256)
	}

	t.Setenv("APP_CATALOG_SYNC_ENABLED", "true")
	if cfg := LoadServer(); !cfg.AppCatalogSyncEnabled {
		t.Fatal("app catalog sync did not honor explicit opt-in")
	}
}

func TestRuntimeNotificationRateLimitDefaultsAndFailsSafe(t *testing.T) {
	t.Setenv("RUNTIME_NOTIFICATION_RATE_LIMIT_PER_MINUTE", "")
	if got := LoadServer().RuntimeNotificationRateLimit; got != 120 {
		t.Fatalf("default runtime notification rate limit = %d, want 120", got)
	}

	t.Setenv("RUNTIME_NOTIFICATION_RATE_LIMIT_PER_MINUTE", "45")
	if got := LoadServer().RuntimeNotificationRateLimit; got != 45 {
		t.Fatalf("configured runtime notification rate limit = %d, want 45", got)
	}

	t.Setenv("RUNTIME_NOTIFICATION_RATE_LIMIT_PER_MINUTE", "0")
	if got := LoadServer().RuntimeNotificationRateLimit; got != 120 {
		t.Fatalf("unsafe runtime notification rate limit = %d, want safe default", got)
	}
}

func TestRenterdPasswordLoadsFromMountedSecretFile(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "renterd-api-password")
	if err := os.WriteFile(secretPath, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_SIA_RENTERD_PASSWORD_FILE", secretPath)
	t.Setenv("NODE_SIA_RENTERD_PASSWORD", "environment-secret")

	if cfg := LoadNode(); cfg.RenterdPassword != "file-secret" {
		t.Fatalf("RenterdPassword = %q, want mounted file value", cfg.RenterdPassword)
	}
}

func TestRenterdPasswordFileFailsClosed(t *testing.T) {
	t.Setenv("NODE_SIA_RENTERD_PASSWORD_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("NODE_SIA_RENTERD_PASSWORD", "environment-secret")

	if cfg := LoadNode(); cfg.RenterdPassword != "" {
		t.Fatalf("RenterdPassword = %q, want no environment fallback for a configured missing secret file", cfg.RenterdPassword)
	}
}

func TestNodeDiskHeadroomDefaultsAndOverrides(t *testing.T) {
	t.Setenv("NODE_MIN_FREE_DISK_BYTES", "")
	t.Setenv("NODE_MIN_FREE_DISK_PERCENT", "")
	cfg := LoadNode()
	if cfg.MinFreeDiskBytes != 10<<30 {
		t.Fatalf("MinFreeDiskBytes = %d, want %d", cfg.MinFreeDiskBytes, int64(10<<30))
	}
	if cfg.MinFreeDiskPercent != 5 {
		t.Fatalf("MinFreeDiskPercent = %d, want 5", cfg.MinFreeDiskPercent)
	}

	t.Setenv("NODE_MIN_FREE_DISK_BYTES", "21474836480")
	t.Setenv("NODE_MIN_FREE_DISK_PERCENT", "8")
	cfg = LoadNode()
	if cfg.MinFreeDiskBytes != 20<<30 {
		t.Fatalf("MinFreeDiskBytes = %d, want %d", cfg.MinFreeDiskBytes, int64(20<<30))
	}
	if cfg.MinFreeDiskPercent != 8 {
		t.Fatalf("MinFreeDiskPercent = %d, want 8", cfg.MinFreeDiskPercent)
	}
}

func TestNodeDiskHeadroomInvalidValuesFailToSafeDefaults(t *testing.T) {
	for _, test := range []struct {
		name    string
		bytes   string
		percent string
	}{
		{name: "negative", bytes: "-1", percent: "-1"},
		{name: "overflow", bytes: "9223372036854775808", percent: "101"},
		{name: "malformed", bytes: "ten-gib", percent: "five"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NODE_MIN_FREE_DISK_BYTES", test.bytes)
			t.Setenv("NODE_MIN_FREE_DISK_PERCENT", test.percent)
			cfg := LoadNode()
			if cfg.MinFreeDiskBytes != 10<<30 || cfg.MinFreeDiskPercent != 5 {
				t.Fatalf(
					"disk headroom = %d bytes/%d percent, want safe defaults",
					cfg.MinFreeDiskBytes,
					cfg.MinFreeDiskPercent,
				)
			}
		})
	}
}
