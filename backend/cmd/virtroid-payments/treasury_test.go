package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/tron"
)

type fakeTreasuryControl struct {
	watched []watchedAddress
	sia     siaWalletState
	siaErr  error
}

func (f *fakeTreasuryControl) watchList(context.Context) ([]watchedAddress, error) {
	return f.watched, nil
}
func (f *fakeTreasuryControl) siaWallet(context.Context) (siaWalletState, error) {
	return f.sia, f.siaErr
}

type sentTx struct {
	raw, signature []byte
}

type fakeChain struct {
	balances map[string]accountBalance
	sent     []sentTx
	outcomes map[string]bool
}

func (f *fakeChain) account(_ context.Context, address string) (accountBalance, error) {
	return f.balances[address], nil
}
func (f *fakeChain) nowBlock(context.Context) (tron.BlockRef, error) {
	return tron.BlockRef{Number: 87105266, ID: make([]byte, 32)}, nil
}
func (f *fakeChain) broadcast(_ context.Context, raw, signature []byte) (string, error) {
	f.sent = append(f.sent, sentTx{raw, signature})
	id := tron.TxID(raw)
	return fmt.Sprintf("%x", id), nil
}
func (f *fakeChain) txOutcome(_ context.Context, txID string) (bool, bool, error) {
	succeeded, done := f.outcomes[txID]
	return done, succeeded, nil
}

type fakeSwaps struct {
	recentList []swapExchange
	created    []map[string]string
	exchange   swapExchange
}

// One USDT buys 100 SC, 0.0005 ETH (2,000 USD an ETH) or 4 TRX; the
// service takes no fee, so reverse quotes are exact.
func rate(to swapCurrency) *big.Rat {
	switch to {
	case siacoin:
		return big.NewRat(100, 1)
	case ether:
		return big.NewRat(1, 2000)
	default:
		return big.NewRat(4, 1)
	}
}

func (f *fakeSwaps) estimate(_ context.Context, _, to swapCurrency, amount string, reverse bool) (string, error) {
	value, ok := new(big.Rat).SetString(amount)
	if !ok {
		return "", errors.New("bad amount")
	}
	if reverse {
		return new(big.Rat).Quo(value, rate(to)).FloatString(8), nil
	}
	return new(big.Rat).Mul(value, rate(to)).FloatString(8), nil
}
func (f *fakeSwaps) ranges(context.Context, swapCurrency, swapCurrency) (string, string, error) {
	return "10", "", nil
}
func (f *fakeSwaps) create(_ context.Context, from, to swapCurrency, amount, addressTo, refund string) (swapExchange, error) {
	f.created = append(f.created, map[string]string{"to": to.String(), "amount": amount, "addressTo": addressTo, "refund": refund})
	exchange := f.exchange
	if exchange.ID == "" {
		exchange = swapExchange{ID: "ex-1", Status: "waiting", TickerFrom: from.Ticker, NetworkFrom: from.Network,
			TickerTo: to.Ticker, NetworkTo: to.Network, AmountFrom: amount, AddressTo: addressTo,
			AddressFrom: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"}
	}
	return exchange, nil
}
func (f *fakeSwaps) recent(context.Context, time.Time) ([]swapExchange, error) {
	return f.recentList, nil
}

type fakeCredits struct {
	perDay  int64
	balance int64
}

func (f fakeCredits) creditBalance(context.Context, string) (int64, error) { return f.balance, nil }
func (f fakeCredits) dailySpend(context.Context, string, int, time.Time) (int64, error) {
	return f.perDay, nil
}

type fakeEther struct{ wei *big.Int }

func (f fakeEther) balanceWei(context.Context, string) (*big.Int, error) { return f.wei, nil }

const testSiaAddress = "1111111111111111111111111111111111111111111111111111111111111111111111111111"

type treasuryHarness struct {
	t        *treasury
	wallet   *tron.Wallet
	control  *fakeTreasuryControl
	chain    *fakeChain
	swaps    *fakeSwaps
	logs     []string
	treasury string
	deposit  string
}

func newHarness(t *testing.T, mode string) *treasuryHarness {
	t.Helper()
	phrase, err := tron.NewSeedPhrase()
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := tron.OpenWallet(phrase)
	if err != nil {
		t.Fatal(err)
	}
	treasuryAddress, _ := wallet.Address(0)
	deposit, _ := wallet.Address(1)
	h := &treasuryHarness{
		wallet:   wallet,
		control:  &fakeTreasuryControl{watched: []watchedAddress{{Index: 1, Address: deposit}}, sia: siaWalletState{Address: testSiaAddress, ConfirmedHastings: "25000" + strings.Repeat("0", 24)}},
		chain:    &fakeChain{balances: map[string]accountBalance{treasuryAddress: {TRXSun: 200_000_000}}, outcomes: map[string]bool{}},
		swaps:    &fakeSwaps{},
		treasury: treasuryAddress,
		deposit:  deposit,
	}
	cfg := defaultTreasuryConfig()
	cfg.Mode = mode
	cfg.SiaAddress = testSiaAddress
	h.t = newTreasury(cfg, wallet, h.control, h.chain, h.swaps, fakeCredits{}, fakeEther{wei: big.NewInt(0)})
	h.t.pace = func() {}
	h.t.logf = func(format string, args ...any) { h.logs = append(h.logs, fmt.Sprintf(format, args...)) }
	return h
}

func (h *treasuryHarness) logged(fragment string) bool {
	for _, line := range h.logs {
		if strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

func TestDryRunOnlyLogsSweepsAndSwaps(t *testing.T) {
	h := newHarness(t, treasuryDryRun)
	h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000}
	h.chain.balances[h.treasury] = accountBalance{TRXSun: 200_000_000, USDTMicro: 400_000_000}
	h.control.sia.ConfirmedHastings = "5000" + strings.Repeat("0", 24)
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.chain.sent) != 0 || len(h.swaps.created) != 0 {
		t.Fatalf("dry run sent %d transactions and created %d exchanges", len(h.chain.sent), len(h.swaps.created))
	}
	if !h.logged("would send 15.000000 TRX from the treasury to "+h.deposit) || !h.logged("would swap 150.000000 USDT for about 15000.00000000 sc:sc") {
		t.Fatalf("logs = %q", h.logs)
	}
}

func TestLiveSweepFundsThenSweepsWithTheDepositKey(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.chain.sent) != 1 {
		t.Fatalf("sent %d transactions, want the fee top-up", len(h.chain.sent))
	}
	if signer, err := tron.Signer(h.chain.sent[0].raw, h.chain.sent[0].signature); err != nil || signer != h.treasury {
		t.Fatalf("top-up signed by %s (%v), want the treasury", signer, err)
	}

	// Pending: nothing more until it is solidified.
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.chain.sent) != 1 {
		t.Fatalf("sent again while the top-up was pending")
	}
	id := tron.TxID(h.chain.sent[0].raw)
	h.chain.outcomes[fmt.Sprintf("%x", id)] = true
	h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000, TRXSun: 15_000_000}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.chain.sent) != 2 {
		t.Fatalf("sent %d transactions, want the sweep too", len(h.chain.sent))
	}
	sweep := h.chain.sent[1]
	if signer, err := tron.Signer(sweep.raw, sweep.signature); err != nil || signer != h.deposit {
		t.Fatalf("sweep signed by %s (%v), want the deposit address", signer, err)
	}
	treasuryBytes, _ := tron.AddressBytes(h.treasury)
	usdtBytes, _ := tron.AddressBytes(tron.USDTContract)
	call := fmt.Sprintf("a9059cbb%024x%x%064x", 0, treasuryBytes[1:], 120_000_000)
	if got := fmt.Sprintf("%x", sweep.raw); !strings.Contains(got, call) || !strings.Contains(got, fmt.Sprintf("%x", usdtBytes)) {
		t.Fatal("the sweep is not a USDT transfer of the whole balance to the treasury")
	}
}

func TestLiveSwapPaysOnlyAnExchangeThatMatches(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.treasury] = accountBalance{TRXSun: 200_000_000, USDTMicro: 400_000_000}
	h.control.sia.ConfirmedHastings = "15000" + strings.Repeat("0", 24)
	h.swaps.exchange = swapExchange{ID: "ex-bad", Status: "waiting", TickerFrom: "usdt", NetworkFrom: "trx",
		TickerTo: "sc", NetworkTo: "sc", AmountFrom: "50.000000", AddressTo: strings.Repeat("2", 76),
		AddressFrom: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.swaps.created) != 1 || h.swaps.created[0]["addressTo"] != testSiaAddress || h.swaps.created[0]["refund"] != h.treasury {
		t.Fatalf("created = %v", h.swaps.created)
	}
	if len(h.chain.sent) != 0 || !h.logged("pays "+strings.Repeat("2", 76)) {
		t.Fatalf("paid an exchange for another address: sent=%d logs=%q", len(h.chain.sent), h.logs)
	}

	h.swaps.exchange = swapExchange{}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.chain.sent) != 1 {
		t.Fatalf("did not pay the matching exchange")
	}
	if signer, _ := tron.Signer(h.chain.sent[0].raw, h.chain.sent[0].signature); signer != h.treasury {
		t.Fatalf("payment signed by %s", signer)
	}
}

func TestSwapsKeepToTheDailyCapAndSkipPairsInFlight(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.treasury] = accountBalance{TRXSun: 200_000_000, USDTMicro: 1_000_000_000}
	h.control.sia.ConfirmedHastings = "0"
	h.swaps.recentList = []swapExchange{
		{ID: "a", Status: "finished", TickerFrom: "usdt", NetworkFrom: "trx", TickerTo: "eth", NetworkTo: "eth", AmountFrom: "450"},
		{ID: "b", Status: "expired", TickerFrom: "usdt", NetworkFrom: "trx", TickerTo: "sc", NetworkTo: "sc", AmountFrom: "300"},
	}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.swaps.created) != 1 || h.swaps.created[0]["amount"] != "50.000000" {
		t.Fatalf("created = %v, want one 50 USDT swap under the 500 USDT daily cap", h.swaps.created)
	}

	h.swaps.recentList = append(h.swaps.recentList, swapExchange{ID: "c", Status: "exchanging", TickerFrom: "usdt", NetworkFrom: "trx", TickerTo: "sc", NetworkTo: "sc", AmountFrom: "10"})
	h.swaps.created = nil
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.swaps.created) != 0 || !h.logged("an exchange for sc:sc is still in flight") {
		t.Fatalf("swapped a pair already in flight: %v", h.swaps.created)
	}
}

func TestTreasuryWithoutTRXAsksForItInsteadOfSwapping(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.treasury] = accountBalance{USDTMicro: 400_000_000}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.swaps.created) != 0 || !h.logged("send at least 150.000000 TRX to "+h.treasury) {
		t.Fatalf("created=%v logs=%q", h.swaps.created, h.logs)
	}
}

func TestLiveSiacoinNeedsAPinnedAddressThatMatchesIndexd(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.treasury] = accountBalance{TRXSun: 200_000_000, USDTMicro: 400_000_000}
	h.control.sia = siaWalletState{Address: strings.Repeat("3", 76), ConfirmedHastings: "0"}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.swaps.created) != 0 || !h.logged("not PAYMENTS_SIA_ADDRESS") {
		t.Fatalf("swapped to an address the control plane named: %v", h.swaps.created)
	}
}

func TestAlephNeedBuysTheDaysOfCreditsMissing(t *testing.T) {
	h := newHarness(t, treasuryDryRun)
	h.t.cfg.AlephFundAddress = "0x4631b0613fF21cd5715f1b1572b4CD5bc0916c4C"
	h.t.credits = fakeCredits{perDay: 4_000_000, balance: 10_000_000}
	need, ok := h.t.alephNeed(context.Background())
	if !ok {
		t.Fatalf("no need; logs = %q", h.logs)
	}
	// 7 days x 4M = 28M credits; 10M held; 18M = 18 USD = 0.009 ETH, plus
	// the 0.002 ETH gas reserve since no ETH is waiting.
	if need.to != ether || need.amount != "0.011000" || need.addressTo != h.t.cfg.AlephFundAddress {
		t.Fatalf("need = %+v", need)
	}
	h.t.eth = fakeEther{wei: new(big.Int).Mul(big.NewInt(10_000_000_000_000_000), big.NewInt(1))} // 0.01 ETH, 0.008 spare = 16 USD
	need, ok = h.t.alephNeed(context.Background())
	if ok {
		t.Fatalf("bought again although ETH waiting covers it: %+v", need)
	}
}

func TestDecimalToMicroRounds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		up   bool
		want int64
	}{
		{"12.5", false, 12_500_000},
		{"0.0000001", true, 1},
		{"0.0000001", false, 0},
		{"150", true, 150_000_000},
	} {
		if got, err := decimalToMicro(tc.in, tc.up); err != nil || got != tc.want {
			t.Fatalf("decimalToMicro(%q, %v) = %d, %v", tc.in, tc.up, got, err)
		}
	}
}

func TestDailySpendStopsAtAlephsLastPage(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			http.Error(w, "No credit history found for this address", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"credit_history":[{"amount":-7000000,"payment_method":"credit_expense","message_timestamp":%q},{"amount":5000000,"payment_method":"credit_purchase","message_timestamp":%q}]}`,
			now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-2*time.Hour).Format(time.RFC3339Nano))
	}))
	defer server.Close()
	perDay, err := alephAPI{baseURL: server.URL, client: server.Client()}.dailySpend(context.Background(), "0x4631b0613fF21cd5715f1b1572b4CD5bc0916c4C", 7, now)
	if err != nil || perDay != 1_000_000 {
		t.Fatalf("dailySpend = %d, %v; want 1,000,000", perDay, err)
	}
}
