package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"sync"
)

const (
	// Bodies this small aren't charged, so ordinary requests keep working while
	// larger ones are refused. The edge's 1500 connections bound them to 24 MiB.
	unchargedSignedBodyBytes int64 = 16 << 10
	// Accounts cost nothing to make, so one account's share isn't enough: the
	// total keeps many of them from filling this server's memory.
	signedBodyBudgetBytes        int64 = 96 << 20
	signedBodyAccountBudgetBytes int64 = 8 << 20
	requestBudgetRetryAfter            = "5"
)

// byteBudget bounds the bytes held for requests in flight, in total and for
// any one account. A nil budget has no limit.
type byteBudget struct {
	total      int64
	perAccount int64

	mu        sync.Mutex
	used      int64
	byAccount map[string]int64
}

func newByteBudget(total, perAccount int64) *byteBudget {
	return &byteBudget{total: total, perAccount: perAccount, byAccount: make(map[string]int64)}
}

// reserve takes n bytes for an account until the returned func is called, or
// reports that they don't fit.
func (b *byteBudget) reserve(accountID string, n int64) (func(), bool) {
	if b == nil {
		return func() {}, true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.total || b.byAccount[accountID]+n > b.perAccount {
		return nil, false
	}
	b.used += n
	b.byAccount[accountID] += n
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.used -= n
			if b.byAccount[accountID] -= n; b.byAccount[accountID] <= 0 {
				delete(b.byAccount, accountID)
			}
		})
	}, true
}

// readSignedBody reads the body of a phone's request, whose signature must
// already have checked out, compares it with the hash that signature covers,
// and puts it back for the handler. The bytes stay charged to the account until
// the request ends.
func (a *API) readSignedBody(w http.ResponseWriter, r *http.Request, accountID, bodyHash, signer string) bool {
	if charge := signedBodyCharge(r); charge > 0 {
		release, ok := a.signedBodies.reserve(accountID, charge)
		if !ok {
			w.Header().Set("Retry-After", requestBudgetRetryAfter)
			writeAPIError(w, http.StatusTooManyRequests, "request_budget_exceeded", "too many large requests are in progress; try again shortly")
			return false
		}
		context.AfterFunc(r.Context(), release)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read request body"})
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	digest := sha256.Sum256(body)
	if base64.RawURLEncoding.EncodeToString(digest[:]) != bodyHash {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": signer + " body hash mismatch"})
		return false
	}
	return true
}

// signedBodyCharge is what a body costs before it arrives: its declared length,
// or the most its route allows when it declares none.
func signedBodyCharge(r *http.Request) int64 {
	switch {
	case r.ContentLength < 0:
		return apiRequestBodyLimit(r.URL.Path)
	case r.ContentLength <= unchargedSignedBodyBytes:
		return 0
	default:
		return r.ContentLength
	}
}
