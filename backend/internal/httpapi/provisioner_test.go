package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
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
	_ "github.com/jackc/pgx/v5/stdlib"

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
		ProvisionerTrialMaxVMs:  len(live) + 1,
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
	// The test makes a second runtime, more than a trial allows.
	raiseRuntimeLimit(t, accountID)

	// Shared region: the usual no-node error.
	resp := httptest.NewRecorder()
	api.writeNoReadyHost(resp, httptest.NewRequest(http.MethodPost, "/", nil), accountID, "", store.ErrNoReadyHost)
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), store.NoReadyHostCode) {
		t.Fatalf("shared-region response = %d %s", resp.Code, resp.Body.String())
	}

	// Provisioned region: a VM is requested and the app is asked to wait.
	if err := st.SetAccountRuntimeRegion(ctx, accountID, region); err != nil {
		t.Fatal(err)
	}
	resp = httptest.NewRecorder()
	api.writeNoReadyHost(resp, httptest.NewRequest(http.MethodPost, "/", nil), accountID, "", store.ErrNoReadyHost)
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
			"audio_streaming": true,
			"file_import":     true,
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

	// The VM serves sessions from the address the provisioner recorded, which
	// is on no static allowlist, and from nowhere else.
	stored, err := st.GetHost(ctx, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if target, _, err := api.nodeCallbackTarget(ctx, stored); err != nil || target != "https://[2001:db8:3100:1781::77]:8443" {
		t.Fatalf("callback target = %q err=%v", target, err)
	}
	if endpoint, pin, err := api.relayEndpointForHost(ctx, stored); err != nil ||
		endpoint.Address != "[2001:db8:3100:1781::77]:8443" || pin != testAttestationPin {
		t.Fatalf("relay endpoint = %+v pin=%q err=%v", endpoint, pin, err)
	}
	stored.PublicAddr = "[2001:db8:3100:1781::99]:8443"
	if _, _, err := api.nodeCallbackTarget(ctx, stored); err == nil {
		t.Fatal("callback target accepted an address the provisioner did not record")
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

	// The VM gets Sia credentials for its account's runtime, and only that.
	runtime, err := st.CreateRuntime(ctx, accountID, store.CreateRuntimeInput{Name: "CI sia", FileMode: "upload-only"})
	if err != nil {
		t.Fatal(err)
	}
	if started, err := st.StartRuntime(ctx, accountID, runtime.ID); err != nil || started.HostID == nil || *started.HostID != vm.ID {
		t.Fatalf("start on the VM = %+v err=%v", started, err)
	}
	credentials := func(handler http.Handler, runtimeID string) *httptest.ResponseRecorder {
		t.Helper()
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, signedTestRequest(t, nodeKey, vm.ID, http.MethodGet, "/api/v1/internal/runtimes/"+runtimeID+"/sia-credentials", nil))
		return resp
	}
	if resp := credentials(handler, runtime.ID); resp.Code != http.StatusNotFound || !strings.Contains(resp.Body.String(), "sia_not_configured") {
		t.Fatalf("credentials without sia = %d %s", resp.Code, resp.Body.String())
	}
	var registered []string
	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registered = append(registered, r.Method+" "+r.URL.Path)
	}))
	defer indexer.Close()
	siaCfg := cfg
	siaCfg.SiaIndexerAppURL = "https://indexer.example:9982"
	siaCfg.SiaIndexerAdminURL = indexer.URL + "/api"
	siaCfg.SiaIndexerAdminPassword = "admin-secret"
	siaCfg.SiaKeySecret = strings.Repeat("k", 32)
	siaCfg.SiaQuota = "virtroid"
	siaCfg.SiaMaxPinnedBytes = 64 << 30
	siaCfg.SiaFundTargetBytes = 1 << 30
	siaCfg.SiaDataShards, siaCfg.SiaTotalShards = 10, 30
	siaHandler := New(siaCfg, st)
	resp = credentials(siaHandler, runtime.ID)
	var issued struct {
		IndexerURL  string `json:"indexer_url"`
		AppKeySeed  []byte `json:"app_key_seed"`
		DataShards  int    `json:"data_shards"`
		TotalShards int    `json:"total_shards"`
	}
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &issued) != nil ||
		issued.IndexerURL != "https://indexer.example:9982" || len(issued.AppKeySeed) != 32 || issued.TotalShards != 30 {
		t.Fatalf("credentials = %d %s", resp.Code, resp.Body.String())
	}
	if len(registered) != 3 || registered[2] != "POST /api/apps/register" {
		t.Fatalf("indexer calls = %v, want the quota, connect key and registration", registered)
	}
	other, err := st.CreateRuntime(ctx, accountID, store.CreateRuntimeInput{Name: "CI sia other", FileMode: "upload-only"})
	if err != nil {
		t.Fatal(err)
	}
	if resp := credentials(siaHandler, other.ID); resp.Code != http.StatusNotFound || strings.Contains(resp.Body.String(), "sia_not_configured") {
		t.Fatalf("credentials for a runtime the VM does not hold = %d %s", resp.Code, resp.Body.String())
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

func TestSNPMeasurementsAreServedAsSigned(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/list.json"
	handler := New(config.ServerConfig{AppEnv: "production", SNPMeasurementsPath: path}, nil)
	get := func() *httptest.ResponseRecorder {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/v1/snp-measurements", nil))
		return resp
	}
	if resp := get(); resp.Code != http.StatusNotFound {
		t.Fatalf("missing list status = %d", resp.Code)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if resp := get(); resp.Code != http.StatusNotFound {
		t.Fatalf("placeholder list status = %d", resp.Code)
	}
	if err := os.WriteFile(path, []byte(`{"payload":"cGF5bG9hZA==","signature":"c2ln"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := get()
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"payload":"cGF5bG9hZA=="`) {
		t.Fatalf("list response = %d %s", resp.Code, resp.Body.String())
	}
}

func raiseRuntimeLimit(t *testing.T, accountID string) {
	t.Helper()
	db, err := sql.Open("pgx", os.Getenv("VIRTROID_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE account_entitlements SET runtime_limit = 10 WHERE account_id = $1`, accountID); err != nil {
		t.Fatalf("raise runtime limit: %v", err)
	}
}

func TestProvisioningRetryAfterShortensOnceTheVMBoots(t *testing.T) {
	cases := []struct {
		state, desired string
		want           time.Duration
	}{
		{"requested", "present", provisioningRetryAfter},
		{"creating", "present", provisioningRetryAfter},
		{"unlocking", "present", provisioningRetryAfter},
		{"booting", "present", provisioningBootingRetryAfter},
		{"ready", "present", provisioningBootingRetryAfter},
		{"booting", "absent", provisioningRetryAfter},
	}
	for _, c := range cases {
		if got := provisioningRetryAfterFor(store.ProvisionedVM{State: c.state, Desired: c.desired}); got != c.want {
			t.Errorf("%s/%s: retry after %v, want %v", c.state, c.desired, got, c.want)
		}
	}
}

func TestTrialVMLimitsPostgresIntegration(t *testing.T) {
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
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	live, err := st.ProvisionerWork(ctx)
	if err != nil {
		t.Fatal(err)
	}

	region := "ci-trial-api-" + uuid.NewString()[:8]
	defer func() { _, _ = db.Exec(`DELETE FROM provisioned_vms WHERE region = $1`, region) }()
	handler := New(config.ServerConfig{
		AppEnv:                           "production",
		ProvisionedRegions:               []string{region},
		ProvisionerMaxVMs:                len(live) + 100,
		ProvisionerTrialMaxVMs:           len(live) + 2,
		ProvisionerTrialVMsPerSourceHour: 1,
		ProvisionerSetupTimeout:          30 * time.Minute,
		ProvisionerRetireAfter:           time.Hour,
		TrustProxyHeaders:                true,
	}, st)

	const verifier = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	type actor struct {
		device  testDevice
		runtime string
	}
	newActor := func(source string, withData bool) actor {
		t.Helper()
		device := newTestDevice(t)
		if _, err := st.BootstrapAccountWithIdentity(ctx, device.accountID, device.deviceID, "CI device", device.publicKey(t), store.CreateRuntimeInput{}); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		t.Cleanup(func() { _ = st.DeleteAccount(context.Background(), device.accountID) })
		if err := st.SetAccountRuntimeRegion(ctx, device.accountID, region); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE devices SET blob_key_verifier = $2 WHERE id = $1`, device.deviceID, verifier); err != nil {
			t.Fatalf("set verifier: %v", err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE account_entitlements SET source = $2 WHERE account_id = $1`, device.accountID, source); err != nil {
			t.Fatalf("set entitlement: %v", err)
		}
		runtime, err := st.CreateRuntime(ctx, device.accountID, store.CreateRuntimeInput{Name: "CI runtime", FileMode: "upload-only"})
		if err != nil {
			t.Fatalf("create runtime: %v", err)
		}
		if withData {
			if _, err := db.ExecContext(ctx,
				`UPDATE runtimes SET blob_store_kind = 'sia-indexd', blob_manifest_json = '{"version":3,"generation":1,"total_bytes":1}' WHERE id = $1`,
				runtime.ID,
			); err != nil {
				t.Fatalf("store a snapshot: %v", err)
			}
		}
		return actor{device: device, runtime: runtime.ID}
	}
	// ask sends what the app does first for each of these: the lease it needs
	// the account's VM for.
	ask := func(who actor, operation, address string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"account_id": who.device.accountID, "device_id": who.device.deviceID,
			"operation": operation, "blob_key_verifier": verifier,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := who.device.request(t, http.MethodPost, "/api/v1/me/runtimes/"+who.runtime+"/blob-key-lease", body)
		req.Header.Set("X-Forwarded-For", address)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		var answer map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &answer); err != nil {
			t.Fatalf("answer %d %q: %v", resp.Code, resp.Body.String(), err)
		}
		return resp, answer
	}
	expect := func(resp *httptest.ResponseRecorder, answer map[string]any, status int, code string) {
		t.Helper()
		if resp.Code != status || answer["code"] != code {
			t.Fatalf("answer = %d %v, want %d %s", resp.Code, answer, status, code)
		}
	}

	const sourceA, sourceB, sourceC = "203.0.113.1", "203.0.113.2", "203.0.113.3"

	first := newActor("trial", false)
	resp, answer := ask(first, "start", sourceA)
	expect(resp, answer, http.StatusServiceUnavailable, store.RuntimeHostProvisioningCode)
	// The app asks again every few seconds while it waits. That isn't another VM.
	for range 3 {
		resp, answer = ask(first, "start", sourceA)
		expect(resp, answer, http.StatusServiceUnavailable, store.RuntimeHostProvisioningCode)
	}

	// Another account at the same address has used up what the address may ask for.
	second := newActor("trial", false)
	resp, answer = ask(second, "start", sourceA)
	expect(resp, answer, http.StatusTooManyRequests, store.TrialVMRateCode)
	if resp.Header().Get("Retry-After") == "" || answer["retry_after_seconds"] == nil ||
		!strings.Contains(answer["error"].(string), "deposit") {
		t.Fatalf("source refusal = %v %v", resp.Header(), answer)
	}
	resp, answer = ask(second, "start", sourceB)
	expect(resp, answer, http.StatusServiceUnavailable, store.RuntimeHostProvisioningCode)

	// The pool is full now, from any address.
	third := newActor("trial", false)
	resp, answer = ask(third, "start", sourceC)
	expect(resp, answer, http.StatusServiceUnavailable, store.TrialVMCapacityCode)
	if resp.Header().Get("Retry-After") != "300" || !strings.Contains(answer["error"].(string), "deposit") {
		t.Fatalf("capacity refusal = %v %v", resp.Header(), answer)
	}
	// Deleting a runtime with nothing stored waits too, and isn't told to pay.
	resp, answer = ask(third, "delete", sourceC)
	expect(resp, answer, http.StatusServiceUnavailable, store.TrialVMCapacityCode)
	if strings.Contains(answer["error"].(string), "deposit") {
		t.Fatalf("a deletion was told to deposit: %v", answer)
	}

	// A paying account from the same busy address gets its VM.
	resp, answer = ask(newActor("operator", false), "start", sourceA)
	expect(resp, answer, http.StatusServiceUnavailable, store.RuntimeHostProvisioningCode)

	// So does deleting or wiping stored data, past the full pool and that address's limit.
	resp, answer = ask(newActor("trial", true), "delete", sourceA)
	expect(resp, answer, http.StatusServiceUnavailable, store.RuntimeHostProvisioningCode)
	resp, answer = ask(newActor("trial", true), "wipe", sourceA)
	expect(resp, answer, http.StatusServiceUnavailable, store.RuntimeHostProvisioningCode)
	// But starting one has no such claim.
	resp, answer = ask(newActor("trial", true), "start", sourceC)
	expect(resp, answer, http.StatusServiceUnavailable, store.TrialVMCapacityCode)
}

func TestWithoutGuestLogcat(t *testing.T) {
	const socket = "encrypted viewer socket /x did not open after 30 seconds"
	for _, test := range []struct{ message, want string }{
		// What nodes on images through 29 send, with and without the viewer's own logs.
		{
			"Viewer prepare failed: " + socket + ": host viewer log: a | guest viewer log: b | logcat: 11-06 12:00:00 I bank: code 4821.",
			"Viewer prepare failed: " + socket + ": host viewer log: a | guest viewer log: b.",
		},
		{
			"Viewer prewarm failed: " + socket + ": logcat: 11-06 I bank: first line\n11-06 I bank: code 4821\n.",
			"Viewer prewarm failed: " + socket + ".",
		},
		// What they send when there's no logcat, and what any node sends otherwise.
		{
			"Viewer prepare failed: " + socket + ": host viewer log: a | guest viewer log: b.",
			"Viewer prepare failed: " + socket + ": host viewer log: a | guest viewer log: b.",
		},
		{"Encrypted viewer bridge prewarmed.", "Encrypted viewer bridge prewarmed."},
		{"The word logcat: on its own stays.", "The word logcat: on its own stays."},
	} {
		if got := withoutGuestLogcat(test.message); got != test.want {
			t.Errorf("withoutGuestLogcat(%q) = %q, want %q", test.message, got, test.want)
		}
	}
}

func TestRuntimeLogAppendKeepsGuestLogcatOutPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	device := newTestDevice(t)
	if _, err := st.BootstrapAccountWithIdentity(ctx, device.accountID, device.deviceID, "CI device", device.publicKey(t), store.CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer func() { _ = st.DeleteAccount(context.Background(), device.accountID) }()
	runtime, err := st.CreateRuntime(ctx, device.accountID, store.CreateRuntimeInput{Name: "CI runtime", FileMode: "upload-only"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	node := newProvisionerTestKey(t)
	nodeID := "ci-log-" + uuid.NewString()
	operatorID := "ci-log-operator-" + uuid.NewString()[:8]
	if _, err := st.ApproveNode(ctx, store.ApproveNodeInput{
		NodeID: nodeID, OperatorID: operatorID, OperatorName: "CI log operator", PublicKey: node.public,
		Actor: "ci", Reason: "fixture",
	}); err != nil {
		t.Fatalf("approve node: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO hosts (id, name, advertise_addr, public_key) VALUES ($1, 'CI log', '127.0.0.1', $2)`, nodeID, node.public,
	); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`UPDATE runtimes SET host_id = NULL WHERE host_id = $1`, nodeID)
		_, _ = db.Exec(`DELETE FROM node_request_nonces WHERE node_id = $1`, nodeID)
		_, _ = db.Exec(`DELETE FROM hosts WHERE id = $1`, nodeID)
		_, _ = db.Exec(`DELETE FROM node_registry_audit WHERE node_id = $1`, nodeID)
		_, _ = db.Exec(`DELETE FROM approved_nodes WHERE node_id = $1`, nodeID)
		_, _ = db.Exec(`DELETE FROM operator_registry_audit WHERE operator_id = $1`, operatorID)
		_, _ = db.Exec(`DELETE FROM node_operators WHERE id = $1`, operatorID)
	}()
	if _, err := db.ExecContext(ctx, `UPDATE runtimes SET host_id = $2 WHERE id = $1`, runtime.ID, nodeID); err != nil {
		t.Fatalf("assign runtime: %v", err)
	}

	handler := New(config.ServerConfig{AppEnv: "development"}, st)
	body, err := json.Marshal(map[string]string{
		"source": "node", "level": "error",
		"message": "Viewer prepare failed: socket did not open: guest viewer log: viewer stopped | logcat: I bank: code 4821.",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, signedTestRequest(t, node, nodeID, http.MethodPost, "/api/v1/internal/runtimes/"+runtime.ID+"/logs", body))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("append = %d %s", resp.Code, resp.Body.String())
	}

	logs, err := st.ListRuntimeLogs(ctx, device.accountID, runtime.ID, 50)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	found := false
	for _, line := range logs {
		if strings.Contains(line.Message, "4821") || strings.Contains(line.Message, "logcat") {
			t.Fatalf("stored %q", line.Message)
		}
		found = found || line.Message == "Viewer prepare failed: socket did not open: guest viewer log: viewer stopped."
	}
	if !found {
		t.Fatalf("the viewer's own diagnostics weren't stored: %+v", logs)
	}
}
