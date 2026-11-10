package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/tron"
)

const testTxID = "047491a7235e673edc6acaad3860a4cb86c0ddc38e770c5d812463c1c967b4ba"

// The recipient of testTxID on mainnet, which received 69.190552 USDT.
const testRecipient = "TLCEZf651fSe9Wqb9C67ScNjmSYY7ciQwy"

func transactionInfo(t *testing.T, recipient string, amount int64, result, contract string) map[string]any {
	t.Helper()
	raw, err := tron.AddressBytes(recipient)
	if err != nil {
		t.Fatal(err)
	}
	usdt, _ := tron.AddressBytes(tron.USDTContract)
	if contract == "" {
		contract = hex.EncodeToString(usdt)
	}
	return map[string]any{
		"id":               testTxID,
		"blockTimeStamp":   int64(1791900048000),
		"contract_address": contract,
		"receipt":          map[string]any{"result": result},
		"log": []map[string]any{{
			"address": hex.EncodeToString(usdt[1:]),
			"topics": []string{
				transferEventTopic,
				strings.Repeat("0", 24) + "f372e671e8e872e14f17236ab29c7995296a3bad",
				strings.Repeat("0", 24) + hex.EncodeToString(raw[1:]),
			},
			"data": fmt.Sprintf("%064x", amount),
		}},
	}
}

func TestVerifyTransferChecksTheNodesRecord(t *testing.T) {
	var info map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/walletsolidity/gettransactioninfobyid" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	}))
	defer server.Close()
	node := tronNode{baseURL: server.URL, client: server.Client()}
	ctx := context.Background()

	info = transactionInfo(t, testRecipient, 69190552, "SUCCESS", "")
	blockTime, err := node.verifyTransfer(ctx, testTxID, testRecipient, 69190552)
	if err != nil || !blockTime.Equal(time.UnixMilli(1791900048000)) {
		t.Fatalf("verify = %v %v", blockTime, err)
	}
	// A payout that reaches the address through another contract (an
	// exchange's batch send, a swap) is the same deposit: the USDT contract
	// logged the transfer.
	info = transactionInfo(t, testRecipient, 69190552, "SUCCESS", "41"+strings.Repeat("ab", 20))
	if _, err := node.verifyTransfer(ctx, testTxID, testRecipient, 69190552); err != nil {
		t.Fatalf("a transfer made through another contract: %v", err)
	}
	// But not a transfer some other token logged.
	info = transactionInfo(t, testRecipient, 69190552, "SUCCESS", "")
	info["log"].([]map[string]any)[0]["address"] = strings.Repeat("ab", 20)
	if _, err := node.verifyTransfer(ctx, testTxID, testRecipient, 69190552); !errors.Is(err, errTransferRejected) {
		t.Fatalf("a transfer of another token: %v", err)
	}
	info = transactionInfo(t, testRecipient, 69190552, "SUCCESS", "")

	// What the node contradicts is rejected for good; what it doesn't have
	// yet is only not known yet.
	for name, check := range map[string]struct {
		verify   func() error
		rejected bool
	}{
		"amount": {rejected: true, verify: func() error {
			_, err := node.verifyTransfer(ctx, testTxID, testRecipient, 69190553)
			return err
		}},
		"recipient": {rejected: true, verify: func() error {
			_, err := node.verifyTransfer(ctx, testTxID, tron.USDTContract, 69190552)
			return err
		}},
		"failed": {rejected: true, verify: func() error {
			info = transactionInfo(t, testRecipient, 69190552, "REVERT", "")
			_, err := node.verifyTransfer(ctx, testTxID, testRecipient, 69190552)
			return err
		}},
		"unsolidified": {rejected: false, verify: func() error {
			info = map[string]any{}
			_, err := node.verifyTransfer(ctx, testTxID, testRecipient, 69190552)
			return err
		}},
	} {
		err := check.verify()
		if err == nil {
			t.Fatalf("%s: verification passed", name)
		}
		if errors.Is(err, errTransferRejected) != check.rejected {
			t.Fatalf("%s: rejected for good = %v, want %v (%v)", name, !check.rejected, check.rejected, err)
		}
		info = transactionInfo(t, testRecipient, 69190552, "SUCCESS", "")
	}
}

func TestTransfersToFollowsPagesAndKeepsOnlyUSDTToTheAddress(t *testing.T) {
	entry := func(tx, to, token string, value string) map[string]any {
		return map[string]any{
			"transaction_id": tx, "block_timestamp": int64(1791900048000), "to": to, "type": "Transfer", "value": value,
			"token_info": map[string]any{"address": token, "decimals": 6},
		}
	}
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts/"+testRecipient+"/transactions/trc20" || r.URL.Query().Get("only_confirmed") != "true" {
			http.NotFound(w, r)
			return
		}
		pages++
		response := map[string]any{"success": true}
		if r.URL.Query().Get("fingerprint") == "" {
			response["data"] = []any{
				entry(strings.Repeat("a", 64), testRecipient, tron.USDTContract, "5000000"),
				entry(strings.Repeat("b", 64), testRecipient, "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", "9000000"),
				entry(strings.Repeat("c", 64), tron.USDTContract, tron.USDTContract, "9000000"),
			}
			response["meta"] = map[string]any{"fingerprint": "next"}
		} else {
			response["data"] = []any{entry(strings.Repeat("d", 64), testRecipient, tron.USDTContract, "7")}
			response["meta"] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	grid := tronGrid{baseURL: server.URL, client: server.Client()}
	transfers, err := grid.transfersTo(context.Background(), testRecipient, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(transfers) != 2 || transfers[0].AmountMicro != 5000000 || transfers[1].AmountMicro != 7 {
		t.Fatalf("pages=%d transfers=%+v", pages, transfers)
	}
}

type fakeControl struct {
	addresses []watchedAddress
	credited  map[string]int64
	reports   int
}

func (f *fakeControl) watchList(context.Context) ([]watchedAddress, error) { return f.addresses, nil }

func (f *fakeControl) reportDeposit(_ context.Context, report depositReport) (bool, error) {
	f.reports++
	key := report.TxID + report.Address
	if _, ok := f.credited[key]; ok {
		return false, nil
	}
	f.credited[key] = report.AmountMicro
	return true, nil
}

type fakeTransfers struct {
	byAddress map[string][]usdtTransfer
	calls     map[string]int
}

func (f *fakeTransfers) transfersTo(_ context.Context, address string, since time.Time) ([]usdtTransfer, error) {
	f.calls[address]++
	var transfers []usdtTransfer
	for _, transfer := range f.byAddress[address] {
		if !transfer.BlockTime.Before(since) {
			transfers = append(transfers, transfer)
		}
	}
	return transfers, nil
}

type fakeVerifier struct{ fail bool }

func (f fakeVerifier) verifyTransfer(context.Context, string, string, int64) (time.Time, error) {
	if f.fail {
		return time.Time{}, errors.New("not on the second node")
	}
	return time.UnixMilli(1791900048000).UTC(), nil
}

func TestWatcherReportsVerifiedDepositsOnceAndSkipsForeignAddresses(t *testing.T) {
	wallet, err := tron.OpenWallet("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := wallet.Address(1)
	second, _ := wallet.Address(2)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	viewed := now.Add(-time.Hour)
	control := &fakeControl{credited: map[string]int64{}, addresses: []watchedAddress{
		{Index: 1, Address: first, ViewedAt: &viewed},
		{Index: 2, Address: second},
		{Index: 3, Address: testRecipient}, // not this seed's: never watched
	}}
	transfers := &fakeTransfers{calls: map[string]int{}, byAddress: map[string][]usdtTransfer{
		first: {
			{TxID: strings.Repeat("a", 64), To: first, AmountMicro: 3_000_000, BlockTime: now.Add(-time.Minute)},
			{TxID: strings.Repeat("a", 64), To: first, AmountMicro: 2_000_000, BlockTime: now.Add(-time.Minute)},
		},
	}}
	w := newWatcher(wallet, control, transfers, fakeVerifier{})
	w.now = func() time.Time { return now }

	if err := w.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := control.credited[strings.Repeat("a", 64)+first]; got != 5_000_000 {
		t.Fatalf("credited %d, want both transfers of the transaction", got)
	}
	if transfers.calls[testRecipient] != 0 {
		t.Fatal("an address this seed does not derive was watched")
	}

	// Ten minutes later only the recently opened wallet is checked again, and
	// the deposit it already reported is not credited twice.
	now = now.Add(10 * time.Minute)
	if err := w.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transfers.calls[first] != 2 || transfers.calls[second] != 1 || len(control.credited) != 1 || control.reports != 2 {
		t.Fatalf("calls=%v reports=%d credited=%v", transfers.calls, control.reports, control.credited)
	}
	now = now.Add(idleCheckEvery)
	if err := w.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transfers.calls[second] != 2 {
		t.Fatalf("the idle address was not checked again after %s", idleCheckEvery)
	}

	// A transfer the second node cannot confirm is not reported.
	transfers.byAddress[second] = []usdtTransfer{{TxID: strings.Repeat("b", 64), To: second, AmountMicro: 1, BlockTime: now}}
	w.verifier = fakeVerifier{fail: true}
	now = now.Add(idleCheckEvery)
	if err := w.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := control.credited[strings.Repeat("b", 64)+second]; ok {
		t.Fatal("an unverified transfer was reported")
	}
}

// verifierByTx answers per transaction; one it has no answer for verifies.
type verifierByTx map[string]error

func (v verifierByTx) verifyTransfer(_ context.Context, txID, _ string, _ int64) (time.Time, error) {
	if err := v[txID]; err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(1791900048000).UTC(), nil
}

func TestWatcherCreditsADepositBehindOneItCannot(t *testing.T) {
	wallet, err := tron.OpenWallet("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	if err != nil {
		t.Fatal(err)
	}
	address, _ := wallet.Address(1)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	viewed := now.Add(-time.Hour)
	control := &fakeControl{credited: map[string]int64{}, addresses: []watchedAddress{{Index: 1, Address: address, ViewedAt: &viewed}}}
	contradicted, unsettled, fine := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	// Newest first, as TronGrid lists them: the two that can't be credited
	// come before the one that can.
	transfers := &fakeTransfers{calls: map[string]int{}, byAddress: map[string][]usdtTransfer{address: {
		{TxID: contradicted, To: address, AmountMicro: 30_000_000, BlockTime: now.Add(-1 * time.Hour)},
		{TxID: unsettled, To: address, AmountMicro: 40_000_000, BlockTime: now.Add(-5 * time.Hour)},
		{TxID: fine, To: address, AmountMicro: 50_000_000, BlockTime: now.Add(-6 * time.Hour)},
	}}}
	verifier := verifierByTx{
		contradicted: fmt.Errorf("%w: the node shows 0 to the address, not 30000000", errTransferRejected),
		unsettled:    errors.New("the transaction is not solidified yet"),
	}
	w := newWatcher(wallet, control, transfers, verifier)
	w.now = func() time.Time { return now }
	watched := control.addresses[0]

	err = w.checkAddress(context.Background(), watched, now)
	if err == nil || !strings.Contains(err.Error(), unsettled) || strings.Contains(err.Error(), contradicted) {
		t.Fatalf("check = %v, want only the unsettled transfer still waiting", err)
	}
	if got := control.credited[fine+address]; got != 50_000_000 {
		t.Fatalf("the deposit behind the others was credited %d", got)
	}
	if len(control.credited) != 1 {
		t.Fatalf("credited %v", control.credited)
	}
	// The next scan comes back for the unsettled transfer, though it is
	// older than the overlap before the newest one.
	if cursor := w.scanned[address]; cursor.After(now.Add(-5 * time.Hour)) {
		t.Fatalf("the next scan starts at %s, after the transfer still waiting", cursor)
	}

	// Once the node has it, it is credited; the contradicted one is never
	// asked about again, and never credited.
	delete(verifier, unsettled)
	verifier[contradicted] = errors.New("asked about a rejected transfer again")
	if err := w.checkAddress(context.Background(), watched, now.Add(time.Minute)); err != nil {
		t.Fatalf("second check: %v", err)
	}
	if got := control.credited[unsettled+address]; got != 40_000_000 {
		t.Fatalf("the transfer that settled was credited %d", got)
	}
	if _, ok := control.credited[contradicted+address]; ok || len(control.credited) != 2 {
		t.Fatalf("credited %v", control.credited)
	}
}

func TestWatcherScanNeverStartsEarlierThanTheLast(t *testing.T) {
	wallet, err := tron.OpenWallet("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	if err != nil {
		t.Fatal(err)
	}
	address, _ := wallet.Address(1)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	control := &fakeControl{credited: map[string]int64{}}
	w := newWatcher(wallet, control, &fakeTransfers{calls: map[string]int{}, byAddress: map[string][]usdtTransfer{}}, fakeVerifier{})
	watched := watchedAddress{Index: 1, Address: address}
	for range 3 {
		if err := w.checkAddress(context.Background(), watched, now); err != nil {
			t.Fatal(err)
		}
	}
	if cursor, want := w.scanned[address], now.Add(-firstScanWindow); !cursor.Equal(want) {
		t.Fatalf("an address with no transfers is scanned from %s, want %s", cursor, want)
	}
}

func TestRequestGateSpacesRequests(t *testing.T) {
	gate := newRequestGate(30 * time.Millisecond)
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := gate.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Fatalf("four requests took %s, want at least 90ms", elapsed)
	}
}
