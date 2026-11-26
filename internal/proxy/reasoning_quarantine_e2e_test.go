package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

func TestHandlerRetriesInvalidEncryptedContentWithoutTheOffendingReasoningItem(t *testing.T) {
	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	const badBlob = "AAAA-QUARANTINE-BLOB-ZZZZ"
	var mu sync.Mutex
	var upstreamBodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstreamBodies = append(upstreamBodies, string(body))
		call := len(upstreamBodies)
		mu.Unlock()
		if call == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			message := `{"error":{"code":"invalid_encrypted_content","message":"会话上下文校验失败，请携带完整历史重试或新建会话。原始错误: The encrypted content AAAA...ZZZZ could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error"},"type":"error"}`
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{"message": message, "type": "api_error"},
			})
			return
		}
		if strings.Contains(string(body), "QUARANTINE") {
			t.Errorf("upstream request %d still carried the quarantined item: %s", call, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[]}}\n\n")
	}))
	defer upstream.Close()

	responsesEnabled := true
	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstream.URL + "/v1"
	cfg.Upstream.APIType = "responses"
	cfg.Upstream.Capabilities = config.UpstreamCapabilitiesConfig{Responses: &responsesEnabled}
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true
	handler, err := NewHandlerWithAuth(cfg, st, nil, nil)
	if err != nil {
		t.Fatalf("NewHandlerWithAuth() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	requestBody := `{"model":"gpt-6.1-sol","input":[` +
		`{"type":"reasoning","id":"rs_bad","encrypted_content":"` + badBlob + `"},` +
		`{"role":"user","content":"hi"}],"stream":true}`
	post := func() *http.Response {
		req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/responses", bytes.NewBufferString(requestBody))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")
		resp, err := proxyServer.Client().Do(req)
		if err != nil {
			t.Fatalf("client.Do() error = %v", err)
		}
		return resp
	}

	first := post()
	if first.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(first.Body)
		first.Body.Close()
		t.Fatalf("first status = %d, want 200 after the quarantine retry; body=%s", first.StatusCode, body)
	}
	_, _ = io.Copy(io.Discard, first.Body)
	first.Body.Close()

	second := post()
	if second.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(second.Body)
		second.Body.Close()
		t.Fatalf("second status = %d, want 200 with the proactive drop; body=%s", second.StatusCode, body)
	}
	_, _ = io.Copy(io.Discard, second.Body)
	second.Body.Close()

	mu.Lock()
	bodies := append([]string(nil), upstreamBodies...)
	mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("upstream calls = %d, want failed attempt + retry + proactive request", len(bodies))
	}
	if !strings.Contains(bodies[0], "QUARANTINE") {
		t.Fatalf("first upstream attempt did not carry the offending item: %s", bodies[0])
	}
	for i, body := range bodies[1:] {
		if strings.Contains(body, "QUARANTINE") {
			t.Fatalf("upstream request %d still carried the quarantined item: %s", i+2, body)
		}
		if !strings.Contains(body, `"content":"hi"`) {
			t.Fatalf("upstream request %d lost the rest of the conversation: %s", i+2, body)
		}
	}
}
