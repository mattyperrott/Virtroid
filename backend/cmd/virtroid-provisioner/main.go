// virtroid-provisioner creates and deletes the dedicated runtime VMs that the
// control plane asks for, as AMD SEV-SNP confidential instances on Aleph
// Cloud. It holds the Aleph account key, the runtime disk passphrase, and each
// VM's node key; the control plane holds none of them.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"virtroid/backend/internal/nodeauth"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, cp, driver, interval, err := load()
	if err != nil {
		log.Fatalf("virtroid-provisioner: %v", err)
	}
	r := newReconciler(cfg, cp, driver)
	log.Printf("virtroid-provisioner: regions=%d runtime=%s image=%s", len(cfg.RegionCRNs), cfg.Runtime, cfg.Image)
	for {
		if err := r.runOnce(ctx); err != nil {
			log.Printf("virtroid-provisioner: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func load() (provisionerConfig, controlPlane, alephDriver, time.Duration, error) {
	required := func(key string) (string, error) {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			return "", fmt.Errorf("%s is required", key)
		}
		return value, nil
	}
	var cfg provisionerConfig
	var err error

	controlPlaneURL, err := required("CONTROL_PLANE_URL")
	if err != nil {
		return cfg, nil, nil, 0, err
	}
	keyFile, err := required("PROVISIONER_PRIVATE_KEY_FILE")
	if err != nil {
		return cfg, nil, nil, 0, err
	}
	keyMaterial, err := os.ReadFile(keyFile)
	if err != nil {
		return cfg, nil, nil, 0, fmt.Errorf("read provisioner key: %w", err)
	}
	privateKey, _, err := nodeauth.LoadPrivateKey(string(keyMaterial))
	if err != nil || privateKey == nil {
		return cfg, nil, nil, 0, errors.New("PROVISIONER_PRIVATE_KEY_FILE must hold a P-256 private key")
	}

	if cfg.VMControlPlaneURL, err = required("VM_CONTROL_PLANE_URL"); err != nil {
		return cfg, nil, nil, 0, err
	}
	if parsed, err := url.Parse(cfg.VMControlPlaneURL); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return cfg, nil, nil, 0, errors.New("VM_CONTROL_PLANE_URL must be an https URL")
	}
	if cfg.Runtime, err = itemHashEnv("ALEPH_RUNTIME"); err != nil {
		return cfg, nil, nil, 0, err
	}
	if cfg.Image, err = itemHashEnv("ALEPH_IMAGE"); err != nil {
		return cfg, nil, nil, 0, err
	}
	if cfg.PassphraseFile, err = required("LUKS_PASSPHRASE_FILE"); err != nil {
		return cfg, nil, nil, 0, err
	}
	if _, err := os.Stat(cfg.PassphraseFile); err != nil {
		return cfg, nil, nil, 0, fmt.Errorf("LUKS_PASSPHRASE_FILE: %w", err)
	}
	cfg.StateDir = envOr("PROVISIONER_STATE_DIR", "/var/lib/virtroid-provisioner")
	if cfg.VCPUs, err = intEnv("VM_VCPUS", 2); err != nil {
		return cfg, nil, nil, 0, err
	}
	if cfg.MemoryMiB, err = intEnv("VM_MEMORY_MIB", 4096); err != nil {
		return cfg, nil, nil, 0, err
	}
	if cfg.DiskMiB, err = intEnv("VM_DISK_MIB", 4096); err != nil {
		return cfg, nil, nil, 0, err
	}
	if cfg.DataMiB, err = intEnv("VM_DATA_MIB", 30720); err != nil {
		return cfg, nil, nil, 0, err
	}
	regionCRNs, err := required("PROVISIONER_REGION_CRNS")
	if err != nil {
		return cfg, nil, nil, 0, err
	}
	if cfg.RegionCRNs, err = parseRegionCRNs(regionCRNs); err != nil {
		return cfg, nil, nil, 0, err
	}
	interval, err := time.ParseDuration(envOr("PROVISIONER_POLL_INTERVAL", "10s"))
	if err != nil || interval < time.Second {
		return cfg, nil, nil, 0, errors.New("PROVISIONER_POLL_INTERVAL must be a duration of at least 1s")
	}

	alephEnv := []string{
		"HOME=" + cfg.StateDir,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"ALEPH_CONFIG_HOME=" + envOr("ALEPH_CONFIG_HOME", cfg.StateDir+"/aleph"),
	}
	if passwordFile := strings.TrimSpace(os.Getenv("ALEPH_PASSWORD_FILE")); passwordFile != "" {
		password, err := os.ReadFile(passwordFile)
		if err != nil {
			return cfg, nil, nil, 0, fmt.Errorf("read ALEPH_PASSWORD_FILE: %w", err)
		}
		alephEnv = append(alephEnv, "ALEPH_PASSWORD="+strings.TrimRight(string(password), "\r\n"))
	}
	account, err := required("ALEPH_ACCOUNT")
	if err != nil {
		return cfg, nil, nil, 0, err
	}
	driver := cliDriver{
		bin:     envOr("ALEPH_BIN", "/usr/local/bin/aleph"),
		account: account,
		env:     alephEnv,
		timeout: 10 * time.Minute,
	}
	cp := httpControlPlane{
		baseURL:    controlPlaneURL,
		privateKey: privateKey,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
	return cfg, cp, driver, interval, nil
}

// parseRegionCRNs reads "region=hash[@Product],hash;region2=hash" into the
// compute nodes each region may use. Product is the AMD EPYC generation
// (Milan, Genoa, Turin) that the attestation chain is checked against.
func parseRegionCRNs(raw string) (map[string][]crnTarget, error) {
	regions := map[string][]crnTarget{}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		region, list, ok := strings.Cut(entry, "=")
		region = strings.ToLower(strings.TrimSpace(region))
		if !ok || region == "" {
			return nil, fmt.Errorf("invalid PROVISIONER_REGION_CRNS entry %q", entry)
		}
		for _, item := range strings.Split(list, ",") {
			hash, product, _ := strings.Cut(strings.TrimSpace(item), "@")
			hash = strings.ToLower(strings.TrimSpace(hash))
			if !isItemHash(hash) {
				return nil, fmt.Errorf("invalid compute node hash %q for region %s", hash, region)
			}
			product = strings.TrimSpace(product)
			if product == "" {
				product = "Genoa"
			}
			switch product {
			case "Milan", "Genoa", "Turin":
			default:
				return nil, fmt.Errorf("unknown AMD product %q for node %s", product, hash)
			}
			regions[region] = append(regions[region], crnTarget{Hash: hash, AMDProduct: product})
		}
	}
	if len(regions) == 0 {
		return nil, errors.New("PROVISIONER_REGION_CRNS names no compute nodes")
	}
	return regions, nil
}

func itemHashEnv(key string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if !isItemHash(value) {
		return "", fmt.Errorf("%s must be a 64-character hex item hash", key)
	}
	return value, nil
}

func isItemHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func intEnv(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
