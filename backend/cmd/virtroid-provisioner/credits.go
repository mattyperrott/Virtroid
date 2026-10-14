package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// creditBuyer turns ETH that the payments treasury sends to the provisioner's
// address into Aleph credits on that address, keeping some ETH for the
// purchase's gas. In dry-run mode (the default) it only logs what it would buy.
type creditBuyer struct {
	mode       string
	address    string
	minWei     *big.Int
	reserveWei *big.Int
	balance    func(ctx context.Context, address string) (*big.Int, error)
	buy        func(ctx context.Context, amountETH string) (string, error)
	logf       func(format string, args ...any)
}

const (
	creditBuyOff    = "off"
	creditBuyDryRun = "dry-run"
	creditBuyLive   = "live"
	creditBuyEvery  = time.Hour
)

func newCreditBuyer(mode, address, rpcURL string, driver cliDriver) *creditBuyer {
	rpc := ethBalanceReader{url: rpcURL, client: &http.Client{Timeout: 30 * time.Second}}
	return &creditBuyer{
		mode:       mode,
		address:    address,
		minWei:     big.NewInt(3_000_000_000_000_000), // 0.003 ETH, about the smallest worth a purchase's gas
		reserveWei: big.NewInt(2_000_000_000_000_000), // 0.002 ETH kept for gas
		balance:    rpc.balanceWei,
		buy:        driver.BuyCredits,
		logf:       log.Printf,
	}
}

func (b *creditBuyer) runOnce(ctx context.Context) {
	if b.mode == creditBuyOff || b.address == "" {
		return
	}
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

// BuyCredits pays amountETH of native ETH for Aleph credits on the
// provisioner's own account.
func (d cliDriver) BuyCredits(ctx context.Context, amountETH string) (string, error) {
	out, err := d.run(ctx, "--account", "credit", "buy", "--token", "eth", "--amount", amountETH, "--yes")
	if err != nil {
		return "", err
	}
	return lastLines(string(out), 4), nil
}

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
	var response struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, errors.New(response.Error.Message)
	}
	wei, ok := new(big.Int).SetString(strings.TrimPrefix(response.Result, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("unreadable ETH balance %q", response.Result)
	}
	return wei, nil
}
