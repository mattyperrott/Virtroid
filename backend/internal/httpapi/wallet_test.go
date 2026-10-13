package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
	"virtroid/backend/internal/tron"
)

type testDevice struct {
	accountID string
	deviceID  string
	key       *ecdsa.PrivateKey
}

func newTestDevice(t *testing.T) testDevice {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testDevice{accountID: uuid.NewString(), deviceID: uuid.NewString(), key: key}
}

func (d testDevice) publicKey(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&d.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// request is signed the way the app signs its requests.
func (d testDevice) request(t *testing.T, method, target string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	bodyDigest := sha256.Sum256(body)
	bodyHash := base64.RawURLEncoding.EncodeToString(bodyDigest[:])
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.NewString()
	digest := sha256.Sum256([]byte(signedRequestCanonical(method, req.URL.RequestURI(), d.accountID, d.deviceID, timestamp, nonce, bodyHash)))
	signature, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Virtroid-Account-ID", d.accountID)
	req.Header.Set("X-Virtroid-Device-ID", d.deviceID)
	req.Header.Set("X-Virtroid-Timestamp", timestamp)
	req.Header.Set("X-Virtroid-Nonce", nonce)
	req.Header.Set("X-Virtroid-Body-SHA256", bodyHash)
	req.Header.Set("X-Virtroid-Signature", base64.RawURLEncoding.EncodeToString(signature))
	return req
}

func TestWalletDepositsPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	wallet, err := tron.OpenWallet("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	if err != nil {
		t.Fatal(err)
	}
	xpub, err := wallet.XPub()
	if err != nil {
		t.Fatal(err)
	}
	payments := newProvisionerTestKey(t)
	cfg := config.ServerConfig{AppEnv: "production", TronDepositXPub: xpub, PaymentsPublicKey: payments.public}
	handler := New(cfg, st)

	device := newTestDevice(t)
	if _, err := st.BootstrapAccountWithIdentity(ctx, device.accountID, device.deviceID, "CI device", device.publicKey(t), store.CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer func() { _ = st.DeleteAccount(context.Background(), device.accountID) }()

	type walletView struct {
		Currency       string                `json:"currency"`
		BalanceMicro   int64                 `json:"balance_micro"`
		DepositAddress string                `json:"deposit_address"`
		Deposits       []store.WalletDeposit `json:"deposits"`
		Entries        []store.LedgerEntry   `json:"entries"`
	}
	openWallet := func() walletView {
		t.Helper()
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, device.request(t, http.MethodGet, "/api/v1/me/wallet", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("wallet status = %d body=%s", resp.Code, resp.Body.String())
		}
		var view walletView
		if err := json.Unmarshal(resp.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	view := openWallet()
	if view.Currency != "USDT" || view.BalanceMicro != 0 || !tron.ValidAddress(view.DepositAddress) || len(view.Deposits) != 0 {
		t.Fatalf("new wallet = %+v", view)
	}
	if again := openWallet(); again.DepositAddress != view.DepositAddress {
		t.Fatalf("the deposit address changed: %s then %s", view.DepositAddress, again.DepositAddress)
	}

	// The payments service watches the address, at the index the seed derives it from.
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, signedTestRequest(t, payments, paymentsRequestID, http.MethodGet, "/api/v1/internal/payments/watch", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("watch status = %d body=%s", resp.Code, resp.Body.String())
	}
	var watch struct {
		Addresses []struct {
			Index   int64  `json:"index"`
			Address string `json:"address"`
		} `json:"addresses"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &watch); err != nil {
		t.Fatal(err)
	}
	var index int64
	for _, entry := range watch.Addresses {
		if entry.Address == view.DepositAddress {
			index = entry.Index
		}
	}
	if index == 0 {
		t.Fatalf("watch list does not have %s", view.DepositAddress)
	}
	if fromSeed, _ := wallet.Address(uint32(index)); fromSeed != view.DepositAddress {
		t.Fatalf("seed derives %s at index %d, the control plane %s", fromSeed, index, view.DepositAddress)
	}

	report := func(key provisionerTestKey, address string, amount int64) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"tx_id":        strings.Repeat("ab", 32),
			"address":      address,
			"index":        index,
			"amount_micro": amount,
			"block_time":   time.Now().UTC(),
		})
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, signedTestRequest(t, key, paymentsRequestID, http.MethodPost, "/api/v1/internal/payments/deposits", body))
		return resp
	}
	if resp := report(newProvisionerTestKey(t), view.DepositAddress, 25_000_000); resp.Code != http.StatusUnauthorized {
		t.Fatalf("deposit signed with another key = %d", resp.Code)
	}
	other, _ := wallet.Address(uint32(index + 1))
	if resp := report(payments, other, 25_000_000); resp.Code != http.StatusBadRequest {
		t.Fatalf("deposit to another index's address = %d %s", resp.Code, resp.Body.String())
	}
	for attempt, wantCredited := range []bool{true, false} {
		resp := report(payments, view.DepositAddress, 25_000_000)
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"credited":`+strconv.FormatBool(wantCredited)) {
			t.Fatalf("report %d = %d %s", attempt, resp.Code, resp.Body.String())
		}
	}
	view = openWallet()
	if view.BalanceMicro != 25_000_000 || len(view.Deposits) != 1 || len(view.Entries) != 1 || view.Entries[0].Kind != "deposit" {
		t.Fatalf("wallet after the deposit = %+v", view)
	}

	off := New(config.ServerConfig{AppEnv: "production"}, st)
	resp = httptest.NewRecorder()
	off.ServeHTTP(resp, device.request(t, http.MethodGet, "/api/v1/me/wallet", nil))
	if resp.Code != http.StatusNotFound || !strings.Contains(resp.Body.String(), "wallet_unavailable") {
		t.Fatalf("wallet with deposits off = %d %s", resp.Code, resp.Body.String())
	}
}
