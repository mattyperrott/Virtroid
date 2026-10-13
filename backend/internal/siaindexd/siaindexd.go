// Package siaindexd gives each Virtroid account its own app key on the
// operator's Sia indexer (indexd). Nodes use the key to store the account's
// encrypted runtime snapshots on Sia; the indexer only tracks sealed object
// metadata and pays the hosts.
//
// Every key is derived from one secret and the account ID, so nothing has to
// be stored: an account's keys are registered through the indexer's admin API
// the first time they are needed, and registering again is harmless.
package siaindexd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// StoreKind names snapshots kept through the indexer, as nodes report them.
const StoreKind = "sia-indexd"

const (
	appKeyContext     = "virtroid-sia-app-key-v1"
	connectKeyContext = "virtroid-sia-connect-key-v1"
	maxResponseBytes  = 64 << 10
)

// appID identifies Virtroid's accounts in the indexer's app listing.
const appID = "5669727472696f6420616e64726f69642072756e74696d6520736e617073686f"

var quotaNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Config is the indexer the control plane registers accounts with.
type Config struct {
	// AppURL is the indexer's application API as nodes reach it.
	AppURL string
	// AdminURL is the admin API base, ending in /api.
	AdminURL      string
	AdminPassword string
	// KeySecret derives every account's app and connect keys.
	KeySecret []byte
	// Quota names the indexer quota every account's connect key uses.
	Quota           string
	MaxPinnedBytes  uint64
	FundTargetBytes uint64
	DataShards      int
	TotalShards     int
}

// Credentials let a node act for one account on the indexer.
type Credentials struct {
	IndexerURL  string `json:"indexer_url"`
	AppKeySeed  []byte `json:"app_key_seed"`
	DataShards  int    `json:"data_shards"`
	TotalShards int    `json:"total_shards"`
}

// Registry registers accounts with the indexer and hands out their
// credentials.
type Registry struct {
	cfg        Config
	client     *http.Client
	mu         sync.Mutex
	quotaSet   bool
	registered map[string]bool
}

// New checks the configuration. The indexer is first contacted when an
// account needs it.
func New(cfg Config, client *http.Client) (*Registry, error) {
	cfg.AppURL = strings.TrimRight(strings.TrimSpace(cfg.AppURL), "/")
	cfg.AdminURL = strings.TrimRight(strings.TrimSpace(cfg.AdminURL), "/")
	for name, raw := range map[string]string{"app": cfg.AppURL, "admin": cfg.AdminURL} {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return nil, fmt.Errorf("sia indexer %s url is invalid", name)
		}
	}
	if cfg.AdminPassword == "" {
		return nil, errors.New("sia indexer admin password is required")
	}
	if len(cfg.KeySecret) < 32 {
		return nil, errors.New("sia key secret must be at least 32 bytes")
	}
	if !quotaNamePattern.MatchString(cfg.Quota) {
		return nil, errors.New("sia quota name must be 1-32 letters, digits, hyphens or underscores")
	}
	if cfg.MaxPinnedBytes == 0 || cfg.FundTargetBytes == 0 {
		return nil, errors.New("sia quota limits must be positive")
	}
	if cfg.DataShards < 1 || cfg.TotalShards <= cfg.DataShards || cfg.TotalShards > 255 {
		return nil, fmt.Errorf("sia redundancy %d-of-%d is invalid", cfg.DataShards, cfg.TotalShards)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Registry{cfg: cfg, client: client, registered: make(map[string]bool)}, nil
}

// Credentials registers the account with the indexer if this process has not
// yet, and returns what a node needs to store its snapshots.
func (r *Registry) Credentials(ctx context.Context, accountID string) (Credentials, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Credentials{}, errors.New("account id is required")
	}
	seed := r.derive(appKeyContext, accountID)
	r.mu.Lock()
	registered := r.registered[accountID]
	r.mu.Unlock()
	if !registered {
		if err := r.register(ctx, accountID, seed); err != nil {
			return Credentials{}, err
		}
		r.mu.Lock()
		r.registered[accountID] = true
		r.mu.Unlock()
	}
	return Credentials{
		IndexerURL:  r.cfg.AppURL,
		AppKeySeed:  seed,
		DataShards:  r.cfg.DataShards,
		TotalShards: r.cfg.TotalShards,
	}, nil
}

// AccountKey is the account's key on the indexer, as the admin API names it.
func (r *Registry) AccountKey(accountID string) string {
	public := ed25519.NewKeyFromSeed(r.derive(appKeyContext, strings.TrimSpace(accountID))).Public().(ed25519.PublicKey)
	return "ed25519:" + hex.EncodeToString(public)
}

// DeleteAccount removes the account and its objects from the indexer. A
// missing account counts as deleted. The indexer keeps a deleted account's
// connect key, which only this registry's secret can produce.
func (r *Registry) DeleteAccount(ctx context.Context, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return errors.New("account id is required")
	}
	if err := r.admin(ctx, http.MethodDelete, "/account/"+url.PathEscape(r.AccountKey(accountID)), nil, http.StatusNotFound); err != nil {
		return fmt.Errorf("delete sia account: %w", err)
	}
	r.mu.Lock()
	delete(r.registered, accountID)
	r.mu.Unlock()
	return nil
}

func (r *Registry) register(ctx context.Context, accountID string, seed []byte) error {
	if err := r.ensureQuota(ctx); err != nil {
		return err
	}
	connectKey := hex.EncodeToString(r.derive(connectKeyContext, accountID))
	// One use: once the account's key is registered, the connect key cannot
	// register another, even if it leaks.
	if err := r.admin(ctx, http.MethodPost, "/apps/connect/keys", map[string]any{
		"key":         connectKey,
		"description": "Virtroid account " + accountID,
		"quota":       r.cfg.Quota,
	}, http.StatusConflict); err != nil {
		return fmt.Errorf("add sia connect key: %w", err)
	}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if err := r.admin(ctx, http.MethodPost, "/apps/register", map[string]any{
		"connectKey": connectKey,
		"appKey":     "ed25519:" + hex.EncodeToString(public),
		"meta": map[string]any{
			"id":          appID,
			"name":        "Virtroid",
			"description": "Encrypted Android runtime snapshots",
			"logoURL":     "",
			"serviceURL":  "",
		},
	}); err != nil {
		return fmt.Errorf("register sia app key: %w", err)
	}
	return nil
}

func (r *Registry) ensureQuota(ctx context.Context) error {
	r.mu.Lock()
	set := r.quotaSet
	r.mu.Unlock()
	if set {
		return nil
	}
	if err := r.admin(ctx, http.MethodPut, "/quotas/"+url.PathEscape(r.cfg.Quota), map[string]any{
		"description":     "Virtroid accounts",
		"maxPinnedData":   r.cfg.MaxPinnedBytes,
		"totalUses":       1,
		"fundTargetBytes": r.cfg.FundTargetBytes,
	}); err != nil {
		return fmt.Errorf("set sia quota: %w", err)
	}
	r.mu.Lock()
	r.quotaSet = true
	r.mu.Unlock()
	return nil
}

// admin calls the indexer's admin API. Statuses in tolerated count as success.
func (r *Registry) admin(ctx context.Context, method, route string, body any, tolerated ...int) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.cfg.AdminURL+route, payload)
	if err != nil {
		return err
	}
	req.SetBasicAuth("", r.cfg.AdminPassword)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	message, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 300 {
		return nil
	}
	for _, status := range tolerated {
		if resp.StatusCode == status {
			return nil
		}
	}
	return fmt.Errorf("%s %s: status %d: %s", method, route, resp.StatusCode, strings.TrimSpace(string(message)))
}

func (r *Registry) derive(label, accountID string) []byte {
	mac := hmac.New(sha256.New, r.cfg.KeySecret)
	_, _ = mac.Write([]byte(label))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(accountID))
	return mac.Sum(nil)
}
