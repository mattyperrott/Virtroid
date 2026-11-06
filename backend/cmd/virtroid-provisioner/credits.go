package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"virtroid/backend/internal/alephapi"
)

// creditBuyer turns ETH that the payments treasury sends to the provisioner's
// address into Aleph credits on that address, keeping some ETH for the
// purchase's gas. A purchase always lands on the buyer's own account, so while
// VMs are charged to another (chargedAddress) it forwards them there, or they
// would sit where nothing spends them. In dry-run mode (the default) it only
// logs what it would do.
type creditBuyer struct {
	mode           string
	address        string
	chargedAddress string
	minWei         *big.Int
	reserveWei     *big.Int
	minForward     int64
	balance        func(ctx context.Context, address string) (*big.Int, error)
	buy            func(ctx context.Context, amountETH string) (string, error)
	credits        func(ctx context.Context, address string) (int64, error)
	forward        func(ctx context.Context, to string, credits int64) (string, error)
	logf           func(format string, args ...any)
}

const (
	creditBuyOff    = "off"
	creditBuyDryRun = "dry-run"
	creditBuyLive   = "live"
	creditBuyEvery  = time.Hour
)

func newCreditBuyer(mode, address, rpcURL string, driver cliDriver, aleph alephapi.Client) *creditBuyer {
	rpc := ethBalanceReader{url: rpcURL, client: &http.Client{Timeout: 30 * time.Second}}
	return &creditBuyer{
		mode:           mode,
		address:        address,
		chargedAddress: driver.owner,
		minWei:         big.NewInt(3_000_000_000_000_000), // 0.003 ETH, about the smallest worth a purchase's gas
		reserveWei:     big.NewInt(2_000_000_000_000_000), // 0.002 ETH kept for gas
		minForward:     100_000,                           // 0.10 USD; less isn't worth a message
		balance:        rpc.balanceWei,
		buy:            driver.BuyCredits,
		credits:        aleph.CreditBalance,
		forward:        driver.TransferCredits,
		logf:           log.Printf,
	}
}

func (b *creditBuyer) runOnce(ctx context.Context) {
	if b.mode == creditBuyOff || b.address == "" {
		return
	}
	b.buyWithSpareETH(ctx)
	// A purchase takes Aleph a while to credit, so what was just bought goes
	// on the next pass; an earlier purchase, or a failed forward, goes now.
	b.forwardCredits(ctx)
}

func (b *creditBuyer) buyWithSpareETH(ctx context.Context) {
	wei, err := b.balance(ctx, b.address)
	if err != nil {
		b.logf("provisioner credits: ETH balance of %s: %v", b.address, err)
		return
	}
	spare := new(big.Int).Sub(wei, b.reserveWei)
	if spare.Cmp(b.minWei) < 0 {
		return
	}
	// Whole millionths of an ETH, rounded down.
	micro := new(big.Int).Quo(spare, big.NewInt(1_000_000_000_000))
	amount := new(big.Rat).SetFrac(micro, big.NewInt(1_000_000)).FloatString(6)
	if b.mode != creditBuyLive {
		b.logf("provisioner credits (dry run): would buy Aleph credits with %s ETH of %s's %s ETH",
			amount, b.address, new(big.Rat).SetFrac(wei, big.NewInt(1e18)).FloatString(6))
		return
	}
	out, err := b.buy(ctx, amount)
	if err != nil {
		b.logf("provisioner credits: buying with %s ETH: %v", amount, err)
		return
	}
	b.logf("provisioner credits: bought Aleph credits with %s ETH: %s", amount, out)
}

// forwardCredits moves the buyer's credits to the account VMs are charged to.
func (b *creditBuyer) forwardCredits(ctx context.Context) {
	if b.chargedAddress == "" || strings.EqualFold(b.chargedAddress, b.address) {
		return
	}
	credits, err := b.credits(ctx, b.address)
	if err != nil {
		b.logf("provisioner credits: credit balance of %s: %v", b.address, err)
		return
	}
	if credits < b.minForward {
		return
	}
	if b.mode != creditBuyLive {
		b.logf("provisioner credits (dry run): would forward %d credits from %s to %s", credits, b.address, b.chargedAddress)
		return
	}
	message, err := b.forward(ctx, b.chargedAddress, credits)
	if err != nil {
		b.logf("provisioner credits: forwarding %d credits to %s: %v", credits, b.chargedAddress, err)
		return
	}
	b.logf("provisioner credits: forwarded %d credits to %s: %s", credits, b.chargedAddress, message)
}

// BuyCredits pays amountETH of native ETH for Aleph credits on the
// provisioner's own account.
func (d cliDriver) BuyCredits(ctx context.Context, amountETH string) (string, error) {
	out, err := d.run(ctx, "--account", "credit", "buy", "--token", "eth", "--amount", amountETH, "--yes")
	if err != nil {
		return "", err
	}
	return lastLines(string(out), 4), nil
}

// TransferCredits gives credits from the provisioner's own account to another
// account, by a signed message that costs no gas.
func (d cliDriver) TransferCredits(ctx context.Context, to string, credits int64) (string, error) {
	out, err := d.run(ctx, "--account", "credit", "transfer", "--to", to, "--amount", strconv.FormatInt(credits, 10), "--yes")
	if err != nil {
		return "", err
	}
	return parseItemHash(out, "credit transfer")
}

// maxEthRPCResponseBytes bounds an eth_getBalance answer.
const maxEthRPCResponseBytes = 64 << 10

type ethBalanceReader struct {
	url    string
	client *http.Client
}

func (e ethBalanceReader) balanceWei(ctx context.Context, address string) (*big.Int, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_getBalance", "params": []string{address, "latest"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ETH RPC answered status %d", resp.StatusCode)
	}
	var response struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	// The RPC service is outside our control: a balance answer is tiny, so
	// read no more than this, and never a huge string into memory.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEthRPCResponseBytes)).Decode(&response); err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, errors.New(shorten(response.Error.Message, 200))
	}
	// A 256-bit balance is at most 64 hex digits.
	digits := strings.TrimPrefix(response.Result, "0x")
	if digits == "" || len(digits) > 64 {
		return nil, fmt.Errorf("unreadable ETH balance %q", shorten(response.Result, 80))
	}
	wei, ok := new(big.Int).SetString(digits, 16)
	if !ok {
		return nil, fmt.Errorf("unreadable ETH balance %q", response.Result)
	}
	return wei, nil
}

// shorten keeps an untrusted message to at most n bytes for a log or error.
func shorten(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[:n] + "…"
}
