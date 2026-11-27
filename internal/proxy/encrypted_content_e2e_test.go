package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
)

func TestHandlerPreservesRejectedEncryptedContent(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			outputDir := t.TempDir()
			st, err := newTestStore(outputDir)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			message := `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content gAAA...n8I= could not be verified."}}`
			encoded, err := json.Marshal(map[string]any{"error": map[string]string{"message": message, "type": "api_error"}})
			if err != nil {
				t.Fatal(err)
			}
			errorBody := string(encoded) + strings.Repeat(" ", upstreamErrorSnippetBytes+100)
			requestBody := `{"model":"gpt-6.1-sol","input":[{"type":"reasoning","id":"rs_1","encrypted_content":"gAAA-rejected-n8I="},{"type":"compaction","encrypted_content":"gAAA-compaction-Og=="},{"type":"reasoning","id":"rs_2","encrypted_content":"gAAA-other-k78M"},{"role":"user","content":"hi"}],"stream":true}`
			var mu sync.Mutex
			var upstreamBodies []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					http.NotFound(w, r)
					return
				}
				body, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					t.Error(readErr)
				}
				mu.Lock()
				upstreamBodies = append(upstreamBodies, string(body))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-Id", "upstream-rejection")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, errorBody)
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Upstream.BaseURL = upstream.URL + "/v1"
			cfg.Upstream.APIType = "responses"
			cfg.Upstream.Capabilities = config.UpstreamCapabilitiesConfig{Responses: boolPtr(true)}
			cfg.Debug.OutputDir = outputDir
			handler, err := NewHandlerWithAuth(cfg, st, nil, auth.AllowAnonymousForTest())
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			for attempt := 0; attempt < 4; attempt++ {
				req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(requestBody))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer test-key")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != status || string(body) != errorBody || resp.Header.Get("X-Request-Id") != "upstream-rejection" {
					t.Fatalf("attempt %d: upstream error changed: status=%d, bytes=%d", attempt, resp.StatusCode, len(body))
				}
			}
			mu.Lock()
			bodies := append([]string(nil), upstreamBodies...)
			mu.Unlock()
			if len(bodies) != 4 {
				t.Fatalf("upstream calls = %d, want 4 without internal retries", len(bodies))
			}
			for _, body := range bodies {
				if body != requestBody {
					t.Fatal("encrypted input was rewritten or removed")
				}
			}
			for _, snapshot := range handler.router.Snapshots() {
				if snapshot.HealthState == router.HealthOpen {
					t.Fatal("request rejection opened channel breaker")
				}
				for _, health := range snapshot.ModelHealth {
					if health.HealthState == router.HealthOpen {
						t.Fatal("request rejection opened model breaker")
					}
				}
			}
			recordPath := findRecordedHTTP(t, outputDir)
			if _, err := waitForRecordedPrelude(recordPath, time.Second); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte(requestBody)) || !bytes.Contains(raw, []byte(errorBody)) || bytes.Contains(raw, []byte("reasoning.quarantined")) {
				t.Fatal("cassette did not preserve the rejected exchange")
			}
		})
	}
}
