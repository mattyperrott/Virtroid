package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"virtroid/backend/internal/alephapi"
)

// alephAPI reads an address's Aleph credits from Aleph's public API.
type alephAPI struct {
	client alephapi.Client
}

func (a alephAPI) creditBalance(ctx context.Context, address string) (int64, error) {
	return a.client.CreditBalance(ctx, address)
}

func (a alephAPI) dailySpend(ctx context.Context, address string, days int, now time.Time) (int64, error) {
	return a.client.DailySpend(ctx, address, days, now)
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
