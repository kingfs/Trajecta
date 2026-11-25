package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagementRequestBodyLimitBoundsRead(t *testing.T) {
	t.Parallel()

	var read int64
	var readErr error
	handler := limitManagementRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, readErr = io.Copy(io.Discard, r.Body)
	}))
	body := strings.Repeat("x", int(managementRequestBodyLimit+1))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var tooLarge *http.MaxBytesError
	if !errors.As(readErr, &tooLarge) {
		t.Fatalf("request body read error = %v, want *http.MaxBytesError", readErr)
	}
	if read != managementRequestBodyLimit {
		t.Fatalf("read %d request bytes, want the configured cap %d", read, managementRequestBodyLimit)
	}
}

// TestManagementMuxKeepsProxyTokensOffMCP pins the audience split: the token an
// operator hands to an SDK is scoped to the forwarding plane, so it must not be
// accepted by the read-mostly MCP surface even though both use the same auth
// store.
func TestManagementMuxKeepsProxyTokensOffMCP(t *testing.T) {
	t.Parallel()

	st, err := newTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	cfg.MCP.Enabled = true
	cfg.MCP.Path = "/mcp"
	authStore := newTestAuthStore(t)
	defer authStore.Close()

	ctx := context.Background()
	proxyToken, err := authStore.CreateToken(ctx, "admin", "sdk", auth.ScopeProxy, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken(proxy) error = %v", err)
	}
	mcpToken, err := authStore.CreateToken(ctx, "admin", "mcp-client", auth.ScopeMCP, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken(mcp) error = %v", err)
	}

	mux := newManagementMuxWithSecretMappings(st, nil, cfg, nil, nil, nil, nil, authStore)
	postMCP := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := postMCP(proxyToken.Token); code != http.StatusUnauthorized {
		t.Fatalf("proxy-scoped token on /mcp = %d, want 401", code)
	}
	if code := postMCP(mcpToken.Token); code == http.StatusUnauthorized {
		t.Fatalf("mcp-scoped token on /mcp = 401, want the auth gate to accept it")
	}
}

func TestMCPCredentialMasksRequireAdminRoleThroughHTTP(t *testing.T) {
	t.Parallel()

	traceStore, err := newTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer traceStore.Close()
	if err := traceStore.UpsertCredentialMaskAudits([]store.CredentialMaskAuditRecord{{
		TraceID:    "trace-mcp-role",
		EventIndex: 0,
		Rule:       "test.secret",
		Original:   "sensitive-original",
		StandIn:    "sensitive-stand-in",
	}}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	authStore := newTestAuthStore(t)
	defer authStore.Close()
	if _, err := authStore.CreateUserWithRole(context.Background(), "viewer", "viewer-pass-123", auth.RoleViewer); err != nil {
		t.Fatalf("CreateUserWithRole(viewer) error = %v", err)
	}
	adminToken, err := authStore.CreateToken(context.Background(), "admin", "mcp-admin", auth.ScopeMCP, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken(admin) error = %v", err)
	}
	viewerToken, err := authStore.CreateToken(context.Background(), "viewer", "mcp-viewer", auth.ScopeMCP, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken(viewer) error = %v", err)
	}

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	cfg.MCP.Enabled = true
	cfg.MCP.Path = "/mcp"
	server := httptest.NewServer(newManagementMuxWithSecretMappings(traceStore, nil, cfg, nil, nil, nil, nil, authStore))
	defer server.Close()

	callMasks := func(token string) (*mcp.CallToolResult, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "role-test", Version: "v0.0.1"}, nil)
		session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:   server.URL + "/mcp",
			HTTPClient: &http.Client{Transport: authTransport{Token: token}},
		}, nil)
		if err != nil {
			return nil, err
		}
		defer session.Close()
		return session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "list_credential_masks",
			Arguments: map[string]any{"trace_id": "trace-mcp-role"},
		})
	}

	viewerResult, viewerErr := callMasks(viewerToken.Token)
	if viewerErr == nil && (viewerResult == nil || !viewerResult.IsError) {
		t.Fatalf("viewer MCP call = %+v, want authorization error", viewerResult)
	}
	adminResult, adminErr := callMasks(adminToken.Token)
	if adminErr != nil {
		t.Fatalf("admin MCP call error = %v", adminErr)
	}
	if adminResult == nil || adminResult.IsError {
		t.Fatalf("admin MCP result = %+v, want authorized result", adminResult)
	}
}

// TestManagementMuxEndsSessionOnPasswordChange proves the store-backed session
// verifier is wired into the real mux: a password reset invalidates the JWT that
// was issued before it, and a demotion applies on the next request.
func TestManagementMuxEndsSessionOnPasswordChange(t *testing.T) {
	t.Parallel()

	st, err := newTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	authStore := newTestAuthStore(t)
	defer authStore.Close()
	mux := newManagementMuxWithSecretMappings(st, nil, cfg, nil, nil, nil, nil, authStore)

	login := func(password string) string {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("login(%q) = %d; body=%s", password, rr.Code, rr.Body.String())
		}
		var payload struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil || payload.Token == "" {
			t.Fatalf("login(%q) payload = %s", password, rr.Body.String())
		}
		return payload.Token
	}
	check := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}

	token := login("change-me-123")
	if code := check(token); code != http.StatusOK {
		t.Fatalf("check(fresh session) = %d, want 200", code)
	}
	if err := authStore.ResetPassword(context.Background(), "admin", "new-password-123"); err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if code := check(token); code != http.StatusUnauthorized {
		t.Fatalf("check(old session after password reset) = %d, want 401", code)
	}

	fresh := login("new-password-123")
	if code := check(fresh); code != http.StatusOK {
		t.Fatalf("check(fresh session after reset) = %d, want 200", code)
	}
	if err := authStore.SetUserRole(context.Background(), "admin", auth.RoleViewer); err != nil {
		t.Fatalf("SetUserRole() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/system/runtime", nil)
	req.Header.Set("Authorization", "Bearer "+fresh)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("admin route after demotion = %d, want 403", rr.Code)
	}
}
