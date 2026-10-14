package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// swapCurrency names a coin on a network, as SimpleSwap does.
type swapCurrency struct {
	Ticker  string
	Network string
}

func (c swapCurrency) String() string { return c.Ticker + ":" + c.Network }

var (
	usdtOnTron = swapCurrency{Ticker: "usdt", Network: "trx"}
	tronTRX    = swapCurrency{Ticker: "trx", Network: "trx"}
	siacoin    = swapCurrency{Ticker: "sc", Network: "sc"}
	ether      = swapCurrency{Ticker: "eth", Network: "eth"}
)

// swapExchange is one exchange on SimpleSwap.
type swapExchange struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
	TickerFrom     string `json:"tickerFrom"`
	NetworkFrom    string `json:"networkFrom"`
	TickerTo       string `json:"tickerTo"`
	NetworkTo      string `json:"networkTo"`
	AmountFrom     string `json:"amountFrom"`
	ExpectedAmount string `json:"expectedAmount"`
	AddressFrom    string `json:"addressFrom"`
	AddressTo      string `json:"addressTo"`
}

func (e swapExchange) from() swapCurrency { return swapCurrency{e.TickerFrom, e.NetworkFrom} }
func (e swapExchange) to() swapCurrency   { return swapCurrency{e.TickerTo, e.NetworkTo} }

// final reports whether the exchange will not move funds any more.
func (e swapExchange) final() bool {
	switch e.Status {
	case "finished", "failed", "refunded", "expired":
		return true
	}
	return false
}

// simpleSwap is SimpleSwap's partner API (v3), floating rate only.
type simpleSwap struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func (s simpleSwap) estimate(ctx context.Context, from, to swapCurrency, amount string, reverse bool) (string, error) {
	query := pairQuery(from, to)
	query.Set("amount", amount)
	query.Set("reverse", fmt.Sprint(reverse))
	var response struct {
		Result struct {
			EstimatedAmount string `json:"estimatedAmount"`
		} `json:"result"`
	}
	if err := s.call(ctx, http.MethodGet, "/v3/estimates?"+query.Encode(), nil, &response); err != nil {
		return "", err
	}
	if response.Result.EstimatedAmount == "" {
		return "", errors.New("SimpleSwap gave no estimate")
	}
	return response.Result.EstimatedAmount, nil
}

// ranges is the smallest and largest amount of from the pair takes; max is
// "" when unbounded.
func (s simpleSwap) ranges(ctx context.Context, from, to swapCurrency) (string, string, error) {
	var response struct {
		Result struct {
			Min string `json:"min"`
			Max string `json:"max"`
		} `json:"result"`
	}
	if err := s.call(ctx, http.MethodGet, "/v3/ranges?"+pairQuery(from, to).Encode(), nil, &response); err != nil {
		return "", "", err
	}
	return response.Result.Min, response.Result.Max, nil
}

func (s simpleSwap) create(ctx context.Context, from, to swapCurrency, amount, addressTo, refundAddress string) (swapExchange, error) {
	body := map[string]any{
		"tickerFrom": from.Ticker, "networkFrom": from.Network,
		"tickerTo": to.Ticker, "networkTo": to.Network,
		"amount": amount, "addressTo": addressTo,
		"fixed": false, "reverse": false,
		"userRefundAddress": refundAddress,
	}
	var response struct {
		Result swapExchange `json:"result"`
	}
	if err := s.call(ctx, http.MethodPost, "/v3/exchanges", body, &response); err != nil {
		return swapExchange{}, err
	}
	return response.Result, nil
}

// recent lists this partner's exchanges created since the given time.
func (s simpleSwap) recent(ctx context.Context, since time.Time) ([]swapExchange, error) {
	query := url.Values{}
	query.Set("limit", "100")
	query.Set("startDate", since.UTC().Format(time.RFC3339))
	var response struct {
		Result []swapExchange `json:"result"`
	}
	if err := s.call(ctx, http.MethodGet, "/v3/exchanges?"+query.Encode(), nil, &response); err != nil {
		return nil, err
	}
	return response.Result, nil
}

func pairQuery(from, to swapCurrency) url.Values {
	query := url.Values{}
	query.Set("tickerFrom", from.Ticker)
	query.Set("networkFrom", from.Network)
	query.Set("tickerTo", to.Ticker)
	query.Set("networkTo", to.Network)
	query.Set("fixed", "false")
	return query
}

func (s simpleSwap) call(ctx context.Context, method, route string, body any, out any) error {
	if s.apiKey == "" {
		return errors.New("no SimpleSwap API key is configured")
	}
	var payload *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	} else {
		payload = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.baseURL, "/")+route, payload)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return doJSON(s.client, req, out)
}
