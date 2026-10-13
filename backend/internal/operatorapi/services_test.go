package operatorapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

func TestFormatting(t *testing.T) {
	for micro, want := range map[int64]string{
		0: "0.00 USDT", 1_500_000: "1.50 USDT", 1_666: "0.001666 USDT", -250_000: "-0.25 USDT", 12_000_000: "12.00 USDT",
	} {
		if got := formatUSDT(micro); got != want {
			t.Fatalf("formatUSDT(%d) = %q, want %q", micro, got, want)
		}
	}
	if got := hastingsToSC("5000000000000000000000000000"); got != "5000.00" {
		t.Fatalf("hastingsToSC = %q", got)
	}
	if got := hastingsToSC("not a number"); got != "" {
		t.Fatalf("hastingsToSC(garbage) = %q", got)
	}
	if got := formatBytes(20 << 20); got != "20.0 MiB" {
		t.Fatalf("formatBytes = %q", got)
	}
}

func TestAttachServices(t *testing.T) {
	now := time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC)
	recent := now.Add(-20 * time.Second)
	stale := now.Add(-10 * time.Minute)
	ready := now.Add(-time.Hour)
	cfg := config.ServerConfig{
		TronDepositXPub: "xpub-test", RuntimePriceHourMicro: 200_000, StoragePriceGiBMonthMicro: 50_000,
		ProvisionerMaxVMs: 2, ProvisionedRegions: []string{"snp-eu"}, RuntimeVMCostHourMicro: 57_000,
	}
	payments := store.OperatorPayments{
		WalletAccounts: 3, PaidAccounts: 1, DepositCount: 1, DepositTotalMicro: 20_000_000,
		BalancesMicro: 19_500_000, RuntimeCharges24h: 500_000, PaymentsSeenAt: &recent,
		RecentDeposits: []store.OperatorDeposit{{
			TxID: strings.Repeat("ab", 32), AccountID: "account-12345678", AmountMicro: 20_000_000, BlockTime: ready,
		}},
		Accounts: []store.OperatorBalance{{AccountID: "account-12345678", Source: "paid", BalanceMicro: -1, LastEntryAt: &recent}},
	}
	fleet := store.OperatorVMs{
		VMHours24h: 10, ProvisionerSeen: &stale, StoredBytesByKind: map[string]int64{"sia-indexd": 1 << 30},
		VMs: []store.OperatorVM{
			{ID: "pvm-aaaaaaaaaaaa", AccountID: "account-12345678", State: "ready", Desired: "present", RequestedAt: ready, ReadyAt: &ready},
			{ID: "pvm-bbbbbbbbbbbb", State: "booting", Desired: "present", RequestedAt: recent, Failure: "boot loop"},
			{ID: "pvm-cccccccccccc", State: "deleted", Desired: "absent", RequestedAt: ready, DeletedAt: &recent},
		},
	}
	result := overviewResponse{Status: "healthy"}
	attachServices(&result, cfg, payments, fleet, siaStatus{}, now)

	if !result.Payments.Enabled || result.Payments.RuntimePricePerHour != "0.20 USDT" || result.Payments.BalancesHeld != "19.50 USDT" {
		t.Fatalf("payments = %+v", result.Payments)
	}
	if deposit := result.Payments.RecentDeposits[0]; deposit.ShortTxID != "ababab…ababab" || !strings.HasSuffix(deposit.ExplorerURL, deposit.TxID) {
		t.Fatalf("deposit = %+v", deposit)
	}
	if !result.Payments.Accounts[0].Overdrawn {
		t.Fatalf("overdrawn account not flagged: %+v", result.Payments.Accounts[0])
	}
	vms := result.VMs
	if vms.Live != 2 || vms.Ready != 1 || vms.Starting != 1 || vms.Failed != 1 || vms.EstimatedCost24h != "$0.57" || vms.HourlyBurn != "$0.114/h" {
		t.Fatalf("vms = %+v", vms)
	}
	if result.Sia.Enabled || result.Sia.StoredByKind[0].Bytes != "1.0 GiB" {
		t.Fatalf("sia = %+v", result.Sia)
	}
	services := map[string]string{}
	for _, service := range result.Services {
		services[service.Name] = service.Status
	}
	if services["Payments"] != "good" || services["VM provisioner"] != "bad" || services["Sia indexer"] != "off" {
		t.Fatalf("services = %+v", result.Services)
	}
	if result.Status != "degraded" {
		t.Fatalf("status = %q, want degraded with a silent provisioner", result.Status)
	}
}

func TestSiaMonitor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, _ := r.BasicAuth(); password != "admin-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/state":
			w.Write([]byte(`{"version":"f6ae138","network":"mainnet","syncHeight":600000,"synced":true}`))
		case "/api/settings/contracts":
			w.Write([]byte(`{"enabled":true,"wantedContracts":50}`))
		case "/api/contracts":
			w.Write([]byte(`[{},{},{}]`))
		case "/api/stats/accounts":
			w.Write([]byte(`{"registered":4,"pinnedSize":1073741824}`))
		case "/api/wallet":
			w.Write([]byte(`{"confirmed":"5000000000000000000000000000","address":"x"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	monitor := newSiaMonitor(config.ServerConfig{
		SiaIndexerAppURL: "https://example.test:9982", SiaIndexerAdminURL: server.URL + "/api", SiaIndexerAdminPassword: "admin-secret",
	})
	if status := monitor.current(time.Now()); !status.CheckedAt.IsZero() {
		t.Fatalf("first status should be unchecked: %+v", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	var status siaStatus
	for time.Now().Before(deadline) {
		if status = monitor.current(time.Now()); !status.CheckedAt.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !status.Reachable || !status.Synced || status.Contracts != 3 || status.WalletSC != "5000.00" ||
		status.Accounts != 4 || status.PinnedBytes != 1<<30 || !status.ContractsEnabled {
		t.Fatalf("status = %+v", status)
	}
	if newSiaMonitor(config.ServerConfig{}) != nil {
		t.Fatal("a monitor without Sia storage")
	}
}
