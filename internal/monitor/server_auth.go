package monitor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
)

const (
	// loginBodyLimit bounds the one unauthenticated JSON body in the Monitor.
	// A login payload is two short strings; anything larger is an attempt to make
	// the process allocate before it has authenticated anyone.
	loginBodyLimit = 64 << 10
	// loginMaxFailures is per (client IP, username) inside loginFailureWindow.
	// Ten wrong passwords in a quarter of an hour is far above an operator typo
	// and far below a useful online guessing rate.
	loginMaxFailures   = 10
	loginFailureWindow = 15 * time.Minute
	// monitorSessionCookie carries the Monitor JWT. It is HttpOnly so no
	// JavaScript can read it, and SameSite=Lax so a cross-site form cannot
	// submit it.
	monitorSessionCookie = "trajecta_monitor_session"
)

func authStatusAPIHandler(verifier auth.TokenVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"auth_required": verifier != nil})
	}
}
func authLoginAPIHandler(authStore *auth.Store, jwtManager *auth.JWTManager, ttl time.Duration, secureCookie bool) http.HandlerFunc {
	throttle := newLoginThrottle()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, loginBodyLimit)
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid login payload"})
			return
		}
		key := loginAttemptKey(r, req.Username)
		if wait := throttle.retryAfter(key); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds()+0.999)))
			slog.Warn("Monitor login rejected", "reason", "rate_limited", "username", strings.TrimSpace(req.Username), "remote", loginRemoteHost(r))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many login attempts; try again later"})
			return
		}
		var token auth.TokenResult
		var err error
		if jwtManager != nil {
			var principal auth.Principal
			principal, err = authStore.AuthenticatePassword(r.Context(), req.Username, req.Password)
			if err == nil {
				token, err = jwtManager.IssueToken(principal)
			}
		} else {
			token, err = authStore.Login(r.Context(), req.Username, req.Password, ttl)
		}
		if err != nil {
			throttle.recordFailure(key)
			// The username is safe to log; the password never is.
			slog.Warn("Monitor login failed", "username", strings.TrimSpace(req.Username), "remote", loginRemoteHost(r))
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
			return
		}
		throttle.reset(key)
		http.SetCookie(w, &http.Cookie{
			Name:     monitorSessionCookie,
			Value:    token.Token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secureCookie,
			MaxAge:   int(ttl.Seconds()),
		})
		writeJSON(w, http.StatusOK, loginResponse{Token: token.Token, Prefix: token.Prefix})
	}
}

// authLogoutAPIHandler clears the session cookie. It is intentionally
// unauthenticated: an expired or already-cleared cookie must still log out.
func authLogoutAPIHandler(secureCookie bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     monitorSessionCookie,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secureCookie,
			MaxAge:   -1,
		})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// loginThrottle is an in-memory, per-(IP, username) failure counter. It is not
// a distributed rate limiter; it exists so the only unauthenticated write
// endpoint cannot be used as an online password oracle or as a bcrypt CPU sink.
type loginThrottle struct {
	mu      sync.Mutex
	entries map[string]loginThrottleEntry
	max     int
	window  time.Duration
	maxKeys int
	now     func() time.Time
}

type loginThrottleEntry struct {
	failures     int
	windowStart  time.Time
	blockedUntil time.Time
}

func newLoginThrottle() *loginThrottle {
	return newLoginThrottleWithClock(time.Now)
}

func newLoginThrottleWithClock(now func() time.Time) *loginThrottle {
	if now == nil {
		now = time.Now
	}
	return &loginThrottle{
		entries: make(map[string]loginThrottleEntry),
		max:     loginMaxFailures,
		window:  loginFailureWindow,
		maxKeys: 4096,
		now:     now,
	}
}

func (t *loginThrottle) retryAfter(key string) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[key]
	if !ok {
		return 0
	}
	now := t.now()
	if entry.blockedUntil.After(now) {
		return entry.blockedUntil.Sub(now)
	}
	if now.Sub(entry.windowStart) > t.window {
		delete(t.entries, key)
	}
	return 0
}

func (t *loginThrottle) recordFailure(key string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	entry, ok := t.entries[key]
	if !ok || now.Sub(entry.windowStart) > t.window {
		entry = loginThrottleEntry{windowStart: now}
	}
	entry.failures++
	if entry.failures >= t.max {
		entry.failures = 0
		entry.windowStart = now
		entry.blockedUntil = now.Add(t.window)
	}
	t.entries[key] = entry
	if len(t.entries) > t.maxKeys {
		t.pruneLocked(now)
	}
}

func (t *loginThrottle) reset(key string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.entries, key)
	t.mu.Unlock()
}

// pruneLocked drops expired entries, and clears the map if a flood of distinct
// keys filled it. Clearing only loses the throttle, never credentials.
func (t *loginThrottle) pruneLocked(now time.Time) {
	for key, entry := range t.entries {
		if entry.blockedUntil.After(now) {
			continue
		}
		if now.Sub(entry.windowStart) > t.window {
			delete(t.entries, key)
		}
	}
	if len(t.entries) > t.maxKeys {
		t.entries = make(map[string]loginThrottleEntry)
	}
}

func loginAttemptKey(r *http.Request, username string) string {
	return loginRemoteHost(r) + "|" + strings.ToLower(strings.TrimSpace(username))
}

func loginRemoteHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// healthAPIHandler is the only side-effect-free, data-free endpoint the Monitor
// exposes without a credential. It exists so a load balancer or container
// runtime can probe liveness without holding an operator token.
func healthAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}
}
func authCheckAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
func authMeAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		writeJSON(w, http.StatusOK, meResponse{
			Username: principal.Username,
			Role:     principal.Role,
			Scope:    principal.Scope,
		})
	}
}
func authChangePasswordAPIHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok || strings.TrimSpace(principal.Username) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		var req changePasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid password payload"})
			return
		}
		if err := authStore.VerifyPassword(r.Context(), principal.Username, req.CurrentPassword); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
			return
		}
		if err := authStore.ResetPassword(r.Context(), principal.Username, req.NewPassword); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
func authTokensAPIHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListAuthTokens(w, r, authStore)
		case http.MethodPost:
			handleCreateAuthToken(w, r, authStore)
		default:
			http.NotFound(w, r)
		}
	}
}
func handleListAuthTokens(w http.ResponseWriter, r *http.Request, authStore *auth.Store) {
	if authStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || strings.TrimSpace(principal.Username) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	tokens, err := authStore.ListTokens(r.Context(), principal.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	items := make([]tokenItem, 0, len(tokens))
	for _, token := range tokens {
		items = append(items, tokenItemFromRecord(token))
	}
	writeJSON(w, http.StatusOK, tokenListResponse{
		Items: items,
		Total: len(items),
	})
}
func handleCreateAuthToken(w http.ResponseWriter, r *http.Request, authStore *auth.Store) {
	if authStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || strings.TrimSpace(principal.Username) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid token payload"})
		return
	}
	var ttl time.Duration
	if strings.TrimSpace(req.TTL) != "" {
		parsed, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ttl"})
			return
		}
		ttl = parsed
	}
	token, err := authStore.CreateToken(r.Context(), principal.Username, req.Name, req.Scope, ttl)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, createTokenResponse{Token: token.Token, Prefix: token.Prefix})
}
func authTokenDetailAPIHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		if authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok || strings.TrimSpace(principal.Username) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		tokenIDText := strings.TrimPrefix(pathClean(r.URL.Path), "/api/auth/tokens/")
		tokenID, err := strconv.Atoi(tokenIDText)
		if err != nil || tokenID <= 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
			return
		}
		deleteToken := parseBool(r.URL.Query().Get("delete"))
		if deleteToken {
			err = authStore.DeleteToken(r.Context(), principal.Username, tokenID)
		} else {
			err = authStore.RevokeToken(r.Context(), principal.Username, tokenID)
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
func monitorAuthRequired(next http.HandlerFunc, verifier auth.TokenVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Internal in-process callers (MCP tools) pass the authenticated
		// principal in the request context. External HTTP requests still need a
		// configured verifier and a valid cookie or Authorization header.
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			if verifier == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "monitor authentication is not configured"})
				return
			}
			authReq := r
			if cookie, cookieErr := r.Cookie(monitorSessionCookie); cookieErr == nil && strings.TrimSpace(cookie.Value) != "" && r.Header.Get("Authorization") == "" {
				authReq = r.Clone(r.Context())
				authReq.Header = r.Header.Clone()
				authReq.Header.Set("Authorization", "Bearer "+cookie.Value)
			}
			principal, ok = auth.VerifyRequest(authReq, verifier)
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="trajecta-monitor"`)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	}
}

// monitorAdminRequired gates a handler behind the verified principal's admin
// role. It composes monitorAuthRequired, so an unauthenticated caller gets the
// same 401 (with the same WWW-Authenticate header) as every other Monitor API
// and only an authenticated non-admin gets 403.
//
// With no verifier configured there is no identity to check, so the request is
// rejected by monitorAuthRequired with 503 before this gate runs. Tests that
// intentionally skip authentication pass auth.AllowAnonymousForTest.
func monitorAdminRequired(next http.HandlerFunc, verifier auth.TokenVerifier) http.HandlerFunc {
	gated := func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok || !strings.EqualFold(strings.TrimSpace(principal.Role), "admin") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
			return
		}
		next(w, r)
	}
	return monitorAuthRequired(gated, verifier)
}
