package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/callbackauth"
	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

func newTestSpool(t *testing.T, maxBytes int64) *importSpool {
	t.Helper()
	spool, err := newImportSpool(filepath.Join(t.TempDir(), "imports"), maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return spool
}

func spoolFiles(t *testing.T, spool *importSpool) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(spool.dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestNewImportSpoolClearsWhatAnEarlierProcessLeft(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "import-stale")
	other := filepath.Join(dir, "not-an-import")
	for _, path := range []string{stale, other} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newImportSpool(dir, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a stale import survived: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a file that isn't an import was removed: %v", err)
	}
}

func TestNewImportSpoolRefusesAnUnusableDirectory(t *testing.T) {
	if _, err := newImportSpool("", 1<<20); err == nil {
		t.Fatal("an empty directory name was accepted")
	}
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newImportSpool(file, 1<<20); err == nil {
		t.Fatal("a file was accepted as the directory")
	}
}

func TestImportSpoolKeepsAnImportThatMatchesItsHash(t *testing.T) {
	spool := newTestSpool(t, 1<<20)
	content := []byte("sealed for the VM")
	spooled, err := spool.receive("account", bytes.NewReader(content), int64(len(content)), 1024, callbackauth.BodyHash(content))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(spooled.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || spooled.size != int64(len(content)) || spooled.bodyHash != callbackauth.BodyHash(content) {
		t.Fatalf("spooled = %+v, mode %v", spooled, info.Mode())
	}
	body, err := spooled.open()
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if got, err := io.ReadAll(body); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("spooled content = %q, err=%v", got, err)
	}
	spooled.discard()
	spooled.discard() // A second discard changes nothing.
	if files := spoolFiles(t, spool); len(files) != 0 {
		t.Fatalf("discard left %v", files)
	}
	if spool.budget.used != 0 {
		t.Fatalf("discard left %d bytes charged", spool.budget.used)
	}
}

func TestImportSpoolLeavesNothingBehindARefusedImport(t *testing.T) {
	content := []byte("sealed for the VM")
	cases := []struct {
		name     string
		body     io.Reader
		declared int64
		maxSize  int64
		hash     string
		want     error
	}{
		{"wrong hash", bytes.NewReader(content), int64(len(content)), 1024, callbackauth.BodyHash([]byte("other")), errSpooledImportHash},
		{"empty", bytes.NewReader(nil), 0, 1024, callbackauth.BodyHash(nil), errSpooledImportSize},
		{"declared too large", bytes.NewReader(content), 2048, 1024, callbackauth.BodyHash(content), errSpooledImportSize},
		{"undeclared too large", bytes.NewReader(make([]byte, 2048)), -1, 1024, callbackauth.BodyHash(content), errSpooledImportSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spool := newTestSpool(t, 1<<20)
			if _, err := spool.receive("account", tc.body, tc.declared, tc.maxSize, tc.hash); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if files := spoolFiles(t, spool); len(files) != 0 {
				t.Fatalf("left %v", files)
			}
			if spool.budget.used != 0 {
				t.Fatalf("left %d bytes charged", spool.budget.used)
			}
		})
	}
}

func TestImportSpoolWritesNoMoreThanTheLimitForAnUndeclaredBody(t *testing.T) {
	spool := newTestSpool(t, 1<<20)
	body := &countedBody{remaining: 1 << 20}
	if _, err := spool.receive("account", body, -1, 1024, "any"); !errors.Is(err, errSpooledImportSize) {
		t.Fatalf("err = %v", err)
	}
	if body.taken > 1025 {
		t.Fatalf("took %d bytes for a 1024-byte limit", body.taken)
	}
}

func TestImportSpoolBudgetsAccountsAndTheTotal(t *testing.T) {
	spool := newTestSpool(t, 3*(maxRuntimeFileImportBytes+sealedImportOverhead))
	fullSize := maxRuntimeFileImportBytes + sealedImportOverhead
	content := []byte("x")
	hold := func(account string) *spooledImport {
		t.Helper()
		// The charge is the declared length, whatever has arrived so far.
		spooled, err := spool.receive(account, bytes.NewReader(content), fullSize, fullSize, callbackauth.BodyHash(content))
		if err != nil {
			t.Fatal(err)
		}
		return spooled
	}
	first, second := hold("a"), hold("a")
	busy := &countedBody{remaining: fullSize}
	if _, err := spool.receive("a", busy, fullSize, fullSize, "any"); !errors.Is(err, errImportSpoolBusy) {
		t.Fatalf("a third import for one account: err = %v", err)
	}
	if busy.taken != 0 {
		t.Fatalf("took %d bytes for an import with no room", busy.taken)
	}
	third := hold("b")
	if _, err := spool.receive("c", &countedBody{remaining: fullSize}, fullSize, fullSize, "any"); !errors.Is(err, errImportSpoolBusy) {
		t.Fatalf("an import past the total: err = %v", err)
	}
	third.discard()
	hold("c").discard()
	first.discard()
	second.discard()
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the phone went away") }

func TestSpoolSealedImportAnswersEachFailure(t *testing.T) {
	content := []byte("sealed for the VM")
	const maxBytes = 1024
	signed := signedCapabilityRequest{actor: runtimeActor{accountID: "account"}, bodyHash: callbackauth.BodyHash(content)}
	cases := []struct {
		name  string
		api   func(t *testing.T) *API
		body  func(w http.ResponseWriter, r *http.Request) io.ReadCloser
		size  int64
		want  int
		code  string
		retry bool
	}{
		{
			name: "no spool",
			api:  func(*testing.T) *API { return &API{} },
			want: http.StatusServiceUnavailable, code: "runtime_import_unavailable",
		},
		{
			name: "spool directory gone",
			api: func(t *testing.T) *API {
				spool := newTestSpool(t, 1<<20)
				if err := os.RemoveAll(spool.dir); err != nil {
					t.Fatal(err)
				}
				return &API{importSpool: spool}
			},
			want: http.StatusServiceUnavailable, code: "runtime_import_unavailable",
		},
		{
			name: "no room",
			api:  func(t *testing.T) *API { return &API{importSpool: newTestSpool(t, 1)} },
			want: http.StatusTooManyRequests, code: "runtime_import_busy", retry: true,
		},
		{
			name: "declared too large",
			api:  func(t *testing.T) *API { return &API{importSpool: newTestSpool(t, 1<<20)} },
			size: maxBytes + sealedImportOverhead + 1,
			want: http.StatusRequestEntityTooLarge, code: "file_too_large",
		},
		{
			name: "past the route's limit",
			api:  func(t *testing.T) *API { return &API{importSpool: newTestSpool(t, 1<<20)} },
			body: func(w http.ResponseWriter, r *http.Request) io.ReadCloser {
				return http.MaxBytesReader(w, io.NopCloser(bytes.NewReader(make([]byte, 64))), 8)
			},
			size: -1,
			want: http.StatusRequestEntityTooLarge, code: "file_too_large",
		},
		{
			name: "phone went away",
			api:  func(t *testing.T) *API { return &API{importSpool: newTestSpool(t, 1<<20)} },
			body: func(http.ResponseWriter, *http.Request) io.ReadCloser { return io.NopCloser(failingReader{}) },
			want: http.StatusBadRequest, code: "file_read_failed",
		},
		{
			name: "wrong hash",
			api:  func(t *testing.T) *API { return &API{importSpool: newTestSpool(t, 1<<20)} },
			body: func(http.ResponseWriter, *http.Request) io.ReadCloser {
				return io.NopCloser(strings.NewReader("tampered"))
			},
			want: http.StatusUnauthorized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/me/runtimes/x/files?sealed=1", bytes.NewReader(content))
			if tc.body != nil {
				req.Body = tc.body(resp, req)
			}
			if tc.size != 0 {
				req.ContentLength = tc.size
			}
			if _, ok := tc.api(t).spoolSealedImport(resp, req, signed, "file", maxBytes); ok {
				t.Fatal("the import was accepted")
			}
			if resp.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.Code, tc.want, resp.Body.String())
			}
			if tc.code != "" && !strings.Contains(resp.Body.String(), tc.code) {
				t.Fatalf("body = %s, want code %s", resp.Body.String(), tc.code)
			}
			if got := resp.Header().Get("Retry-After") != ""; got != tc.retry {
				t.Fatalf("Retry-After present = %v, want %v", got, tc.retry)
			}
		})
	}
}

// capabilityHeaders are what a phone sends with an import, signed over content.
func capabilityHeaders(t *testing.T, key *ecdsa.PrivateKey, capabilityID, target string, content []byte) http.Header {
	t.Helper()
	digest := sha256.Sum256(content)
	bodyHash := base64.RawURLEncoding.EncodeToString(digest[:])
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.NewString()
	canonical := sha256.Sum256([]byte(capabilityRequestCanonical(http.MethodPost, target, capabilityID, timestamp, nonce, bodyHash)))
	signature, err := ecdsa.SignASN1(rand.Reader, key, canonical[:])
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set("X-Virtroid-Capability-ID", capabilityID)
	headers.Set("X-Virtroid-Capability-Timestamp", timestamp)
	headers.Set("X-Virtroid-Capability-Nonce", nonce)
	headers.Set("X-Virtroid-Capability-Body-SHA256", bodyHash)
	headers.Set("X-Virtroid-Capability-Signature", base64.RawURLEncoding.EncodeToString(signature))
	return headers
}

// The whole import path, as a phone and a node see it.
func TestSealedImportPostgresIntegration(t *testing.T) {
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

	callbackKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	callbackPrivateDER, err := x509.MarshalPKCS8PrivateKey(callbackKey)
	if err != nil {
		t.Fatal(err)
	}
	callbackPublicDER, err := x509.MarshalPKIXPublicKey(&callbackKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	callbackPublic := base64.StdEncoding.EncodeToString(callbackPublicDER)

	var mu sync.Mutex
	var received [][]byte
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("node read: %v", readErr)
		}
		if verifyErr := callbackauth.Verify(
			callbackPublic,
			r.Method,
			r.URL.RequestURI(),
			r.Header.Get(callbackauth.HeaderTimestamp),
			r.Header.Get(callbackauth.HeaderNonce),
			r.Header.Get(callbackauth.HeaderBodySHA256),
			r.Header.Get(callbackauth.HeaderSignature),
		); verifyErr != nil {
			t.Errorf("node verify: %v", verifyErr)
		}
		if callbackauth.BodyHash(body) != r.Header.Get(callbackauth.HeaderBodySHA256) {
			t.Errorf("the callback's hash doesn't match the %d bytes that arrived", len(body))
		}
		if r.ContentLength != int64(len(body)) {
			t.Errorf("Content-Length = %d, want %d", r.ContentLength, len(body))
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]any{"saved": true})
	}))
	defer node.Close()
	nodeHost, nodePortRaw, err := net.SplitHostPort(node.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	nodePort, err := strconv.Atoi(nodePortRaw)
	if err != nil {
		t.Fatal(err)
	}

	device := newTestDevice(t)
	if _, err := st.BootstrapAccountWithIdentity(ctx, device.accountID, device.deviceID, "CI device", device.publicKey(t), store.CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer func() { _ = st.DeleteAccount(context.Background(), device.accountID) }()
	runtime, err := st.CreateRuntime(ctx, device.accountID, store.CreateRuntimeInput{Name: "CI import", FileMode: "upload-only"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	hostID := "ci-import-" + uuid.NewString()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO hosts (id, name, advertise_addr, relay_port, file_import) VALUES ($1, 'CI import', $2, $3, TRUE)`,
		hostID, nodeHost, nodePort,
	); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	defer func() {
		_, _ = db.Exec(`UPDATE runtimes SET host_id = NULL WHERE host_id = $1`, hostID)
		_, _ = db.Exec(`DELETE FROM hosts WHERE id = $1`, hostID)
	}()
	setRuntimeOnline := func(online string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`UPDATE runtimes SET status = 'running', desired_state = 'running', connection_status = $2, host_id = $3 WHERE id = $1`,
			runtime.ID, online, hostID,
		); err != nil {
			t.Fatalf("set runtime state: %v", err)
		}
	}
	setRuntimeOnline("online")

	capabilityKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	capabilityDER, err := x509.MarshalPKIXPublicKey(&capabilityKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	capabilityPublic := base64.StdEncoding.EncodeToString(capabilityDER)
	capabilityID := runtimeCapabilityID(runtime.ID, capabilityPublic)
	if _, err := st.UpsertRuntimeCapability(ctx, device.accountID, runtime.ID, capabilityID, capabilityPublic); err != nil {
		t.Fatalf("upsert capability: %v", err)
	}
	session, err := st.CreateSessionWithCapability(ctx, device.accountID, capabilityID, runtime.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	spoolDir := filepath.Join(t.TempDir(), "imports")
	handler := New(config.ServerConfig{
		AppEnv:                         "production",
		NodeAllowedAdvertiseAddrs:      []string{nodeHost},
		ControlPlaneCallbackPrivateKey: base64.StdEncoding.EncodeToString(callbackPrivateDER),
		RuntimeImportSpoolDir:          spoolDir,
		RuntimeImportSpoolMaxBytes:     1 << 30,
	}, st)
	server := httptest.NewServer(handler)
	defer server.Close()

	content := make([]byte, 5<<20)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	target := "/api/v1/me/runtimes/" + runtime.ID + "/files?session_id=" + session.ID + "&sealed=1"
	send := func(path string, headers http.Header, body []byte) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = headers.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(payload)
	}
	nodeReceived := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(received)
	}
	refusedUnread := func(name string, req *http.Request, want int) {
		t.Helper()
		body := &countedBody{remaining: maxRuntimeFileImportBytes}
		body.attach(req)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("%s: status = %d, want %d: %s", name, resp.Code, want, resp.Body.String())
		}
		if body.taken != 0 {
			t.Fatalf("%s: took %d bytes", name, body.taken)
		}
	}
	signedRequest := func(path string) *http.Request {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header = capabilityHeaders(t, capabilityKey, capabilityID, path, content)
		return req
	}

	refusedUnread("forged", forgedCapabilityRequest(target, &countedBody{}), http.StatusUnauthorized)
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKeyRequest := httptest.NewRequest(http.MethodPost, target, nil)
	wrongKeyRequest.Header = capabilityHeaders(t, wrongKey, capabilityID, target, content)
	refusedUnread("the wrong key", wrongKeyRequest, http.StatusUnauthorized)
	refusedUnread("no session", signedRequest("/api/v1/me/runtimes/"+runtime.ID+"/files?sealed=1"), http.StatusConflict)
	setRuntimeOnline("offline")
	refusedUnread("runtime offline", signedRequest(target), http.StatusConflict)
	setRuntimeOnline("online")

	// A tampered body is refused without spending the nonce, so an upload that
	// breaks off can be sent again as it is.
	headers := capabilityHeaders(t, capabilityKey, capabilityID, target, content)
	tampered := append([]byte(nil), content...)
	tampered[0] ^= 0xff
	if status, payload := send(target, headers, tampered); status != http.StatusUnauthorized || !strings.Contains(payload, "body hash mismatch") {
		t.Fatalf("tampered: %d %s", status, payload)
	}
	if got := nodeReceived(); got != 0 {
		t.Fatalf("the node received %d imports before any valid one", got)
	}

	status, payload := send(target, headers, content)
	if status != http.StatusCreated {
		t.Fatalf("import: %d %s", status, payload)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(payload), &answer); err != nil || answer["saved"] != true {
		t.Fatalf("the node's answer wasn't passed on: %q, err=%v", payload, err)
	}
	mu.Lock()
	if len(received) != 1 || !bytes.Equal(received[0], content) {
		mu.Unlock()
		t.Fatalf("the node received %d imports, or different bytes", len(received))
	}
	mu.Unlock()

	if status, payload := send(target, headers, content); status != http.StatusUnauthorized || !strings.Contains(payload, "already been used") {
		t.Fatalf("replay: %d %s", status, payload)
	}
	if got := nodeReceived(); got != 1 {
		t.Fatalf("a replay reached the node: %d imports", got)
	}
	left, err := filepath.Glob(filepath.Join(spoolDir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("the spool still holds %v", left)
	}

	// A node's forged request is refused unread as well.
	api := &API{cfg: config.ServerConfig{AppEnv: "production"}, store: st}
	forgedNode := httptest.NewRequest(http.MethodPost, "/api/v1/internal/heartbeat", nil)
	forgedNodeBody := &countedBody{remaining: maxAPIRequestBodyBytes}
	forgedNodeBody.attach(forgedNode)
	forgedNode.Header = forgedNodeHeaders()
	forgedNodeResponse := httptest.NewRecorder()
	if _, ok := api.requireNodeRequest(forgedNodeResponse, forgedNode, false); ok || forgedNodeResponse.Code != http.StatusUnauthorized {
		t.Fatalf("forged node request: ok=%v status=%d", ok, forgedNodeResponse.Code)
	}
	if forgedNodeBody.taken != 0 {
		t.Fatalf("took %d bytes of a forged node request", forgedNodeBody.taken)
	}
}

func forgedNodeHeaders() http.Header {
	headers := http.Header{}
	headers.Set("X-Virtroid-Node-ID", "ci-not-a-node-"+uuid.NewString())
	headers.Set("X-Virtroid-Node-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	headers.Set("X-Virtroid-Node-Nonce", "made-up")
	headers.Set("X-Virtroid-Node-Body-SHA256", "made-up")
	headers.Set("X-Virtroid-Node-Signature", "made-up")
	return headers
}

func TestForwardSpooledImportReportsAnUnreachableNode(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	node := httptest.NewServer(http.NotFoundHandler())
	host, portRaw, err := net.SplitHostPort(node.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatal(err)
	}
	node.Close()

	spool := newTestSpool(t, 1<<20)
	content := []byte("sealed for the VM")
	spooled, err := spool.receive("account", bytes.NewReader(content), int64(len(content)), 1024, callbackauth.BodyHash(content))
	if err != nil {
		t.Fatal(err)
	}
	defer spooled.discard()
	api := &API{cfg: config.ServerConfig{
		AppEnv:                         "production",
		NodeAllowedAdvertiseAddrs:      []string{host},
		ControlPlaneCallbackPrivateKey: base64.StdEncoding.EncodeToString(privateDER),
	}}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/runtimes/x/files", nil)
	api.forwardSpooledImport(resp, req, store.Host{AdvertiseAddr: host, RelayPort: port}, "/api/v1/internal/runtimes/x/files?sealed=1", spooled)
	if resp.Code != http.StatusBadGateway || !strings.Contains(resp.Body.String(), "runtime_media_proxy_failed") {
		t.Fatalf("response = %d %s", resp.Code, resp.Body.String())
	}
}
