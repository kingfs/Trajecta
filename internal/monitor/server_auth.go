package monitor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
)

func authStatusAPIHandler(verifier auth.TokenVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"auth_required": verifier != nil})
	}
}
func authLoginAPIHandler(authStore *auth.Store, jwtManager *auth.JWTManager, ttl time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
			return
		}
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid login payload"})
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
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
			return
		}
		writeJSON(w, http.StatusOK, loginResponse{Token: token.Token, Prefix: token.Prefix})
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
	if verifier == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		authReq := r
		if token := strings.TrimSpace(r.URL.Query().Get("access_token")); token != "" && r.Header.Get("Authorization") == "" && allowMonitorQueryAccessToken(r) {
			authReq = r.Clone(r.Context())
			authReq.Header = r.Header.Clone()
			authReq.Header.Set("Authorization", "Bearer "+token)
		}
		principal, ok := auth.VerifyRequest(authReq, verifier)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="trajecta-monitor"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	}
}
func allowMonitorQueryAccessToken(r *http.Request) bool {
	return r.Method == http.MethodGet && pathClean(r.URL.Path) == "/api/events/stream"
}
