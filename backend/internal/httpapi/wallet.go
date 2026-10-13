package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"virtroid/backend/internal/store"
	"virtroid/backend/internal/tron"
)

// paymentsRequestID is the signer the payments service uses.
const paymentsRequestID = "virtroid-payments"

// maxWatchedDeposits caps one watch list.
const maxWatchedDeposits = 5000

var tronTxID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *API) depositKey() (*tron.DepositKey, bool) {
	if strings.TrimSpace(a.cfg.TronDepositXPub) == "" {
		return nil, false
	}
	key, err := tron.ParseDepositXPub(a.cfg.TronDepositXPub)
	if err != nil {
		return nil, false
	}
	return key, true
}

// getMyWallet shows the account's USDT balance, its deposit address and
// recent history.
func (a *API) getMyWallet(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}
	deposits, ok := a.depositKey()
	if !ok {
		writeAPIError(w, http.StatusNotFound, "wallet_unavailable", "deposits are not enabled yet")
		return
	}
	wallet, err := a.store.AccountWallet(r.Context(), accountID)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeAPIError(w, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err != nil {
		writeInternalAPIError(w, "wallet_lookup_failed", err)
		return
	}
	address, err := deposits.Address(uint32(wallet.DepositIndex))
	if err != nil {
		writeInternalAPIError(w, "deposit_address_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"currency":                "USDT",
		"network":                 "TRON",
		"token_contract":          tron.USDTContract,
		"balance_micro":           wallet.BalanceMicro,
		"deposit_address":         address,
		"deposits":                nonNilSlice(wallet.Deposits),
		"entries":                 nonNilSlice(wallet.Entries),
		"plan":                    wallet.Plan,
		"trial_seconds_remaining": wallet.TrialSecondsLeft,
		// What use costs once the account has paid.
		"runtime_price_per_hour_micro":      a.cfg.RuntimePriceHourMicro,
		"storage_price_per_gib_month_micro": a.cfg.StoragePriceGiBMonthMicro,
	})
}

func nonNilSlice[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func (a *API) requirePaymentsRequest(w http.ResponseWriter, r *http.Request) (*tron.DepositKey, bool) {
	deposits, ok := a.depositKey()
	if !ok || strings.TrimSpace(a.cfg.PaymentsPublicKey) == "" {
		writeAPIError(w, http.StatusNotFound, "payments_disabled", "deposits are not enabled")
		return nil, false
	}
	if !a.requireServiceRequest(w, r, paymentsRequestID, a.cfg.PaymentsPublicKey) {
		return nil, false
	}
	a.touchServiceHeartbeat(r, "payments")
	return deposits, true
}

// paymentsWatchList gives the payments service the deposit addresses to
// watch, those whose wallet was opened most recently first.
func (a *API) paymentsWatchList(w http.ResponseWriter, r *http.Request) {
	deposits, ok := a.requirePaymentsRequest(w, r)
	if !ok {
		return
	}
	watched, err := a.store.DepositWatchList(r.Context(), maxWatchedDeposits)
	if err != nil {
		writeInternalAPIError(w, "deposit_watch_list_failed", err)
		return
	}
	type entry struct {
		Index    int64      `json:"index"`
		Address  string     `json:"address"`
		ViewedAt *time.Time `json:"viewed_at,omitempty"`
	}
	addresses := make([]entry, 0, len(watched))
	for _, watch := range watched {
		address, err := deposits.Address(uint32(watch.Index))
		if err != nil {
			writeInternalAPIError(w, "deposit_address_failed", err)
			return
		}
		addresses = append(addresses, entry{Index: watch.Index, Address: address, ViewedAt: watch.ViewedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"addresses": addresses})
}

// paymentsDeposit credits a confirmed USDT transfer the payments service
// found. The address must be the one this control plane derives for the
// index, so the payments service cannot credit an address it made up.
func (a *API) paymentsDeposit(w http.ResponseWriter, r *http.Request) {
	deposits, ok := a.requirePaymentsRequest(w, r)
	if !ok {
		return
	}
	var request struct {
		TxID        string    `json:"tx_id"`
		Address     string    `json:"address"`
		Index       int64     `json:"index"`
		AmountMicro int64     `json:"amount_micro"`
		BlockTime   time.Time `json:"block_time"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_deposit", "invalid deposit report")
		return
	}
	request.TxID = strings.ToLower(strings.TrimSpace(request.TxID))
	if !tronTxID.MatchString(request.TxID) || request.AmountMicro <= 0 || request.BlockTime.IsZero() ||
		request.Index < 1 || request.Index > 1<<31-1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_deposit", "invalid deposit report")
		return
	}
	expected, err := deposits.Address(uint32(request.Index))
	if err != nil || expected != strings.TrimSpace(request.Address) {
		writeAPIError(w, http.StatusBadRequest, "deposit_address_mismatch", "the address is not this index's deposit address")
		return
	}
	credited, err := a.store.CreditDeposit(r.Context(), store.DepositCredit{
		TxID:        request.TxID,
		Address:     expected,
		Index:       request.Index,
		AmountMicro: request.AmountMicro,
		BlockTime:   request.BlockTime,
	})
	if errors.Is(err, store.ErrDepositAccountNotFound) {
		writeAPIError(w, http.StatusNotFound, "deposit_account_not_found", err.Error())
		return
	}
	if err != nil {
		writeInternalAPIError(w, "deposit_credit_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credited": credited})
}
