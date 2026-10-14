package siaindexd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type adminCall struct {
	method string
	path   string
	body   map[string]any
}

type fakeAdmin struct {
	mu          sync.Mutex
	calls       []adminCall
	connectKeys map[string]bool
	accounts    map[string]string
	status      map[string]int
}

func newFakeAdmin(t *testing.T) (*fakeAdmin, *httptest.Server) {
	t.Helper()
	admin := &fakeAdmin{connectKeys: map[string]bool{}, accounts: map[string]string{}, status: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "" || password != "admin-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if payload, _ := io.ReadAll(r.Body); len(payload) > 0 {
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Errorf("decode %s %s: %v", r.Method, r.URL.Path, err)
			}
		}
		admin.mu.Lock()
		defer admin.mu.Unlock()
		admin.calls = append(admin.calls, adminCall{method: r.Method, path: r.URL.Path, body: body})
		if status := admin.status[r.Method+" "+r.URL.Path]; status != 0 {
			w.WriteHeader(status)
			return
		}
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/quotas/"):
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/connect/keys":
			key := body["key"].(string)
			if admin.connectKeys[key] {
				w.WriteHeader(http.StatusConflict)
				return
			}
			admin.connectKeys[key] = true
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/register":
			key := body["connectKey"].(string)
			if !admin.connectKeys[key] {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			admin.accounts[body["appKey"].(string)] = key
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/account/"):
			accountKey := strings.TrimPrefix(r.URL.Path, "/api/account/")
			if _, ok := admin.accounts[accountKey]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(admin.accounts, accountKey)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/apps/connect/keys/"):
			key := strings.TrimPrefix(r.URL.Path, "/api/apps/connect/keys/")
			if !admin.connectKeys[key] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(admin.connectKeys, key)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return admin, server
}

func (a *fakeAdmin) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func testConfig(adminURL string) Config {
	return Config{
		AppURL:          "https://indexer.example:9982/",
		AdminURL:        adminURL + "/api",
		AdminPassword:   "admin-secret",
		KeySecret:       bytes.Repeat([]byte{7}, 32),
		Quota:           "virtroid",
		MaxPinnedBytes:  40 << 30,
		FundTargetBytes: 1 << 30,
		DataShards:      10,
		TotalShards:     30,
	}
}

func TestCredentialsRegisterOnceAndDeriveStableKeys(t *testing.T) {
	admin, server := newFakeAdmin(t)
	registry, err := New(testConfig(server.URL), server.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first, err := registry.Credentials(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if first.IndexerURL != "https://indexer.example:9982" || first.DataShards != 10 || first.TotalShards != 30 || len(first.AppKeySeed) != 32 {
		t.Fatalf("credentials = %+v", first)
	}
	public := ed25519.NewKeyFromSeed(first.AppKeySeed).Public().(ed25519.PublicKey)
	accountKey := "ed25519:" + hex.EncodeToString(public)
	if registry.AccountKey("account-1") != accountKey {
		t.Fatalf("AccountKey = %s, want %s", registry.AccountKey("account-1"), accountKey)
	}
	if admin.accounts[accountKey] == "" {
		t.Fatal("the account's app key was not registered")
	}
	quota := admin.calls[0]
	if quota.method != http.MethodPut || quota.path != "/api/quotas/virtroid" || quota.body["totalUses"].(float64) != 1 {
		t.Fatalf("first call = %+v, want the quota with one use per connect key", quota)
	}

	calls := admin.callCount()
	again, err := registry.Credentials(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("Credentials again: %v", err)
	}
	if !bytes.Equal(again.AppKeySeed, first.AppKeySeed) || admin.callCount() != calls {
		t.Fatal("a registered account was registered again or got another key")
	}

	other, err := registry.Credentials(context.Background(), "account-2")
	if err != nil {
		t.Fatalf("Credentials for another account: %v", err)
	}
	if bytes.Equal(other.AppKeySeed, first.AppKeySeed) {
		t.Fatal("two accounts share an app key")
	}
	if admin.calls[len(admin.calls)-2].method != http.MethodPost || admin.calls[len(admin.calls)-1].path != "/api/apps/register" {
		t.Fatal("the quota was set again for the second account")
	}
}

func TestCredentialsSurviveRestartWithExistingConnectKey(t *testing.T) {
	admin, server := newFakeAdmin(t)
	first, _ := New(testConfig(server.URL), server.Client())
	want, err := first.Credentials(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	// A restarted control plane registers again; the indexer answers 409 for
	// the connect key and accepts the known app key.
	restarted, _ := New(testConfig(server.URL), server.Client())
	got, err := restarted.Credentials(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("Credentials after restart: %v", err)
	}
	if !bytes.Equal(got.AppKeySeed, want.AppKeySeed) || len(admin.accounts) != 1 {
		t.Fatal("restart changed the account's key")
	}
}

func TestCredentialsFailWhenRegistrationFails(t *testing.T) {
	admin, server := newFakeAdmin(t)
	admin.status["POST /api/apps/register"] = http.StatusForbidden
	registry, _ := New(testConfig(server.URL), server.Client())
	if _, err := registry.Credentials(context.Background(), "account-1"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Credentials = %v, want the registration failure", err)
	}
	delete(admin.status, "POST /api/apps/register")
	if _, err := registry.Credentials(context.Background(), "account-1"); err != nil {
		t.Fatalf("Credentials after the indexer recovered: %v", err)
	}
}

func TestDeleteAccountRemovesTheAccount(t *testing.T) {
	admin, server := newFakeAdmin(t)
	registry, _ := New(testConfig(server.URL), server.Client())
	if _, err := registry.Credentials(context.Background(), "account-1"); err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if err := registry.DeleteAccount(context.Background(), "account-1"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if len(admin.accounts) != 0 {
		t.Fatalf("left accounts=%d", len(admin.accounts))
	}
	if err := registry.DeleteAccount(context.Background(), "account-1"); err != nil {
		t.Fatalf("DeleteAccount of a deleted account: %v", err)
	}
	if _, err := registry.Credentials(context.Background(), "account-1"); err != nil || len(admin.accounts) != 1 {
		t.Fatalf("re-registering after delete: err=%v accounts=%d", err, len(admin.accounts))
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"app url":    func(c *Config) { c.AppURL = "indexer.example" },
		"admin url":  func(c *Config) { c.AdminURL = "" },
		"password":   func(c *Config) { c.AdminPassword = "" },
		"secret":     func(c *Config) { c.KeySecret = c.KeySecret[:16] },
		"quota":      func(c *Config) { c.Quota = "has space" },
		"pinned":     func(c *Config) { c.MaxPinnedBytes = 0 },
		"redundancy": func(c *Config) { c.TotalShards = c.DataShards },
	} {
		cfg := testConfig("http://admin.example")
		mutate(&cfg)
		if _, err := New(cfg, nil); err == nil {
			t.Fatalf("%s: bad config accepted", name)
		}
	}
}

func TestWalletReadsAddressAndConfirmedBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, _ := r.BasicAuth(); r.URL.Path != "/api/wallet" || password != "admin-secret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"address":"` + strings.Repeat("a", 76) + `","confirmed":"42000000000000000000000000000","spendable":"1"}`))
	}))
	defer server.Close()
	registry, err := New(testConfig(server.URL), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := registry.Wallet(context.Background())
	if err != nil || wallet.Address != strings.Repeat("a", 76) || wallet.Confirmed != "42000000000000000000000000000" {
		t.Fatalf("wallet = %+v, %v", wallet, err)
	}
}
