package operatorapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

const sessionCookieName = "virtroid_operator_session"

//go:embed web
var webAssets embed.FS

type snapshotStore interface {
	OperatorSnapshot(context.Context) (store.OperatorSnapshot, error)
}

type API struct {
	cfg     config.ServerConfig
	store   snapshotStore
	assets  fs.FS
	limiter *loginLimiter
	now     func() time.Time
}

func New(cfg config.ServerConfig, st snapshotStore) http.Handler {
	assets, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	a := &API{
		cfg:     cfg,
		store:   st,
		assets:  assets,
		limiter: newLoginLimiter(cfg.OperatorLoginRateLimitPerMinute),
		now:     time.Now,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /operator/v1/session", a.createSession)
	mux.HandleFunc("GET /operator/v1/session", a.getSession)
	mux.HandleFunc("DELETE /operator/v1/session", a.deleteSession)
	mux.HandleFunc("GET /operator/v1/overview", a.requireSession(a.overview))
	mux.HandleFunc("GET /operator", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/operator/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("GET /operator/", a.serveApp)
	return securityHeaders(mux)
}

func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.allow(clientIP(r, a.cfg.TrustProxyHeaders), a.now()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many login attempts"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.Token) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an operator access token is required"})
		return
	}
	expected := sha256.Sum256([]byte(a.cfg.OperatorConsoleToken))
	provided := sha256.Sum256([]byte(strings.TrimSpace(body.Token)))
	if a.cfg.OperatorConsoleToken == "" || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid operator credentials"})
		return
	}

	ttl := a.cfg.OperatorSessionTTL
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 8 * time.Hour
	}
	expires := a.now().UTC().Add(ttl)
	value, err := a.signSession(expires)
	if err != nil {
		log.Printf("operator session creation failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "operator session unavailable"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/operator",
		Expires:  expires,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   !strings.EqualFold(a.cfg.AppEnv, "development"),
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusCreated, sessionResponse{Authenticated: true, ExpiresAt: expires})
}

func (a *API) getSession(w http.ResponseWriter, r *http.Request) {
	expires, ok := a.validSession(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "operator authentication required"})
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{Authenticated: true, ExpiresAt: expires})
}

func (a *API) deleteSession(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/operator",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !strings.EqualFold(a.cfg.AppEnv, "development"),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	if a.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operator data unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	snapshot, err := a.store.OperatorSnapshot(ctx)
	if err != nil {
		log.Printf("operator overview query failed")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operator data unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, buildOverview(a.cfg, snapshot, a.now().UTC()))
}

func (a *API) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.validSession(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "operator authentication required"})
			return
		}
		next(w, r)
	}
}

func (a *API) serveApp(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/operator/")
	if path == "" {
		path = "index.html"
	}
	content, err := fs.ReadFile(a.assets, path)
	if err != nil {
		path = "index.html"
		content, err = fs.ReadFile(a.assets, path)
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

type sessionResponse struct {
	Authenticated bool      `json:"authenticated"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

func (a *API) signSession(expires time.Time) (string, error) {
	nonce := make([]byte, 18)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := strconv.FormatInt(expires.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, []byte(a.cfg.OperatorConsoleToken))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (a *API) validSession(r *http.Request) (time.Time, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return time.Time{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || a.cfg.OperatorConsoleToken == "" {
		return time.Time{}, false
	}
	payload := parts[0] + "." + parts[1]
	provided, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return time.Time{}, false
	}
	mac := hmac.New(sha256.New, []byte(a.cfg.OperatorConsoleToken))
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(mac.Sum(nil), provided) {
		return time.Time{}, false
	}
	expiresUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	expires := time.Unix(expiresUnix, 0).UTC()
	if !expires.After(a.now().UTC()) {
		return time.Time{}, false
	}
	return expires, true
}

type overviewResponse struct {
	GeneratedAt string             `json:"generatedAt"`
	Environment string             `json:"environment"`
	ReadOnly    bool               `json:"readOnly"`
	Status      string             `json:"status"`
	Headline    string             `json:"headline"`
	Subline     string             `json:"subline"`
	Metrics     []metricResponse   `json:"metrics"`
	Runtimes    []runtimeResponse  `json:"runtimes"`
	Incidents   []incidentResponse `json:"incidents"`
	Hygiene     []hygieneResponse  `json:"hygiene"`
	Activity    []activityResponse `json:"activity"`
	Fleet       fleetResponse      `json:"fleet"`
	Deployment  deploymentResponse `json:"deployment"`
}

type metricResponse struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail"`
	Trend  string `json:"trend,omitempty"`
	Tone   string `json:"tone"`
}

type runtimeResponse struct {
	ID             string  `json:"id"`
	ShortID        string  `json:"shortId"`
	Name           string  `json:"name"`
	AccountID      string  `json:"accountId"`
	AccountShortID string  `json:"accountShortId"`
	Status         string  `json:"status"`
	DesiredState   string  `json:"desiredState"`
	Connection     string  `json:"connection"`
	Host           string  `json:"host"`
	Persona        int     `json:"persona"`
	AndroidVersion string  `json:"androidVersion"`
	DeviceProfile  string  `json:"deviceProfile"`
	ActiveSession  bool    `json:"activeSession"`
	SessionAge     string  `json:"sessionAge,omitempty"`
	CPU            float64 `json:"cpu"`
	Memory         string  `json:"memory"`
	Storage        string  `json:"storage"`
	Snapshot       string  `json:"snapshot"`
	CleanupPending bool    `json:"cleanupPending"`
	Updated        string  `json:"updated"`
	LastError      string  `json:"lastError,omitempty"`
}

type incidentResponse struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Severity string `json:"severity"`
	Age      string `json:"age"`
	Source   string `json:"source"`
}

type hygieneResponse struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Status string `json:"status"`
}

type activityResponse struct {
	Label      string `json:"label"`
	Sessions   int    `json:"sessions"`
	Operations int    `json:"operations"`
}

type fleetResponse struct {
	Ready     int    `json:"ready"`
	Total     int    `json:"total"`
	Name      string `json:"name"`
	Heartbeat string `json:"heartbeat"`
	Capacity  int    `json:"capacity"`
}

type deploymentResponse struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Schema   string `json:"schema"`
	Deployed string `json:"deployed"`
}

func buildOverview(cfg config.ServerConfig, snapshot store.OperatorSnapshot, now time.Time) overviewResponse {
	status := "healthy"
	if !snapshot.Readiness.Ready || snapshot.CleanupPending > 0 || hasImportantIncident(snapshot.Incidents) {
		status = "degraded"
	}
	if snapshot.Readiness.ReadyNodes == 0 && snapshot.Readiness.ObservedNodes > 0 {
		status = "critical"
	}
	hostName := "No observed node"
	heartbeat := "No heartbeat"
	if len(snapshot.Hosts) > 0 {
		hostName = snapshot.Hosts[0].Name
		heartbeat = relativeAge(now, snapshot.Hosts[0].LastHeartbeatAt)
	}
	result := overviewResponse{
		GeneratedAt: snapshot.GeneratedAt.UTC().Format(time.RFC3339),
		Environment: strings.ToLower(defaultString(cfg.AppEnv, "production")),
		ReadOnly:    true,
		Status:      status,
		Headline:    map[string]string{"healthy": "Everything is in orbit", "degraded": "A few signals need attention", "critical": "Control plane needs attention"}[status],
		Subline:     "Live, sanitized telemetry from the control plane. Privileged actions remain locked.",
		Runtimes:    []runtimeResponse{},
		Incidents:   []incidentResponse{},
		Activity:    []activityResponse{},
		Metrics: []metricResponse{
			{Label: "Active runtimes", Value: strconv.Itoa(snapshot.RunningRuntimes), Detail: fmt.Sprintf("%d total", snapshot.TotalRuntimes), Tone: "mint"},
			{Label: "Live sessions", Value: strconv.Itoa(snapshot.LiveSessions), Detail: "authenticated viewers", Tone: "neutral"},
			{Label: "Active accounts", Value: strconv.Itoa(snapshot.ActiveAccounts), Detail: "non-deleted", Tone: "neutral"},
			{Label: "Cleanup queue", Value: strconv.Itoa(snapshot.CleanupPending), Detail: "pending sanitations", Tone: ternary(snapshot.CleanupPending > 0, "amber", "mint")},
		},
		Hygiene: []hygieneResponse{
			{Label: "Referential integrity", Detail: fmt.Sprintf("%d orphaned runtime records", snapshot.OrphanedRuntimes), Status: ternary(snapshot.OrphanedRuntimes > 0, "attention", "passed")},
			{Label: "Expired live sessions", Detail: fmt.Sprintf("%d records await reaping", snapshot.ExpiredSessions), Status: ternary(snapshot.ExpiredSessions > 0, "attention", "passed")},
			{Label: "Runtime cleanup", Detail: fmt.Sprintf("%d runtimes pending sanitization", snapshot.CleanupPending), Status: ternary(snapshot.CleanupPending > 0, "attention", "passed")},
		},
		Fleet: fleetResponse{
			Ready: snapshot.Readiness.ReadyNodes, Total: snapshot.Readiness.ObservedNodes,
			Name: hostName, Heartbeat: heartbeat, Capacity: snapshot.TotalRuntimes,
		},
		Deployment: deploymentResponse{
			Version: "sha-" + shortID(cfg.ReleaseSourceSHA), Commit: defaultString(cfg.ReleaseSourceSHA, "unavailable"),
			Schema: defaultString(cfg.ReleaseSchemaVersion, "unavailable"), Deployed: "from verified release identity",
		},
	}
	for _, runtime := range snapshot.Runtimes {
		state := "stopped"
		if runtime.CleanupPending || runtime.LastError != nil {
			state = "attention"
		} else if runtime.Status == "running" {
			state = "running"
		}
		cpu := 0.0
		if runtime.LoadAverage != nil {
			cpu = math.Min(100, math.Max(0, *runtime.LoadAverage*100))
		}
		entry := runtimeResponse{
			ID: runtime.ID, ShortID: shortID(runtime.ID), Name: runtime.Name,
			AccountID: runtime.AccountID, AccountShortID: shortID(runtime.AccountID),
			Status: state, DesiredState: normalizedState(runtime.DesiredState), Connection: normalizedConnection(runtime.ConnectionStatus),
			Host: defaultString(runtime.HostID, "unassigned"), Persona: runtime.PersonaVersion, AndroidVersion: runtime.AndroidVersion,
			DeviceProfile: fmt.Sprintf("%d x %d @ %ddpi", runtime.WidthPx, runtime.HeightPx, runtime.DensityDPI),
			ActiveSession: runtime.ActiveSessionAt != nil, CPU: math.Round(cpu*10) / 10,
			Memory: "not reported", Storage: runtime.BlobStoreKind, Snapshot: "not available",
			CleanupPending: runtime.CleanupPending, Updated: relativeAge(now, runtime.UpdatedAt),
		}
		if runtime.ActiveSessionAt != nil {
			entry.SessionAge = relativeAge(now, *runtime.ActiveSessionAt)
		}
		if runtime.BlobLastSnapshotAt != nil {
			entry.Snapshot = relativeAge(now, *runtime.BlobLastSnapshotAt)
		}
		if runtime.LastError != nil {
			entry.LastError = "The node reported a reconciliation error. Review protected backend logs for details."
		}
		result.Runtimes = append(result.Runtimes, entry)
	}
	for _, incident := range snapshot.Incidents {
		severity := normalizeSeverity(incident.Priority)
		result.Incidents = append(result.Incidents, incidentResponse{
			ID: fmt.Sprintf("SEC-%d", incident.ID), Title: incident.Rule,
			Detail:   "A security signal was recorded. Raw event content remains in protected backend logs.",
			Severity: severity, Age: relativeAge(now, incident.EventTime), Source: incident.Source + " · " + incident.NodeID,
		})
	}
	if len(result.Incidents) == 0 {
		result.Incidents = append(result.Incidents, incidentResponse{
			ID: "SYSTEM", Title: "No recent security signals", Detail: "The protected security-event queue is clear.",
			Severity: "info", Age: "now", Source: "control plane",
		})
	}
	for _, point := range snapshot.Activity {
		result.Activity = append(result.Activity, activityResponse{Label: point.Bucket.Local().Format("15:04"), Sessions: point.Sessions, Operations: point.Operations})
	}
	return result
}

func normalizeSeverity(value string) string {
	v := strings.ToLower(value)
	if strings.Contains(v, "critical") || strings.Contains(v, "emergency") || strings.Contains(v, "alert") {
		return "critical"
	}
	if strings.Contains(v, "warn") || strings.Contains(v, "error") || strings.Contains(v, "high") {
		return "warning"
	}
	return "info"
}

func hasImportantIncident(incidents []store.OperatorIncident) bool {
	for _, incident := range incidents {
		if normalizeSeverity(incident.Priority) != "info" {
			return true
		}
	}
	return false
}

func normalizedState(value string) string {
	if strings.EqualFold(value, "running") {
		return "running"
	}
	return "stopped"
}

func normalizedConnection(value string) string {
	if strings.EqualFold(value, "online") {
		return "online"
	}
	return "offline"
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func shortID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func relativeAge(now, then time.Time) string {
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func ternary(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return forwarded
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

type loginLimiter struct {
	mu      sync.Mutex
	limit   int
	attempt map[string][]time.Time
}

func newLoginLimiter(limit int) *loginLimiter {
	if limit <= 0 {
		limit = 5
	}
	return &loginLimiter{limit: limit, attempt: make(map[string][]time.Time)}
}

func (l *loginLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	kept := l.attempt[key][:0]
	for _, attempt := range l.attempt[key] {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	if len(kept) >= l.limit {
		l.attempt[key] = kept
		return false
	}
	l.attempt[key] = append(kept, now)
	return true
}
