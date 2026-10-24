package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"

	"virtroid/backend/internal/tron"
)

// The treasury is the wallet's address at index 0; accounts' deposit
// addresses start at 1. Deposits are swept into it, and from it USDT is
// swapped into what running Virtroid costs: Siacoin for the indexd wallet,
// ETH that the provisioner turns into Aleph credits, and TRX for fees.
//
// In dry-run mode (the default) the treasury only reads balances and quotes
// and logs what it would do. Only "live" signs and sends anything.
const (
	treasuryIndex = 0

	treasuryOff    = "off"
	treasuryDryRun = "dry-run"
	treasuryLive   = "live"

	// pendingTimeout is how long a sent transaction may stay unsolidified
	// before its address is looked at afresh.
	pendingTimeout = 10 * time.Minute
	// refillShare: a balance is topped up once it is this far below target.
	refillShare = 0.10
)

type treasuryConfig struct {
	Mode string
	// Deposits of at least SweepMinMicro USDT are swept; the deposit address
	// needs SweepFeeSun TRX to pay for it, which the treasury sends first.
	SweepMinMicro    int64
	SweepFeeSun      int64
	SweepFeeLimitSun int64
	// The treasury keeps at least TreasuryTRXFloorSun TRX for fees, topping
	// up to TreasuryTRXTargetSun.
	TreasuryTRXFloorSun  int64
	TreasuryTRXTargetSun int64
	// Swaps send only to these addresses, set here rather than taken from
	// the control plane, which only reports balances.
	SiaAddress  string
	SiaTargetSC int64
	// AlephFundAddress receives ETH for credits; AlephSpendAddress is whose
	// spending sets how much (the owner's, while the provisioner deploys on
	// its behalf; the provisioner's own once it pays).
	AlephFundAddress  string
	AlephSpendAddress string
	AlephDays         int
	EthGasReserveWei  *big.Int
	// Caps on what leaves the treasury.
	MinSwapMicro  int64
	MaxSwapMicro  int64
	MaxDailyMicro int64
}

func defaultTreasuryConfig() treasuryConfig {
	return treasuryConfig{
		Mode:                 treasuryDryRun,
		SweepMinMicro:        20_000_000,
		SweepFeeSun:          15_000_000,
		SweepFeeLimitSun:     30_000_000,
		TreasuryTRXFloorSun:  50_000_000,
		TreasuryTRXTargetSun: 150_000_000,
		SiaTargetSC:          20_000,
		AlephDays:            7,
		EthGasReserveWei:     big.NewInt(2_000_000_000_000_000), // 0.002 ETH
		MinSwapMicro:         25_000_000,
		MaxSwapMicro:         200_000_000,
		MaxDailyMicro:        500_000_000,
	}
}

type treasuryWallet interface {
	Key(index uint32) (*btcec.PrivateKey, error)
	Address(index uint32) (string, error)
}

type treasuryControl interface {
	watchList(ctx context.Context) ([]watchedAddress, error)
	siaWallet(ctx context.Context) (siaWalletState, error)
}

// siaWalletState is the indexd wallet as the control plane reports it.
type siaWalletState struct {
	Address           string `json:"address"`
	ConfirmedHastings string `json:"confirmed_hastings"`
}

type treasuryChain interface {
	account(ctx context.Context, address string) (accountBalance, error)
	usdtSent(ctx context.Context, address string, since time.Time) ([]usdtTransfer, error)
	nowBlock(ctx context.Context) (tron.BlockRef, error)
	broadcast(ctx context.Context, rawData, signature []byte) (string, error)
	txOutcome(ctx context.Context, txID string) (bool, bool, error)
}

type swapService interface {
	estimate(ctx context.Context, from, to swapCurrency, amount string, reverse bool) (string, error)
	ranges(ctx context.Context, from, to swapCurrency) (string, string, error)
	create(ctx context.Context, from, to swapCurrency, amount, addressTo, refundAddress string) (swapExchange, error)
	recent(ctx context.Context, since time.Time) ([]swapExchange, error)
}

type creditSource interface {
	creditBalance(ctx context.Context, address string) (int64, error)
	dailySpend(ctx context.Context, address string, days int, now time.Time) (int64, error)
}

type etherSource interface {
	balanceWei(ctx context.Context, address string) (*big.Int, error)
}

type pendingTx struct {
	txID string
	kind string
	at   time.Time
}

type treasury struct {
	cfg     treasuryConfig
	wallet  treasuryWallet
	control treasuryControl
	chain   treasuryChain
	swaps   swapService
	credits creditSource
	eth     etherSource
	now     func() time.Time
	pace    func()
	logf    func(format string, args ...any)
	pending map[string]pendingTx
	told    bool
	// paid is what this process paid exchanges in the last day, until the
	// chain shows it confirmed.
	paid []exchangePayment
}

// exchangePayment is USDT the treasury sent an exchange.
type exchangePayment struct {
	txID  string
	micro int64
	at    time.Time
}

func newTreasury(cfg treasuryConfig, wallet treasuryWallet, control treasuryControl, chain treasuryChain, swaps swapService, credits creditSource, eth etherSource) *treasury {
	return &treasury{
		cfg: cfg, wallet: wallet, control: control, chain: chain, swaps: swaps, credits: credits, eth: eth,
		now:     time.Now,
		pace:    func() { time.Sleep(200 * time.Millisecond) },
		logf:    log.Printf,
		pending: map[string]pendingTx{},
	}
}

func (t *treasury) live() bool { return t.cfg.Mode == treasuryLive }

// plan logs what the treasury does, or in dry-run would do.
func (t *treasury) plan(format string, args ...any) {
	prefix := "treasury: "
	if !t.live() {
		prefix = "treasury (dry run): would "
	}
	t.logf(prefix+format, args...)
}

func (t *treasury) cycle(ctx context.Context) error {
	if t.cfg.Mode == treasuryOff {
		return nil
	}
	treasuryAddress, err := t.wallet.Address(treasuryIndex)
	if err != nil {
		return err
	}
	if !t.told {
		t.logf("treasury: mode %s, address %s", t.cfg.Mode, treasuryAddress)
		t.told = true
	}
	watched, err := t.control.watchList(ctx)
	if err != nil {
		return fmt.Errorf("watch list: %w", err)
	}
	for _, address := range watched {
		if address.Index <= treasuryIndex || address.Index > int64(^uint32(0)>>1) {
			continue
		}
		if err := t.sweep(ctx, address, treasuryAddress); err != nil {
			t.logf("treasury: sweep %s: %v", address.Address, err)
		}
		t.pace()
	}
	return t.convert(ctx, treasuryAddress)
}

// sweep moves a deposit address's USDT into the treasury, first sending it
// the TRX that moving it costs.
func (t *treasury) sweep(ctx context.Context, watched watchedAddress, treasuryAddress string) error {
	now := t.now()
	if pending, ok := t.pending[watched.Address]; ok {
		done, succeeded, err := t.chain.txOutcome(ctx, pending.txID)
		switch {
		case err == nil && done:
			delete(t.pending, watched.Address)
			t.logf("treasury: %s for %s %s (%s)", pending.kind, watched.Address, map[bool]string{true: "succeeded", false: "failed"}[succeeded], pending.txID)
		case now.Sub(pending.at) < pendingTimeout:
			return nil
		default:
			delete(t.pending, watched.Address)
			t.logf("treasury: %s for %s not solidified after %s; looking again", pending.kind, watched.Address, pendingTimeout)
		}
	}
	balance, err := t.chain.account(ctx, watched.Address)
	if err != nil {
		return err
	}
	if balance.USDTMicro < t.cfg.SweepMinMicro {
		return nil
	}
	if balance.TRXSun < t.cfg.SweepFeeSun {
		topUp := t.cfg.SweepFeeSun - balance.TRXSun
		t.plan("send %s TRX from the treasury to %s (index %d) to pay for sweeping its %s USDT",
			sunToTRX(topUp), watched.Address, watched.Index, microToDecimal(balance.USDTMicro))
		if !t.live() {
			return nil
		}
		txID, err := t.sendTRX(ctx, treasuryIndex, treasuryAddress, watched.Address, topUp)
		if err != nil {
			return err
		}
		t.pending[watched.Address] = pendingTx{txID: txID, kind: "fee top-up", at: now}
		return nil
	}
	t.plan("sweep %s USDT from %s (index %d) to the treasury", microToDecimal(balance.USDTMicro), watched.Address, watched.Index)
	if !t.live() {
		return nil
	}
	txID, err := t.sendUSDT(ctx, uint32(watched.Index), watched.Address, treasuryAddress, balance.USDTMicro)
	if err != nil {
		return err
	}
	t.pending[watched.Address] = pendingTx{txID: txID, kind: "sweep", at: now}
	return nil
}

// swapNeed is something the treasury should buy: amount of to, in to's
// units, sent to addressTo.
type swapNeed struct {
	to        swapCurrency
	amount    string
	addressTo string
	why       string
}

// convert swaps treasury USDT for what is running low, within the caps.
func (t *treasury) convert(ctx context.Context, treasuryAddress string) error {
	now := t.now()
	balance, err := t.chain.account(ctx, treasuryAddress)
	if err != nil {
		return fmt.Errorf("treasury balance: %w", err)
	}
	// What the treasury sent in the last day counts against the daily cap from
	// records SimpleSwap can't change: its confirmed USDT transfers on chain,
	// and this process's payments not confirmed yet. SimpleSwap's own history
	// of this partner's exchanges counts too, whichever is larger, and says
	// what is in flight. A restart forgets none of it.
	sentToday, err := t.sentSince(ctx, treasuryAddress, now.Add(-24*time.Hour))
	if err != nil {
		return fmt.Errorf("treasury transfers (no swaps until they can be read): %w", err)
	}
	exchanges, err := t.swaps.recent(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return fmt.Errorf("recent exchanges (no swaps until they can be read): %w", err)
	}
	var swappedToday int64
	inFlight := map[string]bool{}
	for _, exchange := range exchanges {
		if exchange.from() != usdtOnTron {
			continue
		}
		if exchange.Status != "expired" {
			micro, err := decimalToMicro(exchange.AmountFrom, false)
			if err != nil {
				return fmt.Errorf("exchange %s: %w", exchange.ID, err)
			}
			swappedToday += micro
		}
		if !exchange.final() {
			inFlight[exchange.to().String()] = true
		}
	}
	spentToday := max(swappedToday, sentToday)
	budget := min(t.cfg.MaxDailyMicro-spentToday, balance.USDTMicro)
	t.logf("treasury: holds %s USDT and %s TRX; %s USDT swapped in the last day; %s USDT may be swapped now",
		microToDecimal(balance.USDTMicro), sunToTRX(balance.TRXSun), microToDecimal(spentToday), microToDecimal(max(budget, 0)))

	needs := t.needs(ctx, balance, treasuryAddress)
	if len(needs) > 0 && balance.TRXSun < t.cfg.SweepFeeSun {
		t.logf("treasury: holds %s TRX, too little to send USDT; send at least %s TRX to %s",
			sunToTRX(balance.TRXSun), sunToTRX(t.cfg.TreasuryTRXTargetSun), treasuryAddress)
		return nil
	}
	for _, need := range needs {
		if inFlight[need.to.String()] {
			t.logf("treasury: %s; an exchange for %s is still in flight", need.why, need.to)
			continue
		}
		if err := t.swap(ctx, need, treasuryAddress, &budget); err != nil {
			t.logf("treasury: %s: %v", need.why, err)
		}
	}
	return nil
}

func (t *treasury) swap(ctx context.Context, need swapNeed, treasuryAddress string, budget *int64) error {
	// SimpleSwap quotes the other way round (what an amount received costs)
	// only at fixed rates, so the USDT needed is priced from what 100 USDT
	// buys now, with 2% for the rate to move before the swap completes.
	quote, err := t.swaps.estimate(ctx, usdtOnTron, need.to, "100", false)
	if err != nil {
		return fmt.Errorf("quote: %w", err)
	}
	per100, ok := new(big.Rat).SetString(quote)
	if !ok || per100.Sign() <= 0 {
		return fmt.Errorf("unreadable quote %q", quote)
	}
	wantedAmount, ok := new(big.Rat).SetString(need.amount)
	if !ok {
		return fmt.Errorf("unreadable amount %q", need.amount)
	}
	usdt := new(big.Rat).Quo(new(big.Rat).Mul(wantedAmount, big.NewRat(102, 1)), per100)
	wanted, err := decimalToMicro(usdt.FloatString(6), true)
	if err != nil {
		return err
	}
	minimum := t.cfg.MinSwapMicro
	if low, _, err := t.swaps.ranges(ctx, usdtOnTron, need.to); err != nil {
		return fmt.Errorf("ranges: %w", err)
	} else if low != "" {
		if pairMin, err := decimalToMicro(low, true); err == nil {
			minimum = max(minimum, pairMin)
		}
	}
	// A need smaller than the smallest swap buys the smallest swap: the rest
	// is a margin, and waiting would only leave it short.
	amount := min(max(wanted, minimum), t.cfg.MaxSwapMicro, *budget)
	if amount < minimum {
		t.logf("treasury: %s (%s USDT), but %s USDT may be swapped now and the smallest swap is %s USDT",
			need.why, microToDecimal(wanted), microToDecimal(min(t.cfg.MaxSwapMicro, max(*budget, 0))), microToDecimal(minimum))
		return nil
	}
	expected, err := t.swaps.estimate(ctx, usdtOnTron, need.to, microToDecimal(amount), false)
	if err != nil {
		return fmt.Errorf("quote: %w", err)
	}
	t.plan("swap %s USDT for about %s %s, sent to %s: %s", microToDecimal(amount), expected, need.to, need.addressTo, need.why)
	*budget -= amount
	if !t.live() {
		return nil
	}
	exchange, err := t.swaps.create(ctx, usdtOnTron, need.to, microToDecimal(amount), need.addressTo, treasuryAddress)
	if err != nil {
		return fmt.Errorf("create exchange: %w", err)
	}
	sent, err := decimalToMicro(exchange.AmountFrom, false)
	switch {
	case err != nil:
		return fmt.Errorf("exchange %s: %w", exchange.ID, err)
	case exchange.from() != usdtOnTron || exchange.to() != need.to:
		return fmt.Errorf("exchange %s is for %s to %s", exchange.ID, exchange.from(), exchange.to())
	case !strings.EqualFold(exchange.AddressTo, need.addressTo):
		return fmt.Errorf("exchange %s pays %s, not %s", exchange.ID, exchange.AddressTo, need.addressTo)
	case sent != amount:
		return fmt.Errorf("exchange %s asks for %s USDT, not %s", exchange.ID, exchange.AmountFrom, microToDecimal(amount))
	case !tron.ValidAddress(exchange.AddressFrom):
		return fmt.Errorf("exchange %s gave an invalid deposit address", exchange.ID)
	}
	txID, err := t.sendUSDT(ctx, treasuryIndex, treasuryAddress, exchange.AddressFrom, amount)
	if err != nil {
		return fmt.Errorf("pay exchange %s: %w", exchange.ID, err)
	}
	t.paid = append(t.paid, exchangePayment{txID: strings.ToLower(txID), micro: amount, at: t.now()})
	t.logf("treasury: paid exchange %s (%s USDT, transaction %s)", exchange.ID, microToDecimal(amount), txID)
	return nil
}

// sentSince is the USDT the treasury sent since the given time: its confirmed
// transfers on chain, plus this process's payments the chain doesn't show yet.
func (t *treasury) sentSince(ctx context.Context, treasuryAddress string, since time.Time) (int64, error) {
	transfers, err := t.chain.usdtSent(ctx, treasuryAddress, since)
	if err != nil {
		return 0, err
	}
	confirmed := make(map[string]bool, len(transfers))
	var total int64
	for _, transfer := range transfers {
		confirmed[strings.ToLower(transfer.TxID)] = true
		total += transfer.AmountMicro
	}
	kept := t.paid[:0]
	for _, payment := range t.paid {
		if payment.at.Before(since) {
			continue
		}
		kept = append(kept, payment)
		if !confirmed[payment.txID] {
			total += payment.micro
		}
	}
	t.paid = kept
	return total, nil
}

// needs lists what is running low, cheapest to fix first.
func (t *treasury) needs(ctx context.Context, balance accountBalance, treasuryAddress string) []swapNeed {
	var needs []swapNeed
	if balance.TRXSun < t.cfg.TreasuryTRXFloorSun {
		needs = append(needs, swapNeed{
			to: tronTRX, amount: sunToTRX(t.cfg.TreasuryTRXTargetSun - balance.TRXSun), addressTo: treasuryAddress,
			why: fmt.Sprintf("the treasury holds %s TRX for fees, below %s", sunToTRX(balance.TRXSun), sunToTRX(t.cfg.TreasuryTRXFloorSun)),
		})
	}
	if need, ok := t.siaNeed(ctx); ok {
		needs = append(needs, need)
	}
	if need, ok := t.alephNeed(ctx); ok {
		needs = append(needs, need)
	}
	return needs
}

func (t *treasury) siaNeed(ctx context.Context) (swapNeed, bool) {
	if t.cfg.SiaTargetSC <= 0 {
		return swapNeed{}, false
	}
	wallet, err := t.control.siaWallet(ctx)
	if err != nil || wallet.Address == "" {
		t.logf("treasury: the indexd wallet is unknown, so Siacoin waits: %v", err)
		return swapNeed{}, false
	}
	destination := t.cfg.SiaAddress
	if destination == "" {
		if t.live() {
			t.logf("treasury: PAYMENTS_SIA_ADDRESS is not set, so Siacoin waits")
			return swapNeed{}, false
		}
		destination = wallet.Address
	} else if !strings.EqualFold(destination, wallet.Address) {
		t.logf("treasury: indexd's wallet is %s, not PAYMENTS_SIA_ADDRESS %s, so Siacoin waits", wallet.Address, destination)
		return swapNeed{}, false
	}
	hastings, ok := new(big.Int).SetString(strings.TrimSpace(wallet.ConfirmedHastings), 10)
	if !ok {
		t.logf("treasury: unreadable indexd balance %q", wallet.ConfirmedHastings)
		return swapNeed{}, false
	}
	have := new(big.Rat).SetFrac(hastings, new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))
	t.logf("treasury: indexd's wallet %s holds %s SC; target %d SC", destination, have.FloatString(0), t.cfg.SiaTargetSC)
	target := new(big.Rat).SetInt64(t.cfg.SiaTargetSC)
	short := new(big.Rat).Sub(target, have)
	if short.Cmp(new(big.Rat).Mul(target, big.NewRat(int64(refillShare*100), 100))) < 0 {
		return swapNeed{}, false
	}
	return swapNeed{
		to: siacoin, amount: short.FloatString(0), addressTo: destination,
		why: fmt.Sprintf("the indexd wallet holds %s SC, below its %d SC target", have.FloatString(0), t.cfg.SiaTargetSC),
	}, true
}

func (t *treasury) alephNeed(ctx context.Context) (swapNeed, bool) {
	if t.cfg.AlephFundAddress == "" || t.cfg.AlephDays <= 0 {
		return swapNeed{}, false
	}
	spender := t.cfg.AlephSpendAddress
	if spender == "" {
		spender = t.cfg.AlephFundAddress
	}
	perDay, err := t.credits.dailySpend(ctx, spender, t.cfg.AlephDays, t.now())
	if err != nil {
		t.logf("treasury: Aleph credit spend unknown, so ETH waits: %v", err)
		return swapNeed{}, false
	}
	have, err := t.credits.creditBalance(ctx, t.cfg.AlephFundAddress)
	if err != nil {
		t.logf("treasury: Aleph credit balance unknown, so ETH waits: %v", err)
		return swapNeed{}, false
	}
	wei, err := t.eth.balanceWei(ctx, t.cfg.AlephFundAddress)
	if err != nil {
		t.logf("treasury: ETH balance unknown, so ETH waits: %v", err)
		return swapNeed{}, false
	}
	// What 100 USDT buys prices the ETH already waiting to become credits.
	quote, err := t.swaps.estimate(ctx, usdtOnTron, ether, "100", false)
	if err != nil {
		t.logf("treasury: no ETH price, so ETH waits: %v", err)
		return swapNeed{}, false
	}
	ethPer100, ok := new(big.Rat).SetString(quote)
	if !ok || ethPer100.Sign() <= 0 {
		t.logf("treasury: unreadable ETH quote %q", quote)
		return swapNeed{}, false
	}
	usdPerETH := new(big.Rat).Quo(big.NewRat(100, 1), ethPer100)
	spare := new(big.Int).Sub(wei, t.cfg.EthGasReserveWei)
	if spare.Sign() < 0 {
		spare.SetInt64(0)
	}
	// 1 USD buys 1,000,000 credits.
	waiting := new(big.Rat).Mul(new(big.Rat).SetFrac(spare, big.NewInt(1e18)), usdPerETH)
	waiting.Mul(waiting, big.NewRat(1_000_000, 1))
	target := new(big.Rat).SetInt64(perDay * int64(t.cfg.AlephDays))
	t.logf("treasury: %s spends %d Aleph credits a day; %s holds %d credits and %s ETH, %s days of the %d wanted",
		spender, perDay, t.cfg.AlephFundAddress, have, new(big.Rat).SetFrac(wei, big.NewInt(1e18)).FloatString(6),
		daysCovered(have, waiting, perDay), t.cfg.AlephDays)
	short := new(big.Rat).Sub(target, new(big.Rat).Add(new(big.Rat).SetInt64(have), waiting))
	if target.Sign() == 0 || short.Cmp(new(big.Rat).Mul(target, big.NewRat(int64(refillShare*100), 100))) < 0 {
		return swapNeed{}, false
	}
	ethNeeded := new(big.Rat).Quo(new(big.Rat).Quo(short, big.NewRat(1_000_000, 1)), usdPerETH)
	if spare.Sign() == 0 {
		ethNeeded.Add(ethNeeded, new(big.Rat).SetFrac(t.cfg.EthGasReserveWei, big.NewInt(1e18)))
	}
	return swapNeed{
		to: ether, amount: ethNeeded.FloatString(6), addressTo: t.cfg.AlephFundAddress,
		why: fmt.Sprintf("Aleph credits at %s cover %s of the %d days wanted (%d credits a day)",
			t.cfg.AlephFundAddress, daysCovered(have, waiting, perDay), t.cfg.AlephDays, perDay),
	}, true
}

func daysCovered(have int64, waiting *big.Rat, perDay int64) string {
	if perDay <= 0 {
		return "all"
	}
	total := new(big.Rat).Add(new(big.Rat).SetInt64(have), waiting)
	return new(big.Rat).Quo(total, new(big.Rat).SetInt64(perDay)).FloatString(1)
}

func (t *treasury) key(index uint32, address string) (*btcec.PrivateKey, error) {
	key, err := t.wallet.Key(index)
	if err != nil {
		return nil, err
	}
	if tron.AddressFromPublicKey(key.PubKey()) != address {
		key.Zero()
		return nil, fmt.Errorf("index %d does not derive %s", index, address)
	}
	return key, nil
}

func (t *treasury) sendTRX(ctx context.Context, index uint32, from, to string, sun int64) (string, error) {
	key, err := t.key(index, from)
	if err != nil {
		return "", err
	}
	defer key.Zero()
	ref, err := t.chain.nowBlock(ctx)
	if err != nil {
		return "", err
	}
	raw, err := tron.SendTRX(from, to, sun, ref, t.now())
	if err != nil {
		return "", err
	}
	encoded := raw.Encode()
	return t.chain.broadcast(ctx, encoded, tron.Sign(encoded, key))
}

func (t *treasury) sendUSDT(ctx context.Context, index uint32, from, to string, micro int64) (string, error) {
	key, err := t.key(index, from)
	if err != nil {
		return "", err
	}
	defer key.Zero()
	ref, err := t.chain.nowBlock(ctx)
	if err != nil {
		return "", err
	}
	raw, err := tron.SendTRC20(from, tron.USDTContract, to, big.NewInt(micro), t.cfg.SweepFeeLimitSun, ref, t.now())
	if err != nil {
		return "", err
	}
	encoded := raw.Encode()
	return t.chain.broadcast(ctx, encoded, tron.Sign(encoded, key))
}

func microToDecimal(micro int64) string {
	return new(big.Rat).SetFrac64(micro, 1_000_000).FloatString(6)
}

func sunToTRX(sun int64) string {
	return new(big.Rat).SetFrac64(sun, 1_000_000).FloatString(6)
}

// decimalToMicro reads a decimal amount in millionths, rounding up or down.
func decimalToMicro(value string, roundUp bool) (int64, error) {
	amount, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok || amount.Sign() < 0 {
		return 0, fmt.Errorf("unreadable amount %q", value)
	}
	scaled := new(big.Rat).Mul(amount, big.NewRat(1_000_000, 1))
	quotient, remainder := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	if roundUp && remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, errors.New("amount is too large")
	}
	return quotient.Int64(), nil
}
