package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

func testBuyer(mode string, wei *big.Int) (*creditBuyer, *[]string, *[]string) {
	var logs, bought []string
	b := &creditBuyer{
		mode: mode, address: "0x4631b0613fF21cd5715f1b1572b4CD5bc0916c4C",
		minWei: big.NewInt(3_000_000_000_000_000), reserveWei: big.NewInt(2_000_000_000_000_000),
		balance: func(context.Context, string) (*big.Int, error) { return wei, nil },
		buy: func(_ context.Context, amount string) (string, error) {
			bought = append(bought, amount)
			return "ok", nil
		},
		logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	}
	return b, &logs, &bought
}

func TestCreditBuyerKeepsGasAndBuysOnlyWhenLive(t *testing.T) {
	wei, _ := new(big.Int).SetString("12345678901234567", 10) // 0.012345... ETH
	b, logs, bought := testBuyer(creditBuyDryRun, wei)
	b.runOnce(context.Background())
	if len(*bought) != 0 || len(*logs) != 1 || !strings.Contains((*logs)[0], "would buy Aleph credits with 0.010345 ETH") {
		t.Fatalf("dry run: bought=%v logs=%q", *bought, *logs)
	}
	b, _, bought = testBuyer(creditBuyLive, wei)
	b.runOnce(context.Background())
	if len(*bought) != 1 || (*bought)[0] != "0.010345" {
		t.Fatalf("live bought %v", *bought)
	}
}

func TestCreditBuyerWaitsForEnoughETH(t *testing.T) {
	b, logs, bought := testBuyer(creditBuyLive, big.NewInt(4_000_000_000_000_000)) // 0.004: 0.002 spare
	b.runOnce(context.Background())
	if len(*bought) != 0 || len(*logs) != 0 {
		t.Fatalf("bought=%v logs=%q", *bought, *logs)
	}
}
