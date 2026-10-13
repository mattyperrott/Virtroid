package operatorapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

// A service that polls the control plane every 10-30 seconds is stale after
// two minutes of silence.
const serviceStaleAfter = 2 * time.Minute

type serviceResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"` // good, warn, bad, off
	Detail string `json:"detail"`
}

type paymentsResponse struct {
	Enabled                bool              `json:"enabled"`
	RuntimePricePerHour    string            `json:"runtimePricePerHour"`
	StoragePricePerGBMonth string            `json:"storagePricePerGbMonth"`
	WalletAccounts         int               `json:"walletAccounts"`
	PaidAccounts           int               `json:"paidAccounts"`
	DepositCount           int               `json:"depositCount"`
	DepositsTotal          string            `json:"depositsTotal"`
	Deposits24h            string            `json:"deposits24h"`
	BalancesHeld           string            `json:"balancesHeld"`
	Charges24h             string            `json:"charges24h"`
	RuntimeCharges24h      string            `json:"runtimeCharges24h"`
	StorageCharges24h      string            `json:"storageCharges24h"`
	ChargesTotal           string            `json:"chargesTotal"`
	OverdrawnAccounts      int               `json:"overdrawnAccounts"`
	LastDeposit            string            `json:"lastDeposit"`
	RecentDeposits         []depositResponse `json:"recentDeposits"`
	Accounts               []balanceResponse `json:"accounts"`
}

type depositResponse struct {
	TxID           string `json:"txId"`
	ShortTxID      string `json:"shortTxId"`
	ExplorerURL    string `json:"explorerUrl"`
	AccountID      string `json:"accountId"`
	AccountShortID string `json:"accountShortId"`
	Amount         string `json:"amount"`
	Age            string `json:"age"`
}

type balanceResponse struct {
	AccountID      string `json:"accountId"`
	AccountShortID string `json:"accountShortId"`
	Plan           string `json:"plan"`
	Balance        string `json:"balance"`
	Deposited      string `json:"deposited"`
	Charged        string `json:"charged"`
	LastActivity   string `json:"lastActivity"`
	Overdrawn      bool   `json:"overdrawn"`
}

type vmsResponse struct {
	Enabled          bool         `json:"enabled"`
	Regions          []string     `json:"regions"`
	MaxVMs           int          `json:"maxVms"`
	WarmSpares       int          `json:"warmSpares"`
	Live             int          `json:"live"`
	Ready            int          `json:"ready"`
	Starting         int          `json:"starting"`
	Retiring         int          `json:"retiring"`
	Failed           int          `json:"failed"`
	VMHours24h       string       `json:"vmHours24h"`
	EstimatedCost24h string       `json:"estimatedCost24h"`
	HourlyBurn       string       `json:"hourlyBurn"`
	VMs              []vmResponse `json:"vms"`
}

type vmResponse struct {
	ID             string `json:"id"`
	ShortID        string `json:"shortId"`
	AccountShortID string `json:"accountShortId"`
	Region         string `json:"region"`
	State          string `json:"state"`
	Desired        string `json:"desired"`
	Image          string `json:"image"`
	Progress       string `json:"progress,omitempty"`
	Age            string `json:"age"`
	Ready          string `json:"ready"`
	LastUsed       string `json:"lastUsed"`
	Deleted        string `json:"deleted,omitempty"`
	RetireReason   string `json:"retireReason,omitempty"`
	Failed         bool   `json:"failed"`
}

type siaResponse struct {
	Enabled          bool                 `json:"enabled"`
	Reachable        bool                 `json:"reachable"`
	Checked          string               `json:"checked"`
	Network          string               `json:"network,omitempty"`
	Version          string               `json:"version,omitempty"`
	Synced           bool                 `json:"synced"`
	SyncHeight       uint64               `json:"syncHeight"`
	Wallet           string               `json:"wallet"`
	ContractsEnabled bool                 `json:"contractsEnabled"`
	WantedContracts  int                  `json:"wantedContracts"`
	Contracts        int                  `json:"contracts"`
	IndexerAccounts  int                  `json:"indexerAccounts"`
	PinnedData       string               `json:"pinnedData"`
	LinkedAccounts   int                  `json:"linkedAccounts"`
	StoredByKind     []storedKindResponse `json:"storedByKind"`
}

type storedKindResponse struct {
	Kind  string `json:"kind"`
	Bytes string `json:"bytes"`
}

func serviceHealth(name string, enabled bool, seen *time.Time, now time.Time, offDetail string) serviceResponse {
	switch {
	case !enabled:
		return serviceResponse{Name: name, Status: "off", Detail: offDetail}
	case seen == nil:
		return serviceResponse{Name: name, Status: "bad", Detail: "has not reached the control plane"}
	case now.Sub(*seen) > serviceStaleAfter:
		return serviceResponse{Name: name, Status: "bad", Detail: "silent since " + relativeAge(now, *seen)}
	default:
		return serviceResponse{Name: name, Status: "good", Detail: "checked in " + relativeAge(now, *seen)}
	}
}

func buildPayments(cfg config.ServerConfig, payments store.OperatorPayments, now time.Time) paymentsResponse {
	out := paymentsResponse{
		Enabled:                strings.TrimSpace(cfg.TronDepositXPub) != "",
		RuntimePricePerHour:    formatUSDT(cfg.RuntimePriceHourMicro),
		StoragePricePerGBMonth: formatUSDT(cfg.StoragePriceGiBMonthMicro),
		WalletAccounts:         payments.WalletAccounts,
		PaidAccounts:           payments.PaidAccounts,
		DepositCount:           payments.DepositCount,
		DepositsTotal:          formatUSDT(payments.DepositTotalMicro),
		Deposits24h:            formatUSDT(payments.Deposits24hMicro),
		BalancesHeld:           formatUSDT(payments.BalancesMicro),
		Charges24h:             formatUSDT(payments.RuntimeCharges24h + payments.StorageCharges24h),
		RuntimeCharges24h:      formatUSDT(payments.RuntimeCharges24h),
		StorageCharges24h:      formatUSDT(payments.StorageCharges24h),
		ChargesTotal:           formatUSDT(payments.ChargesTotalMicro),
		OverdrawnAccounts:      payments.OverdrawnAccounts,
		LastDeposit:            "never",
		RecentDeposits:         []depositResponse{},
		Accounts:               []balanceResponse{},
	}
	if payments.LastDepositAt != nil {
		out.LastDeposit = relativeAge(now, *payments.LastDepositAt)
	}
	for _, deposit := range payments.RecentDeposits {
		short := deposit.TxID
		if len(short) > 14 {
			short = short[:6] + "…" + short[len(short)-6:]
		}
		out.RecentDeposits = append(out.RecentDeposits, depositResponse{
			TxID: deposit.TxID, ShortTxID: short, ExplorerURL: "https://tronscan.org/#/transaction/" + deposit.TxID,
			AccountID: deposit.AccountID, AccountShortID: shortID(deposit.AccountID),
			Amount: formatUSDT(deposit.AmountMicro), Age: relativeAge(now, deposit.BlockTime),
		})
	}
	for _, balance := range payments.Accounts {
		entry := balanceResponse{
			AccountID: balance.AccountID, AccountShortID: shortID(balance.AccountID), Plan: balance.Source,
			Balance: formatUSDT(balance.BalanceMicro), Deposited: formatUSDT(balance.DepositedMicro),
			Charged: formatUSDT(balance.ChargedMicro), LastActivity: "never", Overdrawn: balance.BalanceMicro < 0,
		}
		if balance.LastEntryAt != nil {
			entry.LastActivity = relativeAge(now, *balance.LastEntryAt)
		}
		out.Accounts = append(out.Accounts, entry)
	}
	return out
}

func buildVMs(cfg config.ServerConfig, fleet store.OperatorVMs, now time.Time) vmsResponse {
	out := vmsResponse{
		Enabled: cfg.ProvisionerMaxVMs > 0 && len(cfg.ProvisionedRegions) > 0,
		Regions: append([]string{}, cfg.ProvisionedRegions...), MaxVMs: cfg.ProvisionerMaxVMs,
		WarmSpares: cfg.ProvisionerWarmSpares, VMs: []vmResponse{},
		VMHours24h: fmt.Sprintf("%.1f", fleet.VMHours24h),
	}
	for _, vm := range fleet.VMs {
		entry := vmResponse{
			ID: vm.ID, ShortID: shortID(strings.TrimPrefix(vm.ID, "pvm-")), AccountShortID: "spare",
			Region: vm.Region, State: vm.State, Desired: vm.Desired, Image: shortID(vm.ItemHash),
			Progress: vm.Progress, Age: relativeAge(now, vm.RequestedAt), Ready: "not yet", LastUsed: "never",
			RetireReason: vm.RetireReason, Failed: strings.TrimSpace(vm.Failure) != "",
		}
		if vm.AccountID != "" {
			entry.AccountShortID = shortID(vm.AccountID)
		}
		if vm.ReadyAt != nil {
			entry.Ready = relativeAge(now, *vm.ReadyAt)
		}
		if vm.LastUsedAt != nil {
			entry.LastUsed = relativeAge(now, *vm.LastUsedAt)
		}
		if vm.DeletedAt != nil {
			entry.Deleted = relativeAge(now, *vm.DeletedAt)
		}
		if vm.DeletedAt == nil && vm.State != store.ProvisionedVMDeleted {
			out.Live++
			switch {
			case vm.Desired == "absent":
				out.Retiring++
			case vm.State == store.ProvisionedVMReady:
				out.Ready++
			default:
				out.Starting++
			}
			if entry.Failed {
				out.Failed++
			}
		}
		out.VMs = append(out.VMs, entry)
	}
	cost := float64(cfg.RuntimeVMCostHourMicro) / 1e6
	out.EstimatedCost24h = fmt.Sprintf("$%.2f", fleet.VMHours24h*cost)
	out.HourlyBurn = fmt.Sprintf("$%.3f/h", float64(out.Live)*cost)
	return out
}

func buildSia(cfg config.ServerConfig, fleet store.OperatorVMs, status siaStatus, now time.Time) siaResponse {
	out := siaResponse{
		Enabled: strings.TrimSpace(cfg.SiaIndexerAppURL) != "", LinkedAccounts: fleet.SiaAccounts,
		StoredByKind: []storedKindResponse{}, Wallet: "unavailable", PinnedData: formatBytes(0), Checked: "not yet",
	}
	kinds := make([]string, 0, len(fleet.StoredBytesByKind))
	for kind := range fleet.StoredBytesByKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		out.StoredByKind = append(out.StoredByKind, storedKindResponse{Kind: kind, Bytes: formatBytes(fleet.StoredBytesByKind[kind])})
	}
	if !out.Enabled {
		return out
	}
	if !status.CheckedAt.IsZero() {
		out.Checked = relativeAge(now, status.CheckedAt)
	}
	out.Reachable = status.Reachable
	out.Network, out.Version = status.Network, status.Version
	out.Synced, out.SyncHeight = status.Synced, status.SyncHeight
	if status.WalletSC != "" {
		out.Wallet = status.WalletSC + " SC"
	} else if status.Reachable && !status.Synced {
		out.Wallet = "available once synced"
	}
	out.ContractsEnabled, out.WantedContracts, out.Contracts = status.ContractsEnabled, status.WantedContracts, status.Contracts
	out.IndexerAccounts = status.Accounts
	out.PinnedData = formatBytes(status.PinnedBytes)
	return out
}

// siaStatus is what the indexer's admin API last reported.
type siaStatus struct {
	CheckedAt        time.Time
	Reachable        bool
	Network          string
	Version          string
	Synced           bool
	SyncHeight       uint64
	WalletSC         string
	ContractsEnabled bool
	WantedContracts  int
	Contracts        int
	Accounts         int
	PinnedBytes      int64
}

// siaMonitor polls the indexer in the background so a slow or syncing
// indexer never holds up the console.
type siaMonitor struct {
	adminURL string
	password string
	client   *http.Client

	mu         sync.Mutex
	status     siaStatus
	refreshing bool
}

const siaStatusMaxAge = 30 * time.Second

func newSiaMonitor(cfg config.ServerConfig) *siaMonitor {
	if strings.TrimSpace(cfg.SiaIndexerAppURL) == "" {
		return nil
	}
	return &siaMonitor{
		adminURL: strings.TrimRight(strings.TrimSpace(cfg.SiaIndexerAdminURL), "/"),
		password: cfg.SiaIndexerAdminPassword,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// current returns the last status and starts a refresh when it is old.
func (m *siaMonitor) current(now time.Time) siaStatus {
	if m == nil {
		return siaStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.refreshing && now.Sub(m.status.CheckedAt) > siaStatusMaxAge {
		m.refreshing = true
		go m.refresh()
	}
	return m.status
}

func (m *siaMonitor) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status := siaStatus{CheckedAt: time.Now().UTC()}

	var state struct {
		Version    string `json:"version"`
		Network    string `json:"network"`
		SyncHeight uint64 `json:"syncHeight"`
		Synced     bool   `json:"synced"`
	}
	if err := m.get(ctx, "/state", &state); err == nil {
		status.Reachable = true
		status.Version, status.Network = state.Version, state.Network
		status.SyncHeight, status.Synced = state.SyncHeight, state.Synced
	}
	if status.Reachable {
		var settings struct {
			Enabled         bool `json:"enabled"`
			WantedContracts int  `json:"wantedContracts"`
		}
		if m.get(ctx, "/settings/contracts", &settings) == nil {
			status.ContractsEnabled, status.WantedContracts = settings.Enabled, settings.WantedContracts
		}
		var contracts []json.RawMessage
		if m.get(ctx, "/contracts?good=true&revisable=true&limit=1000", &contracts) == nil {
			status.Contracts = len(contracts)
		}
		var stats struct {
			Registered int   `json:"registered"`
			PinnedSize int64 `json:"pinnedSize"`
		}
		if m.get(ctx, "/stats/accounts", &stats) == nil {
			status.Accounts, status.PinnedBytes = stats.Registered, stats.PinnedSize
		}
		// The wallet answers only once the chain is scanned.
		if status.Synced {
			var wallet struct {
				Confirmed string `json:"confirmed"`
			}
			if m.get(ctx, "/wallet", &wallet) == nil {
				status.WalletSC = hastingsToSC(wallet.Confirmed)
			}
		}
	}

	m.mu.Lock()
	m.status = status
	m.refreshing = false
	m.mu.Unlock()
}

func (m *siaMonitor) get(ctx context.Context, route string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.adminURL+route, nil)
	if err != nil {
		return err
	}
	request.SetBasicAuth("", m.password)
	response, err := m.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("indexer %s: %s", route, response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out)
}

// hastingsToSC formats a Siacoin amount given in hastings (10^-24 SC).
func hastingsToSC(hastings string) string {
	value, ok := new(big.Int).SetString(strings.Trim(strings.TrimSpace(hastings), `"`), 10)
	if !ok {
		return ""
	}
	sc := new(big.Rat).SetFrac(value, new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))
	return sc.FloatString(2)
}

// formatUSDT formats millionths of a USDT with two to six decimals.
func formatUSDT(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	whole, fraction := micro/1_000_000, micro%1_000_000
	decimals := strings.TrimRight(fmt.Sprintf("%06d", fraction), "0")
	for len(decimals) < 2 {
		decimals += "0"
	}
	return fmt.Sprintf("%s%d.%s USDT", sign, whole, decimals)
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value, suffix := float64(size), "B"
	for _, next := range []string{"KiB", "MiB", "GiB", "TiB"} {
		if value < unit {
			break
		}
		value, suffix = value/unit, next
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}
