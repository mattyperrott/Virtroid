package main

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
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

func testForwarder(mode string, credits int64) (*creditBuyer, *[]string, *[]string) {
	b, logs, _ := testBuyer(mode, big.NewInt(0))
	var forwarded []string
	b.chargedAddress = "0x0aBe3c402B41CC339B7A101Ad940214cFB947a08"
	b.minForward = 100_000
	b.credits = func(_ context.Context, address string) (int64, error) {
		if address != b.address {
			return 0, fmt.Errorf("read the credits of %s", address)
		}
		return credits, nil
	}
	b.forward = func(_ context.Context, to string, amount int64) (string, error) {
		forwarded = append(forwarded, fmt.Sprintf("%d to %s", amount, to))
		return "hash", nil
	}
	return b, logs, &forwarded
}

func TestCreditBuyerForwardsCreditsToTheChargedAccount(t *testing.T) {
	const owner = "0x0aBe3c402B41CC339B7A101Ad940214cFB947a08"
	b, logs, forwarded := testForwarder(creditBuyDryRun, 5_000_000)
	b.runOnce(context.Background())
	if len(*forwarded) != 0 || len(*logs) != 1 || !strings.Contains((*logs)[0], "would forward 5000000 credits") {
		t.Fatalf("dry run: forwarded=%v logs=%q", *forwarded, *logs)
	}
	b, _, forwarded = testForwarder(creditBuyLive, 5_000_000)
	b.runOnce(context.Background())
	if len(*forwarded) != 1 || (*forwarded)[0] != "5000000 to "+owner {
		t.Fatalf("live forwarded %v", *forwarded)
	}
}

func TestCreditBuyerForwardsOnlyWhenThereIsSomewhereToForward(t *testing.T) {
	b, _, forwarded := testForwarder(creditBuyLive, 99_999)
	b.runOnce(context.Background())
	if len(*forwarded) != 0 {
		t.Fatalf("forwarded dust: %v", *forwarded)
	}
	// The provisioner pays for its own VMs: its credits are already in place.
	for _, charged := range []string{"", strings.ToLower(b.address)} {
		b, _, forwarded = testForwarder(creditBuyLive, 5_000_000)
		b.chargedAddress = charged
		b.runOnce(context.Background())
		if len(*forwarded) != 0 {
			t.Fatalf("charged=%q forwarded %v", charged, *forwarded)
		}
	}
	b, _, forwarded = testForwarder(creditBuyOff, 5_000_000)
	b.runOnce(context.Background())
	if len(*forwarded) != 0 {
		t.Fatalf("off, yet forwarded %v", *forwarded)
	}
}

func TestCreditBuyerForwardsEvenWithoutETHToSpend(t *testing.T) {
	b, _, forwarded := testForwarder(creditBuyLive, 5_000_000)
	b.balance = func(context.Context, string) (*big.Int, error) { return nil, fmt.Errorf("RPC down") }
	b.runOnce(context.Background())
	if len(*forwarded) != 1 {
		t.Fatalf("forwarded %v", *forwarded)
	}
}

func TestCreditBuyerWaitsForEnoughETH(t *testing.T) {
	b, logs, bought := testBuyer(creditBuyLive, big.NewInt(4_000_000_000_000_000)) // 0.004: 0.002 spare
	b.runOnce(context.Background())
	if len(*bought) != 0 || len(*logs) != 0 {
		t.Fatalf("bought=%v logs=%q", *bought, *logs)
	}
}

func TestBalanceWeiBoundsTheRPCAnswer(t *testing.T) {
	answers := map[string]func(http.ResponseWriter){
		"/huge": func(w http.ResponseWriter) {
			// A string far past the read limit: decoding must stop, not buffer it.
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x`))
			chunk := bytes.Repeat([]byte("f"), 1<<20)
			for range 8 {
				_, _ = w.Write(chunk)
			}
			_, _ = w.Write([]byte(`"}`))
		},
		"/long": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("f", 65) + `"}`))
		},
		"/status": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
		},
		"/ok": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xde0b6b3a7640000"}`))
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answers[r.URL.Path](w)
	}))
	defer server.Close()
	for _, path := range []string{"/huge", "/long", "/status"} {
		reader := ethBalanceReader{url: server.URL + path, client: server.Client()}
		if _, err := reader.balanceWei(context.Background(), "0x0000000000000000000000000000000000000001"); err == nil {
			t.Fatalf("%s: accepted", path)
		}
	}
	reader := ethBalanceReader{url: server.URL + "/ok", client: server.Client()}
	wei, err := reader.balanceWei(context.Background(), "0x0000000000000000000000000000000000000001")
	if err != nil || wei.String() != "1000000000000000000" {
		t.Fatalf("balance = %v, %v", wei, err)
	}
}
