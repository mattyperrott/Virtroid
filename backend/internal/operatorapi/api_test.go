package operatorapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

type fakeSnapshotStore struct {
	snapshot store.OperatorSnapshot
	payments store.OperatorPayments
	vms      store.OperatorVMs
	err      error
}

func (f fakeSnapshotStore) OperatorSnapshot(context.Context) (store.OperatorSnapshot, error) {
	return f.snapshot, f.err
}

func (f fakeSnapshotStore) OperatorPayments(context.Context) (store.OperatorPayments, error) {
	return f.payments, f.err
}

func (f fakeSnapshotStore) OperatorVMs(context.Context) (store.OperatorVMs, error) {
	return f.vms, f.err
}

func TestOperatorSessionAndSanitizedOverview(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	backend := fakeSnapshotStore{snapshot: store.OperatorSnapshot{
		GeneratedAt:     now,
		ActiveAccounts:  2,
		TotalRuntimes:   1,
		RunningRuntimes: 1,
		LiveSessions:    1,
		Readiness:       store.NodeReadiness{Ready: true, ObservedNodes: 1, ReadyNodes: 1},
		Hosts:           []store.OperatorHost{{ID: "node-1", Name: "primary-node", LastHeartbeatAt: now.Add(-20 * time.Second)}},
		Runtimes: []store.OperatorRuntime{{
			ID: "runtime-12345678", AccountID: "account-12345678", Name: "Primary runtime",
			Status: "running", DesiredState: "running", ConnectionStatus: "online", HostID: "node-1",
			PersonaVersion: 2, AndroidVersion: "android-14", WidthPx: 720, HeightPx: 1600,
			DensityDPI: 320, BlobStoreKind: "local-disk", UpdatedAt: now,
		}},
		Incidents: []store.OperatorIncident{{
			ID: 7, NodeID: "node-1", Source: "falco", Rule: "Unexpected process", Priority: "warning", EventTime: now,
		}},
	}}
	handler := New(config.ServerConfig{
		AppEnv: "development", OperatorConsoleToken: "correct horse battery staple",
		OperatorSessionTTL: time.Hour, OperatorLoginRateLimitPerMinute: 5,
		ReleaseSourceSHA: "1234567890abcdef1234567890abcdef12345678", ReleaseSchemaVersion: "2026090301",
	}, backend)
	server := httptest.NewServer(handler)
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar

	response, err := client.Get(server.URL + "/operator/v1/overview")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.StatusCode)
	}
	response.Body.Close()

	response, err = client.Post(server.URL+"/operator/v1/session", "application/json", strings.NewReader(`{"token":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", response.StatusCode)
	}
	response.Body.Close()

	response, err = client.Post(server.URL+"/operator/v1/session", "application/json", strings.NewReader(`{"token":"correct horse battery staple"}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("login status = %d, want 201", response.StatusCode)
	}
	var session sessionResponse
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !session.Authenticated {
		t.Fatal("session should be authenticated")
	}

	response, err = client.Get(server.URL + "/operator/v1/overview")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("overview status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("missing restrictive CSP: %q", got)
	}
	var overview overviewResponse
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if overview.ReadOnly != true || len(overview.Runtimes) != 1 || overview.Deployment.Commit != "1234567890abcdef1234567890abcdef12345678" {
		t.Fatalf("unexpected overview: %+v", overview)
	}
	if overview.Incidents[0].Detail == "" || strings.Contains(overview.Incidents[0].Detail, "postgres") {
		t.Fatalf("unexpected incident detail: %q", overview.Incidents[0].Detail)
	}
}

func TestProductionSessionCookieSecurity(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv: "production", OperatorConsoleToken: "a sufficiently long operator token",
		OperatorSessionTTL: time.Hour, OperatorLoginRateLimitPerMinute: 5,
	}, fakeSnapshotStore{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/operator/v1/session", strings.NewReader(`{"token":"a sufficiently long operator token"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", recorder.Code)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/operator" {
		t.Fatalf("unsafe session cookie: %+v", cookies)
	}
}

func TestEmptyOverviewUsesJSONArrays(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	overview := buildOverview(config.ServerConfig{AppEnv: "production"}, store.OperatorSnapshot{
		GeneratedAt: now,
		Readiness:   store.NodeReadiness{Ready: true, ObservedNodes: 1, ReadyNodes: 1},
	}, now)

	payload, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"runtimes":[]`, `"incidents":[`, `"activity":[]`} {
		if !strings.Contains(string(payload), field) {
			t.Fatalf("overview JSON missing %s: %s", field, payload)
		}
	}
}

func TestEmbeddedAppIndexDoesNotRedirect(t *testing.T) {
	handler := New(config.ServerConfig{}, fakeSnapshotStore{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/operator/", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "" {
		t.Fatalf("unexpected redirect location %q", location)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("content type = %q, want text/html", got)
	}
}

func TestLoginRateLimit(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv: "development", OperatorConsoleToken: "secret", OperatorLoginRateLimitPerMinute: 2,
	}, fakeSnapshotStore{})
	for attempt := 0; attempt < 3; attempt++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/operator/v1/session", strings.NewReader(`{"token":"wrong"}`))
		request.RemoteAddr = "192.0.2.1:1234"
		handler.ServeHTTP(recorder, request)
		want := http.StatusUnauthorized
		if attempt == 2 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, recorder.Code, want)
		}
	}
}

func TestLoginLimiterForgetsExpiredClients(t *testing.T) {
	limiter := newLoginLimiter(2)
	start := time.Unix(1_700_000_000, 0)
	for i := range 500 {
		limiter.allow(fmt.Sprintf("198.51.%d.%d", i/256, i%256), start)
	}
	// A minute later every entry has expired; the next attempt sweeps them.
	limiter.allow("203.0.113.9", start.Add(2*time.Minute))
	if got := len(limiter.attempt); got != 1 {
		t.Fatalf("limiter kept %d clients, want 1", got)
	}
}

func TestLoginLimiterCountsIPv6PerSlash64(t *testing.T) {
	limiter := newLoginLimiter(2)
	now := time.Unix(1_700_000_000, 0)
	if !limiter.allow("2001:db8:1:2::1", now) || !limiter.allow("2001:db8:1:2::ffff", now) {
		t.Fatal("first two attempts from the /64 refused")
	}
	if limiter.allow("2001:db8:1:2:abcd::9", now) {
		t.Fatal("a third address in the same /64 got a fresh allowance")
	}
	if !limiter.allow("2001:db8:1:3::1", now) {
		t.Fatal("another /64 was refused")
	}
}

func TestLoginLimiterStopsGrowingUnderASpray(t *testing.T) {
	limiter := newLoginLimiter(5)
	now := time.Unix(1_700_000_000, 0)
	for i := range maxLoginLimiterKeys + 500 {
		limiter.allow(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255), now)
	}
	if got := len(limiter.attempt); got > maxLoginLimiterKeys {
		t.Fatalf("limiter grew to %d clients", got)
	}
	if limiter.allow("192.0.2.200", now) {
		t.Fatal("a new client was admitted while the limiter was full")
	}
	if !limiter.allow("192.0.2.200", now.Add(2*time.Minute)) {
		t.Fatal("a new client was refused after the old entries expired")
	}
}
