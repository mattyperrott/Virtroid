package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"virtroid/backend/internal/tron"
)

// accountBalance is what an address holds: TRX in sun and USDT in millionths.
type accountBalance struct {
	TRXSun    int64
	USDTMicro int64
}

// account reads an address's TRX and USDT balances. An address that never
// received TRX is not activated and holds nothing.
func (g tronGrid) account(ctx context.Context, address string) (accountBalance, error) {
	var response struct {
		Success bool `json:"success"`
		Data    []struct {
			Balance int64               `json:"balance"`
			TRC20   []map[string]string `json:"trc20"`
		} `json:"data"`
	}
	if err := g.getJSON(ctx, strings.TrimRight(g.baseURL, "/")+"/v1/accounts/"+address, &response); err != nil {
		return accountBalance{}, err
	}
	if !response.Success {
		return accountBalance{}, errors.New("TronGrid did not answer for the account")
	}
	var balance accountBalance
	if len(response.Data) == 0 {
		return balance, nil
	}
	balance.TRXSun = response.Data[0].Balance
	for _, token := range response.Data[0].TRC20 {
		if raw, ok := token[tron.USDTContract]; ok {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return accountBalance{}, fmt.Errorf("unreadable USDT balance %q", raw)
			}
			balance.USDTMicro = value
		}
	}
	return balance, nil
}

// nowBlock is the newest block, which new transactions name.
func (g tronGrid) nowBlock(ctx context.Context) (tron.BlockRef, error) {
	var block struct {
		BlockID     string `json:"blockID"`
		BlockHeader struct {
			RawData struct {
				Number int64 `json:"number"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := g.postJSON(ctx, "/wallet/getnowblock", map[string]any{}, &block); err != nil {
		return tron.BlockRef{}, err
	}
	id, err := hex.DecodeString(block.BlockID)
	if err != nil || len(id) != 32 || block.BlockHeader.RawData.Number <= 0 {
		return tron.BlockRef{}, errors.New("TronGrid gave an unreadable newest block")
	}
	return tron.BlockRef{Number: block.BlockHeader.RawData.Number, ID: id}, nil
}

// broadcast sends a signed transaction and returns its ID.
func (g tronGrid) broadcast(ctx context.Context, rawData, signature []byte) (string, error) {
	var response struct {
		Result  bool   `json:"result"`
		TxID    string `json:"txid"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	signed := tron.SignedTransaction(rawData, signature)
	if err := g.postJSON(ctx, "/wallet/broadcasthex", map[string]string{"transaction": hex.EncodeToString(signed)}, &response); err != nil {
		return "", err
	}
	id := tron.TxID(rawData)
	want := hex.EncodeToString(id[:])
	if !response.Result {
		message := response.Message
		if decoded, err := hex.DecodeString(message); err == nil {
			message = string(decoded)
		}
		return "", fmt.Errorf("broadcast refused: %s %s", response.Code, message)
	}
	if response.TxID != "" && !strings.EqualFold(response.TxID, want) {
		return "", fmt.Errorf("broadcast answered for transaction %s, not %s", response.TxID, want)
	}
	return want, nil
}

// txOutcome reports whether a transaction is solidified and, if so, whether
// it succeeded.
func (g tronGrid) txOutcome(ctx context.Context, txID string) (done, succeeded bool, err error) {
	var info struct {
		ID      string `json:"id"`
		Result  string `json:"result"`
		Receipt struct {
			Result string `json:"result"`
		} `json:"receipt"`
	}
	if err := g.postJSON(ctx, "/walletsolidity/gettransactioninfobyid", map[string]string{"value": txID}, &info); err != nil {
		return false, false, err
	}
	if !strings.EqualFold(info.ID, txID) {
		return false, false, nil
	}
	ok := info.Result != "FAILED" && (info.Receipt.Result == "" || info.Receipt.Result == "SUCCESS")
	return true, ok, nil
}

func (g tronGrid) postJSON(ctx context.Context, route string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.baseURL, "/")+route, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", g.apiKey)
	}
	return doJSON(g.client, req, out)
}
