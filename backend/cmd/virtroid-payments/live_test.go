//go:build tronlive

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestLiveVerification checks a real mainnet USDT transfer on the public
// node and finds it through TronGrid (go test -tags tronlive).
func TestLiveVerification(t *testing.T) {
	client := &http.Client{Timeout: 30 * time.Second}
	node := tronNode{baseURL: "https://tron-rpc.publicnode.com", client: client}
	blockTime, err := node.verifyTransfer(context.Background(), testTxID, testRecipient, 69190552)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	grid := tronGrid{baseURL: "https://api.trongrid.io", client: client}
	transfers, err := grid.transfersTo(context.Background(), testRecipient, blockTime.Add(-time.Minute))
	if err != nil {
		t.Fatalf("transfers: %v", err)
	}
	for _, transfer := range transfers {
		if transfer.TxID == testTxID && transfer.AmountMicro == 69190552 {
			return
		}
	}
	t.Fatalf("TronGrid did not list %s among %d transfers", testTxID, len(transfers))
}

// TestLiveTrxSent reads a real mainnet TRX transfer from TronGrid's
// transaction listing, which the treasury counts its fee top-ups from.
func TestLiveTrxSent(t *testing.T) {
	grid := tronGrid{baseURL: "https://api.trongrid.io", client: &http.Client{Timeout: 30 * time.Second}}
	sent, err := grid.trxSent(context.Background(), testRecipient, time.UnixMilli(1791900000000))
	if err != nil {
		t.Fatalf("TRX sent: %v", err)
	}
	for _, transfer := range sent {
		if strings.HasPrefix(transfer.TxID, "93e2ef106e57") && transfer.AmountSun == 52564500 &&
			transfer.BlockTime.Equal(time.UnixMilli(1791900090000)) {
			return
		}
	}
	t.Fatalf("TronGrid did not list the 52.5645 TRX transfer among %d", len(sent))
}
