package demoapi

import (
	"bytes"
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
	"strings"
	"sync"
	"time"
)

const (
	demoSessionCookie = "virtroid_demo_session"
	demoRequestHeader = "X-Virtroid-Demo"
	defaultSessionTTL = 8 * time.Minute
)

//go:embed web
var webAssets embed.FS

// Options configures the isolated Android stream behind the public demo page.
type Options struct {
	StreamTarget string
	StreamDevice string
	SessionTTL   time.Duration
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
	return NewWithOptions(Options{
		StreamTarget: strings.TrimSpace(os.Getenv("DEMO_STREAM_TARGET")),
		StreamDevice: strings.TrimSpace(os.Getenv("DEMO_STREAM_DEVICE")),
		SessionTTL:   ttl,
	})
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /demo", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/demo/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("GET /demo/", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(assets, w, r)
	})
	mux.HandleFunc("GET /demo/api/status", func(w http.ResponseWriter, r *http.Request) {
		available, owned, expiresAt := sessions.status(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"available":  available,
			"owned":      owned,
			"configured": options.StreamTarget != "" && options.StreamDevice != "",
			"expires_at": expiresAt,
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
		embedQuery.Set("maxFps", "24")
		embedQuery.Set("maxSize", "1080")
		embedQuery.Set("bitrate", "4000000")
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
		proxy := newStreamProxy(target)
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

func newStreamProxy(target *url.URL) http.Handler {
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
		if response.Request.URL.Path != "/embed.html" || response.StatusCode != http.StatusOK {
			return nil
		}
		content, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		needle := []byte(`<script src="ws-scrcpy.umd.js"></script>`)
		replacement := []byte(`<script src="../stream-bridge.js"></script>` + "\n    " + `<script src="ws-scrcpy.umd.js"></script>`)
		if !bytes.Contains(content, needle) {
			return io.ErrUnexpectedEOF
		}
		content = bytes.Replace(content, needle, replacement, 1)
		response.Body = io.NopCloser(bytes.NewReader(content))
		response.ContentLength = int64(len(content))
		response.Header.Del("Content-Length")
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
				r.URL.Query().Get("action") != "stream" {
				http.Error(w, "stream upgrade required", http.StatusBadRequest)
				return
			}
		} else if !allowedStatic[tail] {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
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
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
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
