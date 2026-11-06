// Package alephapi reads Aleph Cloud's public API: what an address holds in
// credits, what it spends, and what became of a message it submitted.
package alephapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes bounds an answer from a service outside our control.
const maxResponseBytes = 4 << 20

// ErrNotFound is Aleph having no such address history or message (yet).
var ErrNotFound = errors.New("not found on Aleph")

// Client reads one Aleph API server.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// CreditBalance is the credits the address holds.
func (c Client) CreditBalance(ctx context.Context, address string) (int64, error) {
	var response struct {
		CreditBalance int64 `json:"credit_balance"`
	}
	if err := c.get(ctx, "/api/v0/addresses/"+url.PathEscape(address)+"/balance", &response); err != nil {
		return 0, err
	}
	return response.CreditBalance, nil
}

// DailySpend is the credits the address spent per day, on average, over the
// last days days.
func (c Client) DailySpend(ctx context.Context, address string, days int, now time.Time) (int64, error) {
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
		if err := c.get(ctx, route, &response); errors.Is(err, ErrNotFound) {
			break // past the last page, or no history at all
		} else if err != nil {
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

const (
	// StatusRejected is a message Aleph refused, which will never take effect.
	StatusRejected = "rejected"
	// errorCreditInsufficient is pyaleph's ErrorCode.CREDIT_INSUFFICIENT.
	errorCreditInsufficient = 6
)

// Message is what became of a submitted message.
type Message struct {
	Status    string          `json:"status"`
	ErrorCode *int            `json:"error_code"`
	Details   json.RawMessage `json:"details"`
}

// Message reads a message's status. Aleph answers ErrNotFound until it has
// seen the message.
func (c Client) Message(ctx context.Context, itemHash string) (Message, error) {
	var message Message
	err := c.get(ctx, "/api/v0/messages/"+url.PathEscape(itemHash), &message)
	return message, err
}

// Describe says in a sentence why Aleph rejected the message.
func (m Message) Describe() string {
	if m.ErrorCode == nil {
		return "Aleph rejected the VM's message"
	}
	if *m.ErrorCode != errorCreditInsufficient {
		return "Aleph rejected the VM's message (error " + strconv.Itoa(*m.ErrorCode) + ")"
	}
	// The details' shape depends on the error, so only this code reads them.
	var details struct {
		Errors []struct {
			AccountCredits  string `json:"account_credits"`
			RequiredCredits string `json:"required_credits"`
		} `json:"errors"`
	}
	if json.Unmarshal(m.Details, &details) == nil && len(details.Errors) > 0 {
		have, haveOK := wholeCredits(details.Errors[0].AccountCredits)
		need, needOK := wholeCredits(details.Errors[0].RequiredCredits)
		if haveOK && needOK {
			return "Aleph refused the VM for lack of credits: the paying account holds " +
				GroupDigits(have) + " and needs " + GroupDigits(need)
		}
	}
	return "Aleph refused the VM for lack of credits"
}

// wholeCredits reads a decimal amount, rounded up to a whole credit.
func wholeCredits(raw string) (int64, bool) {
	amount, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok {
		return 0, false
	}
	whole := new(big.Int).Div(new(big.Int).Add(amount.Num(), new(big.Int).Sub(amount.Denom(), big.NewInt(1))), amount.Denom())
	if !whole.IsInt64() {
		return 0, false
	}
	return whole.Int64(), true
}

// GroupDigits writes a number with thousands separators.
func GroupDigits(value int64) string {
	digits := strconv.FormatInt(value, 10)
	sign := ""
	if value < 0 {
		sign, digits = "-", digits[1:]
	}
	var grouped strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return sign + grouped.String()
}

func (c Client) get(ctx context.Context, route string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+route, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", req.URL.Path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out)
}
