package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
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
	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/store"
)

type provisionerTestKey struct {
	private *ecdsa.PrivateKey
	public  string
}

func newProvisionerTestKey(t *testing.T) provisionerTestKey {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := nodeauth.PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return provisionerTestKey{private: privateKey, public: publicKey}
}

func signedTestRequest(t *testing.T, key provisionerTestKey, signerID, method, target string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if err := nodeauth.ApplySignedHeaders(req, key.private, signerID, body, "", "", strconv.FormatInt(time.Now().Unix(), 10), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestProvisionerEndpointsRequireTheProvisionerKey(t *testing.T) {
	provisioner := newProvisionerTestKey(t)
	handler := New(config.ServerConfig{AppEnv: "production"}, nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, signedTestRequest(t, provisioner, provisionerRequestID, http.MethodGet, "/api/v1/internal/provisioner/vms", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("disabled provisioner status = %d, want 404", resp.Code)
	}

	handler = New(config.ServerConfig{AppEnv: "production", ProvisionerPublicKey: provisioner.public, ProvisionerMaxVMs: 1}, nil)
	for name, req := range map[string]*http.Request{
		"unsigned":  httptest.NewRequest(http.MethodGet, "/api/v1/internal/provisioner/vms", nil),
		"wrong key": signedTestRequest(t, newProvisionerTestKey(t), provisionerRequestID, http.MethodGet, "/api/v1/internal/provisioner/vms", nil),
		"node id":   signedTestRequest(t, provisioner, "some-node", http.MethodGet, "/api/v1/internal/provisioner/vms", nil),
	} {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("%s request status = %d, want 401", name, resp.Code)
		}
	}
}

func TestProvisionedVMFlowPostgresIntegration(t *testing.T) {
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
	live, err := st.ProvisionerWork(ctx)
	if err != nil {
		t.Fatal(err)
	}

	region := "ci-api-" + uuid.NewString()[:8]
	provisioner := newProvisionerTestKey(t)
	cfg := config.ServerConfig{
		AppEnv:                  "production",
		ProvisionerPublicKey:    provisioner.public,
		ProvisionedRegions:      []string{region},
		ProvisionerMaxVMs:       len(live) + 1,
		ProvisionerSetupTimeout: 30 * time.Minute,
		ProvisionerRetireAfter:  time.Hour,
	}
	api := &API{cfg: cfg, store: st}
	handler := New(cfg, st)

	accountID := uuid.NewString()
	if _, err := st.BootstrapAccountWithIdentity(ctx, accountID, uuid.NewString(), "CI device", newProvisionerTestKey(t).public, store.CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer func() { _ = st.DeleteAccount(context.Background(), accountID) }()

	// Shared region: the usual no-node error.
	resp := httptest.NewRecorder()
	api.writeNoReadyHost(resp, httptest.NewRequest(http.MethodPost, "/", nil), accountID, store.ErrNoReadyHost)
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), store.NoReadyHostCode) {
		t.Fatalf("shared-region response = %d %s", resp.Code, resp.Body.String())
	}

	// Provisioned region: a VM is requested and the app is asked to wait.
	if err := st.SetAccountRuntimeRegion(ctx, accountID, region); err != nil {
		t.Fatal(err)
	}
	resp = httptest.NewRecorder()
	api.writeNoReadyHost(resp, httptest.NewRequest(http.MethodPost, "/", nil), accountID, store.ErrNoReadyHost)
	if resp.Code != http.StatusServiceUnavailable || resp.Header().Get("Retry-After") == "" ||
		!strings.Contains(resp.Body.String(), store.RuntimeHostProvisioningCode) {
		t.Fatalf("provisioned-region response = %d %s", resp.Code, resp.Body.String())
	}

	// The provisioner sees the request.
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, signedTestRequest(t, provisioner, provisionerRequestID, http.MethodGet, "/api/v1/internal/provisioner/vms", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("work status = %d body=%s", resp.Code, resp.Body.String())
	}
	var work struct {
		VMs []store.ProvisionedVM `json:"vms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&work); err != nil {
		t.Fatal(err)
	}
	var vm store.ProvisionedVM
	for _, candidate := range work.VMs {
		if candidate.AccountID != nil && *candidate.AccountID == accountID {
			vm = candidate
		}
	}
	if vm.ID == "" || vm.State != store.ProvisionedVMRequested || vm.Region != region {
		t.Fatalf("work did not list the account's VM: %+v", work.VMs)
	}
	defer func() {
		_, _ = st.ApplyProvisionerReport(context.Background(), vm.ID, store.ProvisionerReport{State: store.ProvisionedVMDeleted})
	}()

	report := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, signedTestRequest(t, provisioner, provisionerRequestID, http.MethodPost,
			"/api/v1/internal/provisioner/vms/"+vm.ID+"/report", []byte(body)))
		return resp
	}
	if resp := report(`{"state":"booting"}`); resp.Code != http.StatusConflict {
		t.Fatalf("out-of-order report status = %d body=%s", resp.Code, resp.Body.String())
	}
	if resp := report(`{"state":"creating","item_hash":"` + strings.Repeat("cd", 32) + `"}`); resp.Code != http.StatusOK {
		t.Fatalf("creating report status = %d body=%s", resp.Code, resp.Body.String())
	}
	nodeKey := newProvisionerTestKey(t)
	if resp := report(`{"state":"unlocking","public_ip":"2001:db8:3100:1781::77","node_public_key":"` + nodeKey.public + `"}`); resp.Code != http.StatusOK {
		t.Fatalf("unlocking report status = %d body=%s", resp.Code, resp.Body.String())
	}
	if resp := report(`{"state":"booting"}`); resp.Code != http.StatusOK {
		t.Fatalf("booting report status = %d body=%s", resp.Code, resp.Body.String())
	}

	heartbeat := func(publicAddr, claimedRegion string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"id":              vm.ID,
			"name":            "CI VM",
			"advertise_addr":  strings.Trim(strings.Split(publicAddr, "]")[0], "["),
			"relay_port":      8090,
			"docker_socket":   true,
			"binder":          true,
			"public_addr":     publicAddr,
			"tls_spki_sha256": testAttestationPin,
			"region":          claimedRegion,
			"snp_attestation": testAttestationBundle,
		})
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, signedTestRequest(t, nodeKey, vm.ID, http.MethodPost, "/api/v1/internal/hosts/heartbeat", body))
		return resp
	}
	if resp := heartbeat("[2001:db8:3100:1781::99]:8443", region); resp.Code != http.StatusForbidden {
		t.Fatalf("heartbeat from another address status = %d body=%s", resp.Code, resp.Body.String())
	}
	resp = heartbeat("[2001:db8:3100:1781::77]:8443", "somewhere-else")
	if resp.Code != http.StatusAccepted {
		t.Fatalf("heartbeat status = %d body=%s", resp.Code, resp.Body.String())
	}
	var host store.Host
	if err := json.NewDecoder(resp.Body).Decode(&host); err != nil {
		t.Fatal(err)
	}
	if host.Region != region {
		t.Fatalf("host region = %q, want the VM's region %q", host.Region, region)
	}
	if got, err := st.ProvisionedVM(ctx, vm.ID); err != nil || got.State != store.ProvisionedVMReady {
		t.Fatalf("VM after heartbeat = %+v err=%v, want ready", got, err)
	}

	// Sessions on the VM carry what the phone needs to verify it.
	fields, err := api.viewerConfidentiality(ctx, vm.ID, preparedViewer{PublicKey: "viewer", Signature: "c2ln"})
	if err != nil {
		t.Fatal(err)
	}
	attestation, _ := fields["node_attestation"].(json.RawMessage)
	if !strings.Contains(string(attestation), `"product":"Genoa"`) ||
		fields["viewer_key_signature"] != "c2ln" || fields["confidential_runtime"] != true {
		t.Fatalf("session confidentiality fields = %v", fields)
	}
}

// testAttestationBundle's tls_spki hashes to testAttestationPin.
var testAttestationBundle = map[string]any{
	"version":  1,
	"product":  "Genoa",
	"report":   "cmVwb3J0",
	"tls_spki": "c3BraQ==",
	"vcek":     "dmNlaw==",
	"ask":      "YXNr",
	"ark":      "YXJr",
}

var testAttestationPin = func() string {
	digest := sha256.Sum256([]byte("spki"))
	return base64.StdEncoding.EncodeToString(digest[:])
}()

func TestNodeAttestationJSONChecksTheBundleShape(t *testing.T) {
	valid, _ := json.Marshal(testAttestationBundle)
	if got, err := nodeAttestationJSON(valid, testAttestationPin); err != nil || !strings.Contains(got, `"version":1`) {
		t.Fatalf("valid bundle = %q err=%v", got, err)
	}
	if got, err := nodeAttestationJSON(nil, ""); err != nil || got != "" {
		t.Fatalf("absent bundle = %q err=%v", got, err)
	}
	for name, raw := range map[string]string{
		"no TLS endpoint": string(valid),
		"unknown field":   `{"version":1,"product":"Genoa","report":"YQ==","tls_spki":"c3BraQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ==","extra":1}`,
		"bad version":     `{"version":2,"product":"Genoa","report":"YQ==","tls_spki":"c3BraQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}`,
		"bad product":     `{"version":1,"product":"Naples","report":"YQ==","tls_spki":"c3BraQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}`,
		"not base64":      `{"version":1,"product":"Genoa","report":"!!","tls_spki":"c3BraQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}`,
		"other TLS key":   `{"version":1,"product":"Genoa","report":"YQ==","tls_spki":"b3RoZXI=","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}`,
		"too large":       `{"version":1,"product":"Genoa","report":"` + strings.Repeat("A", maxNodeAttestationBytes) + `","tls_spki":"c3BraQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}`,
	} {
		pin := testAttestationPin
		if name == "no TLS endpoint" {
			pin = ""
		}
		if _, err := nodeAttestationJSON(json.RawMessage(raw), pin); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
