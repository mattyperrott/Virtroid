package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// alephAPI reads an address's Aleph credits from Aleph's public API.
type alephAPI struct {
	baseURL string
	client  *http.Client
}

func (a alephAPI) creditBalance(ctx context.Context, address string) (int64, error) {
	var response struct {
		CreditBalance int64 `json:"credit_balance"`
	}
	if err := a.get(ctx, "/api/v0/addresses/"+url.PathEscape(address)+"/balance", &response); err != nil {
		return 0, err
	}
	return response.CreditBalance, nil
}

// dailySpend is the credits the address spent per day, on average, over the
// last days days.
func (a alephAPI) dailySpend(ctx context.Context, address string, days int, now time.Time) (int64, error) {
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	var spent int64
	for page := 1; page <= 100; page++ {
		var response struct {
			History []struct {
				Amount        int64  `json:"amount"`
				PaymentMethod string `json:"payment_method"`
				Timestamp     string `json:"message_timestamp"`
			} `json:"credit_history"`
		}
		route := fmt.Sprintf("/api/v0/addresses/%s/credit_history?pagination=200&page=%d", url.PathEscape(address), page)
		if err := a.get(ctx, route, &response); err != nil {
			return 0, err
		}
		if len(response.History) == 0 {
			break
		}
		older := false
		for _, entry := range response.History {
			at, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
			if err != nil {
				return 0, fmt.Errorf("unreadable credit history time %q", entry.Timestamp)
			}
			if at.Before(since) {
				older = true
				continue
			}
			if entry.PaymentMethod == "credit_expense" && entry.Amount < 0 {
				spent -= entry.Amount
			}
		}
		if older {
			break
		}
	}
	return spent / int64(days), nil
}

func (a alephAPI) get(ctx context.Context, route string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.baseURL, "/")+route, nil)
	if err != nil {
		return err
	}
	return doJSON(a.client, req, out)
}

// ethereumRPC reads ETH balances from an Ethereum JSON-RPC node.
type ethereumRPC struct {
	url    string
	client *http.Client
}

func (e ethereumRPC) balanceWei(ctx context.Context, address string) (*big.Int, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_getBalance", "params": []string{address, "latest"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var response struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := doJSON(e.client, req, &response); err != nil {
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
