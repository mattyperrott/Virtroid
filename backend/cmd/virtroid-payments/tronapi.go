package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"virtroid/backend/internal/tron"
)

// transferEventTopic is keccak256("Transfer(address,address,uint256)").
const transferEventTopic = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

const maxAPIResponse = 4 << 20

// usdtTransfer is a confirmed USDT transfer to a watched address.
type usdtTransfer struct {
	TxID        string
	To          string
	AmountMicro int64
	BlockTime   time.Time
}

// tronGrid lists TRC20 transfers to an address through TronGrid's indexed API.
type tronGrid struct {
	baseURL string
	apiKey  string
	client  *http.Client
	// gate spaces requests: without an API key TronGrid takes about one a
	// second, shared by the deposit watcher and the treasury.
	gate *requestGate
}

// requestGate lets one request through per interval.
type requestGate struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newRequestGate(interval time.Duration) *requestGate {
	return &requestGate{interval: interval}
}

func (g *requestGate) wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	now := time.Now()
	at := g.next
	if at.Before(now) {
		at = now
	}
	g.next = at.Add(g.interval)
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(at)):
		return nil
	}
}

// transfersTo lists confirmed USDT transfers to address since the given time,
// newest first, following TronGrid's pages.
func (g tronGrid) transfersTo(ctx context.Context, address string, since time.Time) ([]usdtTransfer, error) {
	return g.transfers(ctx, address, since, true)
}

// transfersFrom lists confirmed USDT transfers from address since the given
// time, newest first.
func (g tronGrid) transfersFrom(ctx context.Context, address string, since time.Time) ([]usdtTransfer, error) {
	return g.transfers(ctx, address, since, false)
}

func (g tronGrid) transfers(ctx context.Context, address string, since time.Time, incoming bool) ([]usdtTransfer, error) {
	query := url.Values{}
	if incoming {
		query.Set("only_to", "true")
	} else {
		query.Set("only_from", "true")
	}
	query.Set("only_confirmed", "true")
	query.Set("limit", "200")
	query.Set("contract_address", tron.USDTContract)
	query.Set("min_timestamp", strconv.FormatInt(since.UnixMilli(), 10))
	var transfers []usdtTransfer
	for page := 0; page < 50; page++ {
		var response struct {
			Success bool `json:"success"`
			Data    []struct {
				TransactionID  string `json:"transaction_id"`
				BlockTimestamp int64  `json:"block_timestamp"`
				From           string `json:"from"`
				To             string `json:"to"`
				Type           string `json:"type"`
				Value          string `json:"value"`
				TokenInfo      struct {
					Address  string `json:"address"`
					Decimals int    `json:"decimals"`
				} `json:"token_info"`
			} `json:"data"`
			Meta struct {
				Fingerprint string `json:"fingerprint"`
			} `json:"meta"`
		}
		endpoint := strings.TrimRight(g.baseURL, "/") + "/v1/accounts/" + url.PathEscape(address) + "/transactions/trc20?" + query.Encode()
		if err := g.getJSON(ctx, endpoint, &response); err != nil {
			return nil, err
		}
		if !response.Success {
			return nil, errors.New("trongrid reported a failure")
		}
		for _, item := range response.Data {
			party := item.From
			if incoming {
				party = item.To
			}
			if item.Type != "Transfer" || party != address || item.TokenInfo.Address != tron.USDTContract || item.TokenInfo.Decimals != 6 {
				continue
			}
			amount, err := strconv.ParseInt(item.Value, 10, 64)
			if err != nil || amount <= 0 {
				continue
			}
			transfers = append(transfers, usdtTransfer{
				TxID:        strings.ToLower(item.TransactionID),
				To:          item.To,
				AmountMicro: amount,
				BlockTime:   time.UnixMilli(item.BlockTimestamp).UTC(),
			})
		}
		if response.Meta.Fingerprint == "" || len(response.Data) == 0 {
			return transfers, nil
		}
		query.Set("fingerprint", response.Meta.Fingerprint)
	}
	return nil, errors.New("too many pages of transfers")
}

func (g tronGrid) getJSON(ctx context.Context, endpoint string, out any) error {
	if err := g.gate.wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if g.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", g.apiKey)
	}
	return doJSON(g.client, req, out)
}

// tronNode checks a transaction on a second, independent full node, so one
// provider alone cannot invent a deposit.
type tronNode struct {
	baseURL string
	client  *http.Client
}

// verifyTransfer checks that txID is a solidified, successful USDT transfer
// of exactly amountMicro to address, and returns its block time.
func (n tronNode) verifyTransfer(ctx context.Context, txID, address string, amountMicro int64) (time.Time, error) {
	body, _ := json.Marshal(map[string]string{"value": txID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(n.baseURL, "/")+"/walletsolidity/gettransactioninfobyid", bytes.NewReader(body))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	var info struct {
		ID              string `json:"id"`
		BlockTimeStamp  int64  `json:"blockTimeStamp"`
		ContractAddress string `json:"contract_address"`
		Receipt         struct {
			Result string `json:"result"`
		} `json:"receipt"`
		Log []struct {
			Address string   `json:"address"`
			Topics  []string `json:"topics"`
			Data    string   `json:"data"`
		} `json:"log"`
	}
	if err := doJSON(n.client, req, &info); err != nil {
		return time.Time{}, err
	}
	if !strings.EqualFold(info.ID, txID) {
		return time.Time{}, errors.New("the transaction is not solidified yet")
	}
	if info.Receipt.Result != "SUCCESS" {
		return time.Time{}, fmt.Errorf("the transaction did not succeed: %q", info.Receipt.Result)
	}
	usdt, _ := tron.AddressBytes(tron.USDTContract)
	if !strings.EqualFold(info.ContractAddress, hex.EncodeToString(usdt)) {
		return time.Time{}, errors.New("the transaction did not call the USDT contract")
	}
	recipient, err := tron.AddressBytes(address)
	if err != nil {
		return time.Time{}, err
	}
	wantTopic := strings.Repeat("0", 24) + hex.EncodeToString(recipient[1:])
	var total big.Int
	for _, entry := range info.Log {
		if !strings.EqualFold(entry.Address, hex.EncodeToString(usdt[1:])) || len(entry.Topics) != 3 ||
			!strings.EqualFold(entry.Topics[0], transferEventTopic) || !strings.EqualFold(entry.Topics[2], wantTopic) {
			continue
		}
		value, ok := new(big.Int).SetString(entry.Data, 16)
		if !ok {
			return time.Time{}, errors.New("the transfer amount is unreadable")
		}
		total.Add(&total, value)
	}
	if !total.IsInt64() || total.Int64() != amountMicro {
		return time.Time{}, fmt.Errorf("the node shows %s to the address, not %d", total.String(), amountMicro)
	}
	return time.UnixMilli(info.BlockTimeStamp).UTC(), nil
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return fmt.Errorf("%s %s: status %d: %s", req.Method, req.URL.Path, resp.StatusCode, detail)
	}
	return json.Unmarshal(payload, out)
}
