package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/callbackauth"
	"virtroid/backend/internal/config"
	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/store"
)

// countedBody is a request body that records how much of it a handler took.
type countedBody struct {
	remaining int64
	taken     int64
}

func (b *countedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.remaining)
	clear(p[:n])
	b.remaining -= n
	b.taken += n
	return int(n), nil
}

func (b *countedBody) attach(req *http.Request) {
	req.Body = io.NopCloser(b)
	req.ContentLength = b.remaining
}

func forgedCapabilityRequest(target string, body *countedBody) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	body.attach(req)
	req.Header.Set("X-Virtroid-Capability-ID", "not-a-real-capability")
	req.Header.Set("X-Virtroid-Capability-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Virtroid-Capability-Nonce", "made-up")
	req.Header.Set("X-Virtroid-Capability-Body-SHA256", "made-up")
	req.Header.Set("X-Virtroid-Capability-Signature", "made-up")
	return req
}

// Anyone can send these headers, so nothing of the body may be taken for them:
// 32 MiB per request would let a handful of uploads exhaust this server.
func TestForgedCapabilityRequestLeavesBodyUnread(t *testing.T) {
	body := &countedBody{remaining: maxRuntimeFileImportBytes}
	req := forgedCapabilityRequest("/api/v1/me/runtimes/x/files?sealed=1", body)
	func() {
		// Without a store the request stops at looking up the capability's key,
		// which is the first thing that could prove who is asking.
		defer func() { _ = recover() }()
		(&API{}).requireRuntimeCapabilityRequest(httptest.NewRecorder(), req, "x")
	}()
	if body.taken != 0 {
		t.Fatalf("took %d bytes before looking up the capability's key", body.taken)
	}
}

func TestSignedRequestLeavesBodyUnreadUntilSignatureChecks(t *testing.T) {
	device := newTestDevice(t)
	other := newTestDevice(t)
	cases := []struct {
		name   string
		lookup func(context.Context, string, string) (string, error)
		want   int
	}{
		{
			name: "unknown device",
			lookup: func(context.Context, string, string) (string, error) {
				return "", store.ErrDeviceNotFound
			},
			want: http.StatusNotFound,
		},
		{
			name: "another device's key",
			lookup: func(context.Context, string, string) (string, error) {
				return other.publicKey(t), nil
			},
			want: http.StatusUnauthorized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := device.request(t, http.MethodPost, "/api/v1/me/storage", []byte("signed"))
			body := &countedBody{remaining: maxAPIRequestBodyBytes}
			body.attach(req)
			resp := httptest.NewRecorder()
			if _, _, ok := (&API{}).requireSignedRequest(resp, req, tc.lookup, deviceSignatureContext); ok {
				t.Fatal("the request was accepted")
			}
			if resp.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.Code, tc.want, resp.Body.String())
			}
			if body.taken != 0 {
				t.Fatalf("took %d bytes before the signature checked out", body.taken)
			}
		})
	}
}

func TestSignedRequestStillComparesItsBody(t *testing.T) {
	device := newTestDevice(t)
	req := device.request(t, http.MethodPost, "/api/v1/me/storage", []byte("signed"))
	req.Body = io.NopCloser(strings.NewReader("tampered"))
	lookup := func(context.Context, string, string) (string, error) { return device.publicKey(t), nil }
	resp := httptest.NewRecorder()
	if _, _, ok := (&API{}).requireSignedRequest(resp, req, lookup, deviceSignatureContext); ok {
		t.Fatal("a tampered body was accepted")
	}
	if resp.Code != http.StatusUnauthorized || !strings.Contains(resp.Body.String(), "body hash mismatch") {
		t.Fatalf("response = %d %s", resp.Code, resp.Body.String())
	}
}

func TestServiceRequestLeavesBodyUnreadUntilSignatureChecks(t *testing.T) {
	key := newProvisionerTestKey(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/provisioner/vms/x/report", nil)
	body := &countedBody{remaining: maxAPIRequestBodyBytes}
	body.attach(req)
	req.Header.Set(nodeauth.HeaderNodeID, provisionerRequestID)
	req.Header.Set(nodeauth.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set(nodeauth.HeaderNonce, "made-up")
	req.Header.Set(nodeauth.HeaderBodySHA256, "made-up")
	req.Header.Set(nodeauth.HeaderSignature, "bWFkZS11cA")
	resp := httptest.NewRecorder()
	if (&API{}).requireServiceRequest(resp, req, provisionerRequestID, key.public) {
		t.Fatal("a forged service request was accepted")
	}
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
	}
	if body.taken != 0 {
		t.Fatalf("took %d bytes before the signature checked out", body.taken)
	}
}

func TestRecoveryBodiesAreBoundedBeforeAnyProof(t *testing.T) {
	const limit = defaultBootstrapMaxBodyBytes
	handler := New(config.ServerConfig{
		AppEnv:                      "production",
		BootstrapRateLimitPerMinute: 5,
		BootstrapMaxBodyBytes:       limit,
	}, nil)
	for _, target := range []string{"/api/v1/recovery/challenge", "/api/v1/recovery/complete"} {
		body := &countedBody{remaining: maxAPIRequestBodyBytes}
		req := httptest.NewRequest(http.MethodPost, target, nil)
		body.attach(req)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d: %s", target, resp.Code, resp.Body.String())
		}
		if body.taken > limit+1 {
			t.Fatalf("%s took %d bytes, past its limit of %d", target, body.taken, limit)
		}
	}
}

func TestByteBudgetBoundsTheTotalAndEachAccount(t *testing.T) {
	budget := newByteBudget(100, 60)
	releaseA, ok := budget.reserve("a", 60)
	if !ok {
		t.Fatal("the first reservation was refused")
	}
	if _, ok := budget.reserve("a", 1); ok {
		t.Fatal("an account went past its share")
	}
	releaseB, ok := budget.reserve("b", 40)
	if !ok {
		t.Fatal("another account's share was refused")
	}
	if _, ok := budget.reserve("c", 1); ok {
		t.Fatal("the total was exceeded")
	}
	releaseA()
	releaseA() // Only the first release counts.
	if got := budget.used; got != 40 {
		t.Fatalf("used = %d after a double release, want 40", got)
	}
	if _, ok := budget.reserve("a", 60); !ok {
		t.Fatal("released bytes were not available again")
	}
	releaseB()

	var unlimited *byteBudget
	release, ok := unlimited.reserve("a", 1<<40)
	if !ok {
		t.Fatal("a nil budget refused a reservation")
	}
	release()
}

func TestSignedBodyOverBudgetIsRefusedUnread(t *testing.T) {
	api := &API{signedBodies: newByteBudget(1<<20, 1<<20)}
	body := &countedBody{remaining: 2 << 20}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/storage", nil)
	body.attach(req)
	resp := httptest.NewRecorder()
	if api.readSignedBody(resp, req, "account", callbackauth.BodyHash(nil), "device signature") {
		t.Fatal("a body over budget was read")
	}
	if resp.Code != http.StatusTooManyRequests || resp.Header().Get("Retry-After") == "" {
		t.Fatalf("response = %d, Retry-After %q", resp.Code, resp.Header().Get("Retry-After"))
	}
	if body.taken != 0 {
		t.Fatalf("took %d bytes over budget", body.taken)
	}
}

func TestSmallSignedBodiesGetThroughAnExhaustedBudget(t *testing.T) {
	api := &API{signedBodies: newByteBudget(0, 0)}
	content := []byte(`{"small":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/storage", strings.NewReader(string(content)))
	resp := httptest.NewRecorder()
	if !api.readSignedBody(resp, req, "account", callbackauth.BodyHash(content), "device signature") {
		t.Fatalf("a small body was refused: %d %s", resp.Code, resp.Body.String())
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil || string(restored) != string(content) {
		t.Fatalf("restored body = %q, err=%v", restored, err)
	}
}

func TestUndeclaredSignedBodyIsChargedTheRouteLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/storage", nil)
	req.ContentLength = -1
	if got := signedBodyCharge(req); got != maxAPIRequestBodyBytes {
		t.Fatalf("charge = %d, want %d", got, maxAPIRequestBodyBytes)
	}
}

func TestSignedBodyChargeEndsWithTheRequest(t *testing.T) {
	api := &API{signedBodies: newByteBudget(1<<20, 1<<20)}
	content := make([]byte, 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/storage", strings.NewReader(string(content))).WithContext(ctx)
	if !api.readSignedBody(httptest.NewRecorder(), req, "account", callbackauth.BodyHash(content), "device signature") {
		t.Fatal("the body was refused")
	}
	if _, ok := api.signedBodies.reserve("other", 1); ok {
		t.Fatal("the body wasn't charged while its request ran")
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if release, ok := api.signedBodies.reserve("other", 1<<20); ok {
			release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(errors.New("the charge outlived its request"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
