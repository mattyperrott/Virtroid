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
	// rejected are the transfers the second node contradicted, by
	// transaction and address: each is said once and not asked about again.
	rejected map[string]bool
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
		rejected:  make(map[string]bool),
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
			log.Printf("payments: index %d: the control plane shows %q, this seed derives %q; check TRON_DEPOSIT_XPUB", watched.Index, watched.Address, derived)
			continue
		}
		recent := watched.ViewedAt != nil && now.Sub(*watched.ViewedAt) < recentlyViewed
		if next, ok := w.nextCheck[watched.Address]; ok && !recent && now.Before(next) {
			continue
		}
		if err := w.checkAddress(ctx, watched, now); err != nil {
			log.Printf("payments: index %d: %q", watched.Index, err)
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
	blockTimes := make(map[string]time.Time)
	var order []string
	latest := since
	for _, transfer := range transfers {
		if transfer.To != watched.Address {
			continue
		}
		if _, seen := totals[transfer.TxID]; !seen {
			order = append(order, transfer.TxID)
			blockTimes[transfer.TxID] = transfer.BlockTime
		}
		totals[transfer.TxID] += transfer.AmountMicro
		if transfer.BlockTime.After(latest) {
			latest = transfer.BlockTime
		}
	}
	// Each transaction stands alone. One that can't be checked or reported
	// now is asked about again next time, and one the second node contradicts
	// is left out; neither keeps the address's other deposits from being
	// credited.
	var waiting []error
	var retryFrom time.Time
	for _, txID := range order {
		if w.rejected[txID+watched.Address] {
			continue
		}
		err := w.credit(ctx, watched, txID, totals[txID])
		switch {
		case err == nil:
		case errors.Is(err, errTransferRejected):
			w.rejected[txID+watched.Address] = true
			log.Printf("payments: index %d: NOT CREDITED: TronGrid lists %s as %d micro-USDT to %s, but %v; check it by hand",
				watched.Index, txID, totals[txID], watched.Address, err)
		default:
			waiting = append(waiting, err)
			if at := blockTimes[txID]; retryFrom.IsZero() || at.Before(retryFrom) {
				retryFrom = at
			}
		}
	}
	// The next scan starts an overlap before the newest transfer seen, never
	// earlier than this one did, and no later than a transfer still waiting.
	next := latest.Add(-scanOverlap)
	if next.Before(since) {
		next = since
	}
	if !retryFrom.IsZero() && retryFrom.Before(next) {
		next = retryFrom
	}
	w.scanned[watched.Address] = next
	return errors.Join(waiting...)
}

// credit checks one transaction on the second node and reports it.
func (w *watcher) credit(ctx context.Context, watched watchedAddress, txID string, amountMicro int64) error {
	blockTime, err := w.verifier.verifyTransfer(ctx, txID, watched.Address, amountMicro)
	if err != nil {
		return fmt.Errorf("verify %s: %w", txID, err)
	}
	credited, err := w.control.reportDeposit(ctx, depositReport{
		TxID:        txID,
		Address:     watched.Address,
		Index:       watched.Index,
		AmountMicro: amountMicro,
		BlockTime:   blockTime,
	})
	if err != nil {
		return fmt.Errorf("report %s: %w", txID, err)
	}
	if credited {
		log.Printf("payments: credited %s to index %d (%d micro-USDT)", txID, watched.Index, amountMicro)
	}
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
