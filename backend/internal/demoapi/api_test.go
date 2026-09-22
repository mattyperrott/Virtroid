package demoapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDemoRoutesAndAssets(t *testing.T) {
	server := httptest.NewServer(NewWithOptions(Options{}))
	defer server.Close()

	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	response, err := client.Get(server.URL + "/demo")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusPermanentRedirect || response.Header.Get("Location") != "/demo/" {
		t.Fatalf("unexpected demo redirect: %d %q", response.StatusCode, response.Header.Get("Location"))
	}

	response, err = client.Get(server.URL + "/demo/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected demo status: %d", response.StatusCode)
	}
	policy := response.Header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "frame-ancestors 'none'") || !strings.Contains(policy, "frame-src 'self'") {
		t.Fatalf("demo response has an incomplete frame policy: %q", policy)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected index cache policy: %q", response.Header.Get("Cache-Control"))
	}
	if !strings.Contains(string(body), "Interactive Virtroid Android client") {
		t.Fatal("demo page is missing the live handset frame")
	}

	response, err = client.Get(server.URL + "/demo/app.js")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected demo asset status: %d", response.StatusCode)
	}
	if !strings.Contains(response.Header.Get("Cache-Control"), "must-revalidate") {
		t.Fatalf("unexpected asset cache policy: %q", response.Header.Get("Cache-Control"))
	}
}

func TestDemoSessionRequiresConfigurationAndRequestHeader(t *testing.T) {
	server := httptest.NewServer(NewWithOptions(Options{}))
	defer server.Close()

	response := postDemo(t, server.Client(), server.URL+"/demo/api/session", false)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing request header returned %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	response.Body.Close()

	response = postDemo(t, server.Client(), server.URL+"/demo/api/session", true)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured session returned %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	response.Body.Close()
}

func TestDemoSessionOwnershipAndRelease(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	handler := NewWithOptions(Options{
		StreamTarget: upstream.URL,
		StreamDevice: "demo-handset:5555",
		SessionTTL:   2 * time.Minute,
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	owner := clientWithCookies(t)
	visitor := clientWithCookies(t)

	response := postDemo(t, owner, server.URL+"/demo/api/session", true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("owner claim returned %d, want %d", response.StatusCode, http.StatusOK)
	}
	var session struct {
		EmbedURL string `json:"embed_url"`
	}
	decodeJSON(t, response, &session)
	if !strings.HasPrefix(session.EmbedURL, "/demo/device/embed.html?") ||
		!strings.Contains(session.EmbedURL, "device=demo-handset%3A5555") ||
		!strings.Contains(session.EmbedURL, "pathname=%2Fdemo%2Fdevice%2Fstream") {
		t.Fatalf("unexpected embed URL: %q", session.EmbedURL)
	}

	response = postDemo(t, visitor, server.URL+"/demo/api/session", true)
	if response.StatusCode != http.StatusLocked {
		t.Fatalf("second visitor claim returned %d, want %d", response.StatusCode, http.StatusLocked)
	}
	response.Body.Close()

	response, err := visitor.Get(server.URL + "/demo/device/embed.html")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("visitor proxy access returned %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	response = postDemo(t, owner, server.URL+"/demo/api/session/end", true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("owner release returned %d, want %d", response.StatusCode, http.StatusOK)
	}
	response.Body.Close()

	response = postDemo(t, visitor, server.URL+"/demo/api/session", true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim after release returned %d, want %d", response.StatusCode, http.StatusOK)
	}
	response.Body.Close()
}

func TestDemoProxyAllowlistAndRewrite(t *testing.T) {
	type observedRequest struct {
		Path  string
		Query string
		Host  string
	}
	observed := make(chan observedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- observedRequest{Path: r.URL.Path, Query: r.URL.RawQuery, Host: r.Host}
		w.Header().Set("Set-Cookie", "ws_scrcpy_token=private; Path=/; SameSite=Strict; HttpOnly")
		if r.URL.Path == "/ws-scrcpy.umd.js" {
			_, _ = w.Write([]byte(`before,fitToScreen:!0,m6.start(l,void 0,!0,u,t,after`))
			return
		}
		_, _ = w.Write([]byte(`<body><script src="ws-scrcpy.umd.js"></script><script src="embed.js"></script></body>`))
	}))
	defer upstream.Close()

	server := httptest.NewServer(NewWithOptions(Options{
		StreamTarget: upstream.URL,
		StreamDevice: "demo-handset:5555",
		SessionTTL:   time.Minute,
	}))
	defer server.Close()
	client := clientWithCookies(t)

	response := postDemo(t, client, server.URL+"/demo/api/session", true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim returned %d", response.StatusCode)
	}
	response.Body.Close()

	response, err := client.Get(server.URL + "/demo/device/embed.html?theme=dark")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `src="../stream-bridge.js?v=20260922-3"`) ||
		!strings.Contains(string(body), `.control-buttons-list { display: none !important; }`) {
		t.Fatalf("allowed asset returned %d %q", response.StatusCode, body)
	}
	if cookies := response.Header.Values("Set-Cookie"); len(cookies) != 1 || !strings.Contains(cookies[0], "Path=/demo/device/") {
		t.Fatalf("upstream bearer cookie was not narrowed: %v", cookies)
	}
	select {
	case got := <-observed:
		if got.Path != "/embed.html" || got.Query != "theme=dark" {
			t.Fatalf("unexpected upstream request: %+v", got)
		}
		if got.Host != strings.TrimPrefix(server.URL, "http://") {
			t.Fatalf("upstream host %q did not preserve the public host", got.Host)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive the allowed asset request")
	}

	response, err = client.Get(server.URL + "/demo/device/ws-scrcpy.umd.js")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "fitToScreen:!1") || strings.Contains(string(body), "fitToScreen:!0") ||
		!strings.Contains(string(body), "m6.start(l,void 0,!1,u,t") || strings.Contains(string(body), "m6.start(l,void 0,!0,u,t") {
		t.Fatalf("viewer bundle was not fixed-size patched: %d %q", response.StatusCode, body)
	}
	select {
	case got := <-observed:
		if got.Path != "/ws-scrcpy.umd.js" {
			t.Fatalf("unexpected viewer bundle request: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive the viewer bundle request")
	}

	response, err = client.Get(server.URL + "/demo/device/api/settings/device?udid=demo-handset%3A5555")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("demo handset settings returned %d, want %d", response.StatusCode, http.StatusOK)
	}
	select {
	case got := <-observed:
		if got.Path != "/api/settings/device" || got.Query != "udid=demo-handset%3A5555" {
			t.Fatalf("unexpected settings request: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive the settings request")
	}

	for _, path := range []string{"api/config", "", "index.html", "shell"} {
		response, err = client.Get(server.URL + "/demo/device/" + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("disallowed proxy path %q returned %d, want %d", path, response.StatusCode, http.StatusNotFound)
		}
	}

	response, err = client.Get(server.URL + "/demo/device/stream?action=stream&udid=demo-handset%3A5555")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-WebSocket stream returned %d, want %d", response.StatusCode, http.StatusBadRequest)
	}

	response, err = client.Get(server.URL + "/demo/device/api/settings/device?udid=another-handset%3A5555")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign handset settings returned %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestDemoSessionRequiresReadyWelcomeScreen(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	ready := false
	server := httptest.NewServer(NewWithOptions(Options{
		StreamTarget: upstream.URL,
		StreamDevice: "demo-handset:5555",
		SessionTTL:   time.Minute,
		ReadyCheck: func(context.Context) (bool, string) {
			if ready {
				return true, "ready"
			}
			return false, "app_starting"
		},
	}))
	defer server.Close()
	client := clientWithCookies(t)

	response, err := client.Get(server.URL + "/demo/api/status")
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Ready bool `json:"ready"`
	}
	decodeJSON(t, response, &status)
	if status.Ready {
		t.Fatal("demo became startable before the welcome screen was ready")
	}

	response = postDemo(t, client, server.URL+"/demo/api/session", true)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unready session returned %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
	response.Body.Close()

	ready = true
	response = postDemo(t, client, server.URL+"/demo/api/session", true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ready session returned %d, want %d", response.StatusCode, http.StatusOK)
	}
	var session struct {
		EmbedURL string `json:"embed_url"`
	}
	decodeJSON(t, response, &session)
	for _, expected := range []string{"maxFps=30", "maxSize=1600", "bitrate=6000000"} {
		if !strings.Contains(session.EmbedURL, expected) {
			t.Fatalf("embed URL %q is missing %q", session.EmbedURL, expected)
		}
	}
}

func TestADBWelcomeReadinessStartsLauncher(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "welcome")
	logPath := filepath.Join(directory, "commands")
	script := filepath.Join(directory, "adb")
	content := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
case "$*" in
  "connect demo-handset:5555") echo "connected to demo-handset:5555" ;;
  "-s demo-handset:5555 get-state") echo "device" ;;
  "-s demo-handset:5555 shell getprop sys.boot_completed") echo "1" ;;
  *"dumpsys activity activities"*)
    if [ -f "` + marker + `" ]; then echo "mResumedActivity io.virtroid.client/.WelcomeActivity"; else echo "mResumedActivity com.android.launcher3/.QuickstepLauncher"; fi ;;
  *"am start -W -n io.virtroid.client/.LauncherActivity"*) touch "` + marker + `" ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	ready, state := adbWelcomeReadiness(
		script,
		"demo-handset:5555",
		"io.virtroid.client",
		"io.virtroid.client/.LauncherActivity",
		"io.virtroid.client/.WelcomeActivity",
	)(context.Background())
	if !ready || state != "ready" {
		t.Fatalf("readiness = %v %q, want true ready", ready, state)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "am force-stop io.virtroid.client") ||
		!strings.Contains(string(commands), "am start -W -n io.virtroid.client/.LauncherActivity") {
		t.Fatalf("welcome reset commands missing:\n%s", commands)
	}
}

func TestSecureCookieBehindHTTPSProxy(t *testing.T) {
	server := httptest.NewServer(NewWithOptions(Options{
		StreamTarget: "http://viewer.internal:8000",
		StreamDevice: "demo-handset:5555",
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/demo/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(demoRequestHeader, "1")
	request.Header.Set("X-Forwarded-Proto", "https")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(response.Cookies()) != 1 || !response.Cookies()[0].Secure || response.Cookies()[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected proxied HTTPS cookie: %+v", response.Cookies())
	}
}

func clientWithCookies(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func postDemo(t *testing.T, client *http.Client, target string, withHeader bool) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withHeader {
		request.Header.Set(demoRequestHeader, "1")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeJSON(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
