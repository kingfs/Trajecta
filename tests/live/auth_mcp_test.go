package live

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Proxy and monitor authentication
// ---------------------------------------------------------------------------

func TestLiveProxyAuthRequired(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 8,
	}
	res := request(t, http.MethodPost, serverURL()+"/v1/chat/completions", body, nil)
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated proxy call: want 401, got %s", res)
	}
	if got := res.Header.Get("WWW-Authenticate"); !strings.Contains(got, "trajecta-proxy") {
		t.Errorf("WWW-Authenticate=%q, want a trajecta-proxy challenge", got)
	}
}

func TestLiveProxyRejectsInvalidToken(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 8,
	}
	res := request(t, http.MethodPost, serverURL()+"/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer llmtl_definitely-not-a-valid-token",
	})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("invalid token: want 401, got %s", res)
	}
}

func TestLiveMonitorLoginAndMe(t *testing.T) {
	requireLive(t)

	jwt := monitorToken(t)
	res := request(t, http.MethodGet, monitorURL()+"/api/auth/me", nil, map[string]string{
		"Authorization": "Bearer " + jwt,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("auth/me: want 200, got %s", res)
	}
	var me struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	decodeJSON(t, res.Body, &me)
	if me.Username == "" {
		t.Errorf("auth/me: empty username; %s", res)
	}
}

func TestLiveMonitorAPITokenLifecycle(t *testing.T) {
	requireLive(t)

	name := fmt.Sprintf("live-suite-%d", time.Now().UnixNano())

	create := request(t, http.MethodPost, monitorURL()+"/api/auth/tokens", map[string]string{"name": name}, map[string]string{
		"Authorization": "Bearer " + monitorToken(t),
	})
	if create.Status != http.StatusOK && create.Status != http.StatusCreated {
		t.Fatalf("create token: want 200/201, got %s", create)
	}
	var created struct {
		Token  string `json:"token"`
		ID     string `json:"id"`
		Prefix string `json:"prefix"`
	}
	decodeJSON(t, create.Body, &created)
	if created.Token == "" {
		t.Fatalf("create token: empty token; %s", create)
	}

	// The freshly minted token must work on the proxy entrypoint.
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: ok"}},
		"max_tokens": 64,
	}
	proxied := request(t, http.MethodPost, serverURL()+"/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer " + created.Token,
	})
	if proxied.Status != http.StatusOK {
		t.Fatalf("proxy call with a fresh token: want 200, got %s", proxied)
	}

	// Rotation: the token must show up in the list.
	list := request(t, http.MethodGet, monitorURL()+"/api/auth/tokens", nil, map[string]string{
		"Authorization": "Bearer " + monitorToken(t),
	})
	if list.Status != http.StatusOK {
		t.Fatalf("list tokens: want 200, got %s", list)
	}
	if !bytes.Contains(list.Body, []byte(name)) {
		t.Errorf("list tokens: %q missing from %s", name, truncate(string(list.Body), 400))
	}

	// Revoke it and confirm the proxy rejects it afterwards.
	if created.ID != "" {
		del := request(t, http.MethodDelete, monitorURL()+"/api/auth/tokens/"+created.ID, nil, map[string]string{
			"Authorization": "Bearer " + monitorToken(t),
		})
		if del.Status != http.StatusOK && del.Status != http.StatusNoContent {
			t.Fatalf("delete token: want 200/204, got %s", del)
		}
		after := request(t, http.MethodPost, serverURL()+"/v1/chat/completions", body, map[string]string{
			"Authorization": "Bearer " + created.Token,
		})
		if after.Status != http.StatusUnauthorized {
			t.Errorf("revoked token: want 401, got %s", after)
		}
	}
}

// ---------------------------------------------------------------------------
// MCP over streamable HTTP
// ---------------------------------------------------------------------------

// mcpCall posts one JSON-RPC request to the management MCP endpoint and returns
// the decoded JSON-RPC envelope. The SDK may answer with JSON or with an SSE
// stream, so both encodings are accepted.
func mcpCall(t *testing.T, method string, params any) (int, map[string]any, []byte) {
	t.Helper()

	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		payload["params"] = params
	}
	res := request(t, http.MethodPost, monitorURL()+"/mcp", payload, map[string]string{
		"Authorization": "Bearer " + proxyToken(t),
		"Accept":        "application/json, text/event-stream",
	})

	raw := res.Body
	if strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		extracted, ok := firstSSEData(raw)
		if !ok {
			t.Fatalf("mcp %s: SSE response had no data frame: %s", method, truncate(string(raw), 400))
		}
		raw = extracted
	}

	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("mcp %s: response is not JSON-RPC (%v): %s", method, err, truncate(string(raw), 400))
	}
	return res.Status, envelope, raw
}

func firstSSEData(raw []byte) ([]byte, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), true
		}
	}
	return nil, false
}

func TestLiveMCPUnauthorized(t *testing.T) {
	requireLive(t)

	res := request(t, http.MethodPost, monitorURL()+"/mcp", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	}, map[string]string{"Accept": "application/json, text/event-stream"})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP: want 401, got %s", res)
	}
}

func TestLiveMCPInitializeAndToolsList(t *testing.T) {
	requireLive(t)

	status, envelope, raw := mcpCall(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "trajecta-live-suite", "version": "1.0.0"},
	})
	if status != http.StatusOK {
		t.Fatalf("mcp initialize: want 200, got %d (%s)", status, truncate(string(raw), 300))
	}
	if envelope["error"] != nil {
		t.Fatalf("mcp initialize returned an error: %s", truncate(string(raw), 400))
	}
	result, _ := envelope["result"].(map[string]any)
	if result == nil {
		t.Fatalf("mcp initialize: missing result: %s", truncate(string(raw), 400))
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo == nil || strings.TrimSpace(fmt.Sprint(serverInfo["name"])) == "" {
		t.Errorf("mcp initialize: missing serverInfo.name: %s", truncate(string(raw), 400))
	}

	_, listEnvelope, listRaw := mcpCall(t, "tools/list", map[string]any{})
	listResult, _ := listEnvelope["result"].(map[string]any)
	if listResult == nil {
		t.Fatalf("mcp tools/list: missing result: %s", truncate(string(listRaw), 400))
	}
	tools, _ := listResult["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("mcp tools/list: no tools advertised: %s", truncate(string(listRaw), 400))
	}

	names := map[string]bool{}
	for _, entry := range tools {
		if obj, ok := entry.(map[string]any); ok {
			names[fmt.Sprint(obj["name"])] = true
		}
	}
	for _, want := range []string{"list_traces", "get_trace", "query_failures", "reanalyze_trace"} {
		if !names[want] {
			t.Errorf("mcp tools/list: expected tool %q, got %d tools", want, len(names))
		}
	}
	if len(names) < 20 {
		t.Errorf("mcp tools/list: advertised %d tools, docs claim 21", len(names))
	}
}

func TestLiveMCPToolsCallListTraces(t *testing.T) {
	requireLive(t)

	// Make sure there is at least one trace.
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "mcp-tool-call-probe"}},
		"max_tokens": 32,
	}
	if res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil); res.Status != http.StatusOK {
		t.Fatalf("mcp seed request: want 200, got %s", res)
	}

	_, envelope, raw := mcpCall(t, "tools/call", map[string]any{
		"name":      "list_traces",
		"arguments": map[string]any{"limit": 1},
	})
	if envelope["error"] != nil {
		t.Fatalf("mcp tools/call list_traces returned an error: %s", truncate(string(raw), 500))
	}
	result, _ := envelope["result"].(map[string]any)
	if result == nil {
		t.Fatalf("mcp tools/call: missing result: %s", truncate(string(raw), 500))
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Errorf("mcp tools/call list_traces: empty content: %s", truncate(string(raw), 500))
	}
}
