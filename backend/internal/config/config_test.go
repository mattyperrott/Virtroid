package config

import "testing"

func TestBootstrapAndNodeAdvertiseDefaultsFailClosedInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("BOOTSTRAP_ENABLED", "")
	t.Setenv("NODE_DEVELOPMENT_ENROLLMENT_ENABLED", "true")
	t.Setenv("NODE_ALLOWED_ADVERTISE_ADDRS", " virtnoded, node.internal,virtnoded ")

	cfg := LoadServer()
	if cfg.BootstrapEnabled {
		t.Fatal("bootstrap enabled by default in production")
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

func TestBootstrapDefaultsToDevelopmentOnlyAndHonorsOverride(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("BOOTSTRAP_ENABLED", "")
	if cfg := LoadServer(); !cfg.BootstrapEnabled {
		t.Fatal("bootstrap disabled by default in development")
	}

	t.Setenv("APP_ENV", "production")
	t.Setenv("BOOTSTRAP_ENABLED", "true")
	if cfg := LoadServer(); !cfg.BootstrapEnabled {
		t.Fatal("bootstrap did not honor explicit production opt-in")
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("BOOTSTRAP_ENABLED", "false")
	if cfg := LoadServer(); cfg.BootstrapEnabled {
		t.Fatal("bootstrap did not honor explicit development opt-out")
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
