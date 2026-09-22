package demoapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	demoSessionCookie = "virtroid_demo_session"
	demoRequestHeader = "X-Virtroid-Demo"
	defaultSessionTTL = 8 * time.Minute
	defaultReadyCache = 4 * time.Second
)

//go:embed web
var webAssets embed.FS

// Options configures the isolated Android stream behind the public demo page.
type Options struct {
	StreamTarget string
	StreamDevice string
	SessionTTL   time.Duration
	ReadyCheck   func(context.Context) (bool, string)
}

// New returns the public Virtroid interactive demo. The browser gateway stays
// disabled unless both DEMO_STREAM_TARGET and DEMO_STREAM_DEVICE are set.
func New() http.Handler {
	ttl := defaultSessionTTL
	if raw := strings.TrimSpace(os.Getenv("DEMO_SESSION_TTL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed >= time.Minute && parsed <= 30*time.Minute {
			ttl = parsed
		}
	}
	options := Options{
		StreamTarget: strings.TrimSpace(os.Getenv("DEMO_STREAM_TARGET")),
		StreamDevice: strings.TrimSpace(os.Getenv("DEMO_STREAM_DEVICE")),
		SessionTTL:   ttl,
	}
	if adbTarget := strings.TrimSpace(os.Getenv("DEMO_ADB_TARGET")); adbTarget != "" {
		options.ReadyCheck = adbWelcomeReadiness(
			"/usr/bin/adb",
			adbTarget,
			"io.virtroid.client",
			"io.virtroid.client/.LauncherActivity",
			"io.virtroid.client/.WelcomeActivity",
		)
	}
	return NewWithOptions(options)
}

// NewWithOptions is the testable constructor used by New.
func NewWithOptions(options Options) http.Handler {
	assets, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	if options.SessionTTL <= 0 {
		options.SessionTTL = defaultSessionTTL
	}

	sessions := &sessionManager{ttl: options.SessionTTL}
	readiness := &readinessGate{check: options.ReadyCheck, ttl: defaultReadyCache}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/demo/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("GET /demo/", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(assets, w, r)
	})
	mux.HandleFunc("GET /demo/api/status", func(w http.ResponseWriter, r *http.Request) {
		available, owned, expiresAt := sessions.status(r)
		configured := options.StreamTarget != "" && options.StreamDevice != ""
		ready, readyState := false, "not_configured"
		if configured {
			switch {
			case owned:
				ready, readyState = true, "session_active"
			case !available:
				readyState = "session_busy"
			default:
				ready, readyState = readiness.status(r.Context(), false)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"available":   available,
			"owned":       owned,
			"configured":  configured,
			"ready":       ready,
			"ready_state": readyState,
			"expires_at":  expiresAt,
		})
	})
	mux.HandleFunc("POST /demo/api/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(demoRequestHeader) != "1" {
			http.Error(w, "demo request header required", http.StatusForbidden)
			return
		}
		if options.StreamTarget == "" || options.StreamDevice == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "offline"})
			return
		}
		if !sessions.owns(r) {
			ready, state := readiness.status(r.Context(), true)
			if !ready {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"status": "preparing",
					"state":  state,
				})
				return
			}
		}
		token, expiresAt, owned, ok := sessions.claim(r)
		if !ok {
			writeJSON(w, http.StatusLocked, map[string]any{"status": "busy", "available_at": expiresAt})
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     demoSessionCookie,
			Value:    token,
			Path:     "/demo/",
			Expires:  expiresAt,
			MaxAge:   max(1, int(time.Until(expiresAt).Seconds())),
			HttpOnly: true,
			Secure:   requestIsHTTPS(r),
			SameSite: http.SameSiteStrictMode,
		})
		embedQuery := url.Values{}
		embedQuery.Set("device", options.StreamDevice)
		embedQuery.Set("pathname", "/demo/device/stream")
		embedQuery.Set("codec", "h264")
		embedQuery.Set("audio", "false")
		embedQuery.Set("keyboard", "true")
		embedQuery.Set("maxFps", "30")
		embedQuery.Set("maxSize", "1600")
		embedQuery.Set("bitrate", "6000000")
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "ready",
			"owned":      owned,
			"expires_at": expiresAt,
			"embed_url":  "/demo/device/embed.html?" + embedQuery.Encode(),
		})
	})
	mux.HandleFunc("POST /demo/api/session/end", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(demoRequestHeader) != "1" {
			http.Error(w, "demo request header required", http.StatusForbidden)
			return
		}
		sessions.release(r)
		http.SetCookie(w, &http.Cookie{
			Name:     demoSessionCookie,
			Value:    "",
			Path:     "/demo/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   requestIsHTTPS(r),
			SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, http.StatusOK, map[string]any{"status": "ended"})
	})

	if target, parseErr := url.Parse(options.StreamTarget); parseErr == nil && target.Scheme != "" && target.Host != "" {
		proxy := newStreamProxy(target, options.StreamDevice)
		mux.Handle("GET /demo/device/", sessions.requireSession(proxy))
	}

	return securityHeaders(mux)
}

type sessionManager struct {
	mu        sync.Mutex
	ttl       time.Duration
	token     string
	expiresAt time.Time
}

func (s *sessionManager) owns(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	return s.requestOwnsLocked(r)
}

func (s *sessionManager) status(r *http.Request) (available, owned bool, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	owned = s.requestOwnsLocked(r)
	return s.token == "" || owned, owned, s.expiresAt
}

func (s *sessionManager) claim(r *http.Request) (token string, expiresAt time.Time, owned, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if s.requestOwnsLocked(r) {
		return s.token, s.expiresAt, true, true
	}
	if s.token != "" {
		return "", s.expiresAt, false, false
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, false, false
	}
	s.token = hex.EncodeToString(raw[:])
	s.expiresAt = time.Now().UTC().Add(s.ttl)
	return s.token, s.expiresAt, false, true
}

func (s *sessionManager) release(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if s.requestOwnsLocked(r) {
		s.token = ""
		s.expiresAt = time.Time{}
	}
}

func (s *sessionManager) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.expireLocked()
		owned := s.requestOwnsLocked(r)
		s.mu.Unlock()
		if !owned {
			http.Error(w, "an active demo session is required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *sessionManager) expireLocked() {
	if s.token != "" && !s.expiresAt.After(time.Now().UTC()) {
		s.token = ""
		s.expiresAt = time.Time{}
	}
}

func (s *sessionManager) requestOwnsLocked(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	cookie, err := r.Cookie(demoSessionCookie)
	return err == nil && cookie.Value != "" && cookie.Value == s.token
}

type readinessGate struct {
	mu        sync.Mutex
	check     func(context.Context) (bool, string)
	ttl       time.Duration
	checkedAt time.Time
	ready     bool
	state     string
}

func (g *readinessGate) status(ctx context.Context, force bool) (bool, string) {
	if g.check == nil {
		return true, "ready"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !force && !g.checkedAt.IsZero() && time.Since(g.checkedAt) < g.ttl {
		return g.ready, g.state
	}
	checkCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	g.ready, g.state = g.check(checkCtx)
	g.checkedAt = time.Now()
	return g.ready, g.state
}

func adbWelcomeReadiness(adbPath, target, packageName, launcherActivity, welcomeActivity string) func(context.Context) (bool, string) {
	return func(ctx context.Context) (bool, string) {
		if _, err := runADB(ctx, adbPath, "connect", target); err != nil {
			return false, "handset_unreachable"
		}
		if state, err := runADB(ctx, adbPath, "-s", target, "get-state"); err != nil || strings.TrimSpace(state) != "device" {
			return false, "handset_unreachable"
		}
		if booted, err := runADB(ctx, adbPath, "-s", target, "shell", "getprop", "sys.boot_completed"); err != nil || strings.TrimSpace(booted) != "1" {
			return false, "android_booting"
		}
		if foreground, err := runADB(ctx, adbPath, "-s", target, "shell", "dumpsys", "activity", "activities"); err == nil && activityIsResumed(foreground, welcomeActivity) {
			return true, "ready"
		}

		// The demo handset is disposable. Return it to the exported launcher;
		// LauncherActivity routes a fresh demo install to WelcomeActivity.
		_, _ = runADB(ctx, adbPath, "-s", target, "shell", "am", "force-stop", packageName)
		if _, err := runADB(ctx, adbPath, "-s", target, "shell", "am", "start", "-W", "-n", launcherActivity); err != nil {
			return false, "app_starting"
		}
		for attempt := 0; attempt < 8; attempt++ {
			if foreground, err := runADB(ctx, adbPath, "-s", target, "shell", "dumpsys", "activity", "activities"); err == nil && activityIsResumed(foreground, welcomeActivity) {
				return true, "ready"
			}
			select {
			case <-ctx.Done():
				return false, "app_starting"
			case <-time.After(200 * time.Millisecond):
			}
		}
		return false, "app_starting"
	}
}

func runADB(ctx context.Context, adbPath string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, adbPath, args...)
	command.Env = make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "ANDROID_USER_HOME=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "HOME=/tmp", "ANDROID_USER_HOME=/tmp/.android")
	output, err := command.CombinedOutput()
	if len(output) > 256*1024 {
		output = output[:256*1024]
	}
	return string(output), err
}

func activityIsResumed(output, expected string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "mResumedActivity") && strings.Contains(line, expected) {
			return true
		}
	}
	return false
}

func newStreamProxy(target *url.URL, streamDevice string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		incomingHost := r.Host
		originalDirector(r)
		tail := strings.TrimPrefix(r.URL.Path, "/demo/device/")
		if tail == "stream" {
			tail = ""
		}
		r.URL.Path = "/" + strings.TrimPrefix(tail, "/")
		r.Host = incomingHost
		r.Header.Set("X-Forwarded-Host", incomingHost)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "interactive Android stream unavailable", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		// The upstream gateway issues its own per-process bearer cookie. Keep it
		// scoped to this isolated proxy surface instead of the whole Virtroid
		// domain.
		for index, value := range response.Header.Values("Set-Cookie") {
			response.Header["Set-Cookie"][index] = strings.Replace(value, "Path=/;", "Path=/demo/device/;", 1)
		}
		if response.StatusCode != http.StatusOK {
			return nil
		}
		if response.Request.URL.Path == "/ws-scrcpy.umd.js" {
			content, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			_ = response.Body.Close()
			needle := []byte("fitToScreen:!0")
			if bytes.Count(content, needle) != 1 {
				return io.ErrUnexpectedEOF
			}
			// The upstream public facade otherwise replaces the requested 720x1600
			// stream size with the small iframe's CSS viewport. Keep the explicit
			// maxSize/bitrate/FPS request so the browser receives the native image.
			content = bytes.Replace(content, needle, []byte("fitToScreen:!1"), 1)
			setResponseBody(response, content)
			return nil
		}
		if response.Request.URL.Path != "/embed.html" {
			return nil
		}
		content, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		needle := []byte(`<script src="ws-scrcpy.umd.js"></script>`)
		replacement := []byte(`<style id="virtroid-demo-stream-style">
        html, body, body[data-embed-entry], body[data-embed-entry] .device-view,
        body[data-embed-entry] .video { width: 100%; height: 100%; }
        body[data-embed-entry] .device-view { display: block; }
        body[data-embed-entry] .video { display: grid; place-items: stretch; overflow: hidden; }
        body[data-embed-entry] .video-layer,
        body[data-embed-entry] .touch-layer {
            width: 100% !important; height: 100% !important;
            max-width: none !important; max-height: none !important;
        }
        body[data-embed-entry] .control-buttons-list { display: none !important; }
    </style>` + "\n    " + `<script src="../stream-bridge.js?v=20260922-3"></script>` + "\n    " + `<script src="ws-scrcpy.umd.js"></script>`)
		if !bytes.Contains(content, needle) {
			return io.ErrUnexpectedEOF
		}
		content = bytes.Replace(content, needle, replacement, 1)
		setResponseBody(response, content)
		return nil
	}

	allowedStatic := map[string]bool{
		"embed.html":           true,
		"embed.js":             true,
		"ws-scrcpy.css":        true,
		"ws-scrcpy.umd.js":     true,
		"ws-scrcpy.umd.js.map": true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tail := strings.TrimPrefix(r.URL.Path, "/demo/device/")
		if tail == "stream" {
			if !headerContainsToken(r.Header, "Connection", "upgrade") ||
				!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
				r.URL.Query().Get("action") != "stream" ||
				r.URL.Query().Get("udid") != streamDevice {
				http.Error(w, "stream upgrade required", http.StatusBadRequest)
				return
			}
		} else if tail == "api/capabilities" {
			// Read-only capability discovery used while the embed client starts.
		} else if tail == "api/settings/device" || tail == "api/devices/screen-state" {
			if r.URL.Query().Get("udid") != streamDevice {
				http.Error(w, "unknown demo handset", http.StatusForbidden)
				return
			}
		} else if !allowedStatic[tail] {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func setResponseBody(response *http.Response, content []byte) {
	response.Body = io.NopCloser(bytes.NewReader(content))
	response.ContentLength = int64(len(content))
	response.Header.Del("Content-Length")
	response.Header.Del("Content-Encoding")
	response.Header.Del("ETag")
}

func headerContainsToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func serveAsset(assets fs.FS, w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/demo/")
	if path == "" {
		path = "index.html"
	}
	content, err := fs.ReadFile(assets, path)
	if err != nil {
		path = "index.html"
		content, err = fs.ReadFile(assets, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if path == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		// Demo assets are intentionally served at stable human-readable paths.
		// Require revalidation so a production release cannot strand visitors on
		// an obsolete stream helper or UI bundle.
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
	http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(content))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The private viewer supplies its own same-origin framing and content
		// policy. A DENY header here would block that one deliberate iframe.
		if !strings.HasPrefix(r.URL.Path, "/demo/device/") {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; media-src 'self'; connect-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'")
			w.Header().Set("X-Frame-Options", "DENY")
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
