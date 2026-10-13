//go:build tronlive

package main

import (
	"context"
	"net/http"
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
