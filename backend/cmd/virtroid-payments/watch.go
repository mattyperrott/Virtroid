package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/tron"
)

// paymentsRequestID must match the control plane's fixed payments signer.
const paymentsRequestID = "virtroid-payments"

const (
	// An address whose wallet was opened recently is checked every cycle,
	// any other one this often.
	recentlyViewed  = 24 * time.Hour
	idleCheckEvery  = 30 * time.Minute
	firstScanWindow = 30 * 24 * time.Hour
	// Pages overlap by this much; crediting is idempotent.
	scanOverlap = time.Hour
)

type watchedAddress struct {
	Index    int64      `json:"index"`
	Address  string     `json:"address"`
	ViewedAt *time.Time `json:"viewed_at"`
}

type depositReport struct {
	TxID        string    `json:"tx_id"`
	Address     string    `json:"address"`
	Index       int64     `json:"index"`
	AmountMicro int64     `json:"amount_micro"`
	BlockTime   time.Time `json:"block_time"`
}

type controlPlane interface {
	watchList(ctx context.Context) ([]watchedAddress, error)
	reportDeposit(ctx context.Context, report depositReport) (bool, error)
}

type transferSource interface {
	transfersTo(ctx context.Context, address string, since time.Time) ([]usdtTransfer, error)
}

type transferVerifier interface {
	verifyTransfer(ctx context.Context, txID, address string, amountMicro int64) (time.Time, error)
}

type addressDeriver interface {
	Address(index uint32) (string, error)
}

// watcher finds deposits to the accounts' addresses and reports them.
type watcher struct {
	wallet    addressDeriver
	control   controlPlane
	transfers transferSource
	verifier  transferVerifier
	now       func() time.Time
	scanned   map[string]time.Time
	nextCheck map[string]time.Time
}

func newWatcher(wallet addressDeriver, control controlPlane, transfers transferSource, verifier transferVerifier) *watcher {
	return &watcher{
		wallet:    wallet,
		control:   control,
		transfers: transfers,
		verifier:  verifier,
		now:       time.Now,
		scanned:   make(map[string]time.Time),
		nextCheck: make(map[string]time.Time),
	}
}

// cycle checks the addresses that are due and reports what it finds.
func (w *watcher) cycle(ctx context.Context) error {
	addresses, err := w.control.watchList(ctx)
	if err != nil {
		return fmt.Errorf("watch list: %w", err)
	}
	now := w.now()
	for _, watched := range addresses {
		if watched.Index < 1 || watched.Index >= 1<<31 {
			continue
		}
		// Every address users see must be one this seed can spend.
		derived, err := w.wallet.Address(uint32(watched.Index))
		if err != nil || derived != watched.Address {
			log.Printf("payments: index %d: the control plane shows %s, this seed derives %s; check TRON_DEPOSIT_XPUB", watched.Index, watched.Address, derived)
			continue
		}
		recent := watched.ViewedAt != nil && now.Sub(*watched.ViewedAt) < recentlyViewed
		if next, ok := w.nextCheck[watched.Address]; ok && !recent && now.Before(next) {
			continue
		}
		if err := w.checkAddress(ctx, watched, now); err != nil {
			log.Printf("payments: index %d: %v", watched.Index, err)
			continue
		}
		w.nextCheck[watched.Address] = now.Add(idleCheckEvery)
	}
	return nil
}

func (w *watcher) checkAddress(ctx context.Context, watched watchedAddress, now time.Time) error {
	since, ok := w.scanned[watched.Address]
	if !ok {
		since = now.Add(-firstScanWindow)
	}
	transfers, err := w.transfers.transfersTo(ctx, watched.Address, since)
	if err != nil {
		return err
	}
	// One transaction can carry several transfers to the address.
	totals := make(map[string]int64)
	var order []string
	latest := since
	for _, transfer := range transfers {
		if transfer.To != watched.Address {
			continue
		}
		if _, seen := totals[transfer.TxID]; !seen {
			order = append(order, transfer.TxID)
		}
		totals[transfer.TxID] += transfer.AmountMicro
		if transfer.BlockTime.After(latest) {
			latest = transfer.BlockTime
		}
	}
	for _, txID := range order {
		blockTime, err := w.verifier.verifyTransfer(ctx, txID, watched.Address, totals[txID])
		if err != nil {
			return fmt.Errorf("verify %s: %w", txID, err)
		}
		credited, err := w.control.reportDeposit(ctx, depositReport{
			TxID:        txID,
			Address:     watched.Address,
			Index:       watched.Index,
			AmountMicro: totals[txID],
			BlockTime:   blockTime,
		})
		if err != nil {
			return fmt.Errorf("report %s: %w", txID, err)
		}
		if credited {
			log.Printf("payments: credited %s to index %d (%d micro-USDT)", txID, watched.Index, totals[txID])
		}
	}
	w.scanned[watched.Address] = latest.Add(-scanOverlap)
	return nil
}

// httpControlPlane signs requests with the payments key.
type httpControlPlane struct {
	baseURL    string
	privateKey *ecdsa.PrivateKey
	client     *http.Client
}

func (c httpControlPlane) do(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := nodeauth.ApplySignedHeaders(req, c.privateKey, paymentsRequestID, body, "", "",
		strconv.FormatInt(time.Now().Unix(), 10), uuid.NewString()); err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status=%d %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out != nil {
		return json.Unmarshal(payload, out)
	}
	return nil
}

func (c httpControlPlane) watchList(ctx context.Context) ([]watchedAddress, error) {
	var response struct {
		Addresses []watchedAddress `json:"addresses"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/internal/payments/watch", nil, &response); err != nil {
		return nil, err
	}
	for _, watched := range response.Addresses {
		if !tron.ValidAddress(watched.Address) {
			return nil, fmt.Errorf("the control plane listed an invalid address %q", watched.Address)
		}
	}
	return response.Addresses, nil
}

func (c httpControlPlane) siaWallet(ctx context.Context) (siaWalletState, error) {
	var response struct {
		Sia *siaWalletState `json:"sia"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/internal/payments/treasury", nil, &response); err != nil {
		return siaWalletState{}, err
	}
	if response.Sia == nil {
		return siaWalletState{}, errors.New("the control plane has no indexd wallet")
	}
	return *response.Sia, nil
}

func (c httpControlPlane) reportDeposit(ctx context.Context, report depositReport) (bool, error) {
	body, err := json.Marshal(report)
	if err != nil {
		return false, err
	}
	var response struct {
		Credited bool `json:"credited"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/internal/payments/deposits", body, &response); err != nil {
		return false, err
	}
	return response.Credited, nil
}
