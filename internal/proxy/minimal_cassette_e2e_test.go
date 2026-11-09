package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
)

func TestMinimalCassetteConfigReachesTheRecordedPrelude(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-min","model":"gpt-5","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstream.URL + "/v1"
	cfg.Upstream.APIType = "chat_completions"
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true
	cfg.Trace.MinimalCassette = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	recordPath := findRecordedHTTP(t, outputDir)
	parsed, err := waitForRecordedPrelude(recordPath, 2*time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude() error = %v", err)
	}
	if len(parsed.Events) != 0 {
		t.Fatalf("minimal cassette events = %+v", parsed.Events)
	}
	if parsed.Header.Meta.SelectedUpstreamID != "" || parsed.Header.Meta.RoutingPolicy != "" {
		t.Fatalf("routing metadata leaked into minimal prelude: %+v", parsed.Header.Meta)
	}
	if parsed.Header.Usage.TotalTokens != 0 {
		t.Fatalf("usage leaked into minimal prelude: %+v", parsed.Header.Usage)
	}
	if parsed.Header.Meta.Model != "gpt-5" || parsed.Header.Meta.StatusCode != 200 {
		t.Fatalf("parse metadata lost: %+v", parsed.Header.Meta)
	}
	if parsed.Header.Layout.ResBodyLen == 0 {
		t.Fatalf("response layout lost: %+v", parsed.Header.Layout)
	}
}
