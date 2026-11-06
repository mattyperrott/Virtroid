package alephapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDailySpendStopsAtAlephsLastPage(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			http.Error(w, "No credit history found for this address", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"credit_history":[{"amount":-7000000,"payment_method":"credit_expense","message_timestamp":%q},{"amount":5000000,"payment_method":"credit_purchase","message_timestamp":%q}]}`,
			now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-2*time.Hour).Format(time.RFC3339Nano))
	}))
	defer server.Close()
	perDay, err := Client{BaseURL: server.URL, HTTP: server.Client()}.DailySpend(context.Background(), "0x4631b0613fF21cd5715f1b1572b4CD5bc0916c4C", 7, now)
	if err != nil || perDay != 1_000_000 {
		t.Fatalf("DailySpend = %d, %v; want 1,000,000", perDay, err)
	}
}

// The body Aleph really answered for the instance it refused on 2026-11-05.
const rejectedForCredits = `{"status":"rejected","item_hash":"25fc1ab1cde85e0e0258e8ddd7fd234b02292529d633e745c446f631030ae7ab","reception_time":"2026-11-05T16:30:08.032963Z","error_code":6,"details":{"errors":[{"account_credits":"1659418","min_runtime_days":1,"required_credits":"2837098.518514818475161600"}]}}`

func TestMessageDescribesWhyAlephRejectedIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/25fc") {
			fmt.Fprint(w, rejectedForCredits)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTP: server.Client()}

	message, err := client.Message(context.Background(), "25fc")
	if err != nil || message.Status != StatusRejected {
		t.Fatalf("Message = %+v, %v", message, err)
	}
	want := "Aleph refused the VM for lack of credits: the paying account holds 1,659,418 and needs 2,837,099"
	if got := message.Describe(); got != want {
		t.Fatalf("Describe = %q, want %q", got, want)
	}
	if _, err := client.Message(context.Background(), "unseen"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unseen message: %v", err)
	}
}

func TestDescribeCopesWithOtherRejections(t *testing.T) {
	for raw, want := range map[string]string{
		`{"status":"rejected"}`: "Aleph rejected the VM's message",
		`{"status":"rejected","error_code":305,"details":{"errors":["bad"]}}`: "Aleph rejected the VM's message (error 305)",
		// A shape this code doesn't know still names the cause.
		`{"status":"rejected","error_code":6,"details":{"errors":["short"]}}`: "Aleph refused the VM for lack of credits",
		`{"status":"rejected","error_code":6}`:                                "Aleph refused the VM for lack of credits",
	} {
		var message Message
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			t.Fatal(err)
		}
		if got := message.Describe(); got != want {
			t.Fatalf("%s: Describe = %q, want %q", raw, got, want)
		}
	}
}

func TestGroupDigits(t *testing.T) {
	for value, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 118746705: "118,746,705", -1234: "-1,234"} {
		if got := GroupDigits(value); got != want {
			t.Fatalf("GroupDigits(%d) = %q, want %q", value, got, want)
		}
	}
}
