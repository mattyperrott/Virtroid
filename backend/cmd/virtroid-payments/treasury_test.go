package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
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
	// outgoing is the USDT the treasury sent; swept is the USDT an address
	// with an entry sent, which for a deposit address is its sweeps.
	outgoing []usdtTransfer
	swept    map[string][]usdtTransfer
	sentErr  error
	// trxOut is the TRX the chain shows the treasury sent.
	trxOut   []trxTransfer
	trxErr   error
	trxReads int
}

func (f *fakeChain) account(_ context.Context, address string) (accountBalance, error) {
	return f.balances[address], nil
}
func (f *fakeChain) usdtSent(_ context.Context, address string, since time.Time) ([]usdtTransfer, error) {
	if f.sentErr != nil {
		return nil, f.sentErr
	}
	list := f.outgoing
	if swept, ok := f.swept[address]; ok {
		list = swept
	}
	var transfers []usdtTransfer
	for _, transfer := range list {
		if !transfer.BlockTime.Before(since) {
			transfers = append(transfers, transfer)
		}
	}
	return transfers, nil
}
func (f *fakeChain) trxSent(_ context.Context, _ string, since time.Time) ([]trxTransfer, error) {
	f.trxReads++
	if f.trxErr != nil {
		return nil, f.trxErr
	}
	var transfers []trxTransfer
	for _, transfer := range f.trxOut {
		if !transfer.BlockTime.Before(since) {
			transfers = append(transfers, transfer)
		}
	}
	return transfers, nil
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
		// As SimpleSwap: floating-rate quotes run forward only.
		return "", errors.New("GET /v3/estimates: status 422: Reverse estimates must be fixed")
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

type perAddressCredits struct {
	perDay   int64
	balances map[string]int64
}

func (f perAddressCredits) creditBalance(_ context.Context, address string) (int64, error) {
	return f.balances[address], nil
}
func (f perAddressCredits) dailySpend(context.Context, string, int, time.Time) (int64, error) {
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
		wallet:  wallet,
		control: &fakeTreasuryControl{watched: []watchedAddress{{Index: 1, Address: deposit}}, sia: siaWalletState{Address: testSiaAddress, ConfirmedHastings: "25000" + strings.Repeat("0", 24)}},
		chain: &fakeChain{balances: map[string]accountBalance{treasuryAddress: {TRXSun: 200_000_000}}, outcomes: map[string]bool{},
			swept: map[string][]usdtTransfer{deposit: nil}},
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
	if !h.logged("would send 15.000000 TRX from the treasury to "+h.deposit) || !h.logged("would swap 153.000000 USDT for about 15300.00000000 sc:sc") {
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

func TestAlephNeedCountsCreditsOnTheChargedAccount(t *testing.T) {
	h := newHarness(t, treasuryDryRun)
	h.t.cfg.AlephFundAddress = "0x4631b0613fF21cd5715f1b1572b4CD5bc0916c4C"
	h.t.cfg.AlephSpendAddress = "0x0aBe3c402B41CC339B7A101Ad940214cFB947a08"
	// The owner pays for the VMs and holds a week's credits, the provisioner none.
	h.t.credits = perAddressCredits{perDay: 4_000_000, balances: map[string]int64{h.t.cfg.AlephSpendAddress: 28_000_000}}
	if need, ok := h.t.alephNeed(context.Background()); ok {
		t.Fatalf("bought credits the charged account already holds: %+v", need)
	}
	// What the provisioner bought and hasn't forwarded yet counts too.
	h.t.credits = perAddressCredits{perDay: 4_000_000, balances: map[string]int64{
		h.t.cfg.AlephSpendAddress: 14_000_000, h.t.cfg.AlephFundAddress: 14_000_000,
	}}
	if need, ok := h.t.alephNeed(context.Background()); ok {
		t.Fatalf("bought again with credits on their way: %+v", need)
	}
	h.t.credits = perAddressCredits{perDay: 4_000_000, balances: map[string]int64{h.t.cfg.AlephSpendAddress: 10_000_000}}
	if _, ok := h.t.alephNeed(context.Background()); !ok {
		t.Fatalf("no need with 10M of the 28M wanted; logs = %q", h.logs)
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

func TestSmallNeedBuysTheSmallestSwap(t *testing.T) {
	h := newHarness(t, treasuryDryRun)
	h.chain.balances[h.treasury] = accountBalance{TRXSun: 200_000_000, USDTMicro: 400_000_000}
	budget := int64(400_000_000)
	// 0.005 ETH is 10.2 USDT with the margin; the treasury's floor is 25.
	if err := h.t.swap(context.Background(), swapNeed{to: ether, amount: "0.005", addressTo: "0x4631b0613fF21cd5715f1b1572b4CD5bc0916c4C", why: "test"}, h.treasury, &budget); err != nil {
		t.Fatal(err)
	}
	if !h.logged("would swap 25.000000 USDT for about 0.01250000 eth:eth") || budget != 375_000_000 {
		t.Fatalf("budget=%d logs=%q", budget, h.logs)
	}
}

func TestDailyCapCountsTransfersTheExchangeLeavesOut(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.treasury] = accountBalance{TRXSun: 200_000_000, USDTMicro: 1_000_000_000}
	h.control.sia.ConfirmedHastings = "0"
	// SimpleSwap reports nothing, but the treasury sent 450 USDT two hours ago.
	h.chain.outgoing = []usdtTransfer{{TxID: "aa", AmountMicro: 450_000_000, BlockTime: time.Now().Add(-2 * time.Hour)}}
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.swaps.created) != 1 || h.swaps.created[0]["amount"] != "50.000000" {
		t.Fatalf("created = %v, want one 50 USDT swap: the chain shows 450 of the 500 USDT cap spent", h.swaps.created)
	}
}

func TestNoSwapsWhenTheTreasuryTransfersCantBeRead(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.treasury] = accountBalance{TRXSun: 200_000_000, USDTMicro: 1_000_000_000}
	h.control.sia.ConfirmedHastings = "0"
	h.chain.sentErr = errors.New("trongrid unavailable")
	_ = h.t.cycle(context.Background())
	if len(h.swaps.created) != 0 {
		t.Fatalf("swapped without knowing what was already sent: %v", h.swaps.created)
	}
}

func TestSentSinceCountsUnconfirmedPaymentsOnce(t *testing.T) {
	h := newHarness(t, treasuryLive)
	now := time.Now()
	h.t.paid = []exchangePayment{
		{txID: "confirmed", micro: 100_000_000, at: now.Add(-time.Hour)},
		{txID: "pending", micro: 40_000_000, at: now.Add(-time.Minute)},
		{txID: "old", micro: 300_000_000, at: now.Add(-30 * time.Hour)},
	}
	h.chain.outgoing = []usdtTransfer{
		{TxID: "CONFIRMED", AmountMicro: 100_000_000, BlockTime: now.Add(-time.Hour)},
		{TxID: "before-restart", AmountMicro: 200_000_000, BlockTime: now.Add(-5 * time.Hour)},
	}
	sent, err := h.t.sentSince(context.Background(), h.treasury, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// 100 + 200 confirmed, plus the 40 not on chain yet; the 30-hour-old payment is outside the day.
	if sent != 340_000_000 {
		t.Fatalf("sent = %d, want 340000000", sent)
	}
	if len(h.t.paid) != 2 {
		t.Fatalf("kept %d payments, want the 2 from the last day", len(h.t.paid))
	}
}

// settle marks the newest sent transaction solidified with the given
// outcome and returns its ID.
func (h *treasuryHarness) settle(succeeded bool) string {
	id := tron.TxID(h.chain.sent[len(h.chain.sent)-1].raw)
	txID := fmt.Sprintf("%x", id)
	h.chain.outcomes[txID] = succeeded
	return txID
}

func (h *treasuryHarness) cycle(t *testing.T) {
	t.Helper()
	if err := h.t.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// failSweep runs one round for the harness's deposit address: the treasury
// tops it up, the address sweeps, and the sweep fails, burning the fee and
// leaving the USDT where it was.
func (h *treasuryHarness) failSweep(t *testing.T, now time.Time) {
	t.Helper()
	before := len(h.chain.sent)
	h.cycle(t)
	if len(h.chain.sent) != before+1 {
		t.Fatalf("sent %d transactions, want a fee top-up; logs = %q", len(h.chain.sent)-before, h.logs)
	}
	topUp := h.settle(true)
	h.chain.trxOut = append(h.chain.trxOut, trxTransfer{TxID: topUp, To: h.deposit, AmountSun: 15_000_000, BlockTime: now})
	h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000, TRXSun: 15_000_000}
	h.cycle(t)
	if len(h.chain.sent) != before+2 {
		t.Fatalf("sent %d transactions, want the sweep after the top-up", len(h.chain.sent)-before)
	}
	h.settle(false)
	h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000}
}

func TestSweepsThatKeepFailingStopGettingFees(t *testing.T) {
	h := newHarness(t, treasuryLive)
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	now := start
	h.t.now = func() time.Time { return now }
	h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000}

	h.failSweep(t, now)
	now = now.Add(time.Hour)
	h.failSweep(t, now)

	// Two top-ups moved nothing: there is no third.
	now = now.Add(time.Hour)
	sent := len(h.chain.sent)
	h.cycle(t)
	if len(h.chain.sent) != sent {
		t.Fatalf("a third fee top-up was sent after two sweeps failed")
	}
	if !h.logged("NOT SWEEPING 120.000000 USDT at "+h.deposit) || !h.logged("its last 2 fee top-ups moved no USDT") ||
		!h.logged("The next try is after 2026-11-17 12:00 UTC") {
		t.Fatalf("logs = %q", h.logs)
	}

	// A restart remembers nothing itself, and the chain still says no.
	cfg := defaultTreasuryConfig()
	cfg.Mode = treasuryLive
	restarted := newTreasury(cfg, h.wallet, h.control, h.chain, h.swaps, fakeCredits{}, fakeEther{wei: big.NewInt(0)})
	restarted.pace = func() {}
	restarted.logf = func(string, ...any) {}
	restarted.now = func() time.Time { return now }
	if err := restarted.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.chain.sent) != sent {
		t.Fatalf("a restarted treasury sent a third fee top-up")
	}

	// Still no the minute before the first of them is a week old, and one
	// more try the minute after.
	now = start.Add(sweepRetryAfter - time.Minute)
	h.cycle(t)
	if len(h.chain.sent) != sent {
		t.Fatalf("retried before %s had passed", sweepRetryAfter)
	}
	now = start.Add(sweepRetryAfter + time.Minute)
	h.cycle(t)
	if len(h.chain.sent) != sent+1 {
		t.Fatalf("no retry after %s", sweepRetryAfter)
	}
}

func TestAnAddressWhoseSweepsWorkKeepsGettingFees(t *testing.T) {
	h := newHarness(t, treasuryLive)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	h.t.now = func() time.Time { return now }
	// Three deposits in a day, each topped up and swept.
	for range 3 {
		h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000, TRXSun: 2_000_000}
		before := len(h.chain.sent)
		h.cycle(t)
		if len(h.chain.sent) != before+1 {
			t.Fatalf("no fee top-up for a deposit whose earlier sweeps worked; logs = %q", h.logs)
		}
		h.chain.trxOut = append(h.chain.trxOut, trxTransfer{TxID: h.settle(true), To: h.deposit, AmountSun: 13_000_000, BlockTime: now})
		h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000, TRXSun: 15_000_000}
		h.cycle(t)
		if len(h.chain.sent) != before+2 {
			t.Fatalf("no sweep after the top-up")
		}
		now = now.Add(time.Minute)
		h.chain.swept[h.deposit] = append(h.chain.swept[h.deposit], usdtTransfer{TxID: h.settle(true), To: h.treasury, AmountMicro: 120_000_000, BlockTime: now})
		now = now.Add(time.Hour)
	}
}

func TestFeeTopUpsKeepToTheDailyCap(t *testing.T) {
	h := newHarness(t, treasuryLive)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	h.t.now = func() time.Time { return now }
	h.t.cfg.MaxDailyFeeSun = 40_000_000
	// 12 TRX went out for fees three hours ago, and 30 two days ago.
	h.chain.trxOut = []trxTransfer{
		{TxID: strings.Repeat("a", 64), To: testRecipient, AmountSun: 12_000_000, BlockTime: now.Add(-3 * time.Hour)},
		{TxID: strings.Repeat("b", 64), To: testRecipient, AmountSun: 30_000_000, BlockTime: now.Add(-48 * time.Hour)},
	}
	for index := int64(1); index <= 3; index++ {
		address, _ := h.wallet.Address(uint32(index))
		if index > 1 {
			h.control.watched = append(h.control.watched, watchedAddress{Index: index, Address: address})
		}
		h.chain.balances[address] = accountBalance{USDTMicro: 50_000_000}
	}
	h.cycle(t)
	// 12 + 15 fits in 40; a second 15 would not.
	if len(h.chain.sent) != 1 {
		t.Fatalf("sent %d fee top-ups, want one within the cap", len(h.chain.sent))
	}
	if !h.logged("PAYMENTS_MAX_DAILY_FEE_TRX") {
		t.Fatalf("logs = %q", h.logs)
	}
	if h.chain.trxReads != 1 {
		t.Fatalf("read the treasury's transfers %d times in one cycle", h.chain.trxReads)
	}
}

func TestNoFeeTopUpWhileTheTreasurysTransfersCannotBeRead(t *testing.T) {
	h := newHarness(t, treasuryLive)
	h.chain.balances[h.deposit] = accountBalance{USDTMicro: 120_000_000}
	h.chain.trxErr = errors.New("status 429")
	h.cycle(t)
	if len(h.chain.sent) != 0 || !h.logged("no fee top-ups until they can be read") {
		t.Fatalf("sent=%d logs=%q", len(h.chain.sent), h.logs)
	}
}

func TestTrxSentKeepsOnlyTheAddresssOwnSuccessfulTRXTransfers(t *testing.T) {
	owner, _ := tron.AddressBytes(testRecipient)
	other, _ := tron.AddressBytes(tron.USDTContract)
	// TronGrid's /v1/accounts/{address}/transactions entries.
	entry := func(tx, kind, result string, from, to []byte, amount int64) map[string]any {
		return map[string]any{
			"txID": tx, "block_timestamp": int64(1791900048000),
			"ret": []map[string]any{{"contractRet": result, "fee": 268000}},
			"raw_data": map[string]any{"contract": []map[string]any{{
				"type": kind,
				"parameter": map[string]any{
					"type_url": "type.googleapis.com/protocol." + kind,
					"value":    map[string]any{"amount": amount, "owner_address": hex.EncodeToString(from), "to_address": hex.EncodeToString(to)},
				},
			}}},
		}
	}
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.URL.Path != "/v1/accounts/"+testRecipient+"/transactions" || query.Get("only_from") != "true" ||
			query.Get("only_confirmed") != "true" || query.Get("min_timestamp") == "" {
			http.NotFound(w, r)
			return
		}
		pages++
		response := map[string]any{"success": true}
		if query.Get("fingerprint") == "" {
			response["data"] = []any{
				entry(strings.Repeat("A", 64), "TransferContract", "SUCCESS", owner, other, 15_000_000),
				entry(strings.Repeat("b", 64), "TriggerSmartContract", "SUCCESS", owner, other, 9_000_000),
				entry(strings.Repeat("c", 64), "TransferContract", "OUT_OF_TIME", owner, other, 9_000_000),
				entry(strings.Repeat("d", 64), "TransferContract", "SUCCESS", other, owner, 9_000_000),
			}
			response["meta"] = map[string]any{"fingerprint": "next"}
		} else {
			response["data"] = []any{entry(strings.Repeat("e", 64), "TransferContract", "SUCCESS", owner, other, 7)}
			response["meta"] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	grid := tronGrid{baseURL: server.URL, client: server.Client()}
	sent, err := grid.trxSent(context.Background(), testRecipient, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(sent) != 2 {
		t.Fatalf("pages=%d sent=%+v", pages, sent)
	}
	if sent[0].TxID != strings.Repeat("a", 64) || sent[0].To != tron.USDTContract || sent[0].AmountSun != 15_000_000 ||
		!sent[0].BlockTime.Equal(time.UnixMilli(1791900048000)) || sent[1].AmountSun != 7 {
		t.Fatalf("sent = %+v", sent)
	}

	// An entry it can't read stops the count rather than shrinking it.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bad := entry(strings.Repeat("f", 64), "TransferContract", "SUCCESS", owner, other, 1)
		bad["raw_data"].(map[string]any)["contract"].([]map[string]any)[0]["parameter"].(map[string]any)["value"].(map[string]any)["to_address"] = "not-hex"
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{bad}, "meta": map[string]any{}})
	}))
	defer broken.Close()
	if _, err := (tronGrid{baseURL: broken.URL, client: broken.Client()}).trxSent(context.Background(), testRecipient, time.Now()); err == nil {
		t.Fatal("an unreadable recipient was skipped")
	}
}

func TestTreasuryConfigReadsTheDailyFeeCap(t *testing.T) {
	t.Setenv("PAYMENTS_MAX_DAILY_FEE_TRX", "300")
	cfg, _, err := treasuryConfigFromEnv()
	if err != nil || cfg.MaxDailyFeeSun != 300_000_000 {
		t.Fatalf("cap = %d, %v", cfg.MaxDailyFeeSun, err)
	}
	// Less than one sweep's fee would stop every sweep.
	t.Setenv("PAYMENTS_MAX_DAILY_FEE_TRX", "10")
	if _, _, err := treasuryConfigFromEnv(); err == nil {
		t.Fatal("a cap below one sweep's fee was accepted")
	}
}
