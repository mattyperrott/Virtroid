package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	siaAddressPattern = regexp.MustCompile(`^[0-9a-f]{76}$`)
	ethAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
)

// treasuryConfigFromEnv reads the treasury's settings (payments.md).
func treasuryConfigFromEnv() (treasuryConfig, time.Duration, error) {
	cfg := defaultTreasuryConfig()
	cfg.Mode = envOrDefault("PAYMENTS_TREASURY", treasuryDryRun)
	switch cfg.Mode {
	case treasuryOff, treasuryDryRun, treasuryLive:
	default:
		return cfg, 0, fmt.Errorf("PAYMENTS_TREASURY must be off, dry-run or live, not %q", cfg.Mode)
	}
	every, err := time.ParseDuration(envOrDefault("PAYMENTS_TREASURY_INTERVAL", "30m"))
	if err != nil || every < time.Minute {
		return cfg, 0, fmt.Errorf("PAYMENTS_TREASURY_INTERVAL must be a duration of at least 1m")
	}
	for _, setting := range []struct {
		name string
		into *int64
	}{
		{"PAYMENTS_SWEEP_MIN_USDT", &cfg.SweepMinMicro},
		{"PAYMENTS_MIN_SWAP_USDT", &cfg.MinSwapMicro},
		{"PAYMENTS_MAX_SWAP_USDT", &cfg.MaxSwapMicro},
		{"PAYMENTS_MAX_DAILY_USDT", &cfg.MaxDailyMicro},
	} {
		if raw := strings.TrimSpace(os.Getenv(setting.name)); raw != "" {
			micro, err := decimalToMicro(raw, false)
			if err != nil || micro <= 0 {
				return cfg, 0, fmt.Errorf("%s must be a positive USDT amount", setting.name)
			}
			*setting.into = micro
		}
	}
	if raw := strings.TrimSpace(os.Getenv("PAYMENTS_SIA_TARGET_SC")); raw != "" {
		target, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || target < 0 {
			return cfg, 0, fmt.Errorf("PAYMENTS_SIA_TARGET_SC must be a whole number of SC (0 turns Siacoin off)")
		}
		cfg.SiaTargetSC = target
	}
	if raw := strings.TrimSpace(os.Getenv("PAYMENTS_ALEPH_DAYS")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 0 || days > 90 {
			return cfg, 0, fmt.Errorf("PAYMENTS_ALEPH_DAYS must be 0 to 90 (0 turns ETH off)")
		}
		cfg.AlephDays = days
	}
	cfg.SiaAddress = strings.ToLower(strings.TrimSpace(os.Getenv("PAYMENTS_SIA_ADDRESS")))
	if cfg.SiaAddress != "" && !siaAddressPattern.MatchString(cfg.SiaAddress) {
		return cfg, 0, fmt.Errorf("PAYMENTS_SIA_ADDRESS must be a 76-character Sia address")
	}
	cfg.AlephFundAddress = strings.TrimSpace(os.Getenv("PAYMENTS_ALEPH_FUND_ADDRESS"))
	cfg.AlephSpendAddress = strings.TrimSpace(os.Getenv("PAYMENTS_ALEPH_SPEND_ADDRESS"))
	for name, address := range map[string]string{"PAYMENTS_ALEPH_FUND_ADDRESS": cfg.AlephFundAddress, "PAYMENTS_ALEPH_SPEND_ADDRESS": cfg.AlephSpendAddress} {
		if address != "" && !ethAddressPattern.MatchString(address) {
			return cfg, 0, fmt.Errorf("%s must be an 0x Ethereum address", name)
		}
	}
	if cfg.MinSwapMicro > cfg.MaxSwapMicro || cfg.MaxSwapMicro > cfg.MaxDailyMicro {
		return cfg, 0, fmt.Errorf("swap caps must satisfy minimum <= per swap <= per day")
	}
	return cfg, every, nil
}
