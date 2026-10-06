package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func TestLiveRoutingInspect(t *testing.T) {
	requireLive(t)

	// A chat-completions request must only consider OpenAI-compatible targets.
	chat := monitorRequest(t, http.MethodPost, "/api/routing/inspect", map[string]any{
		"endpoint": "/v1/chat/completions",
		"model":    modelName(),
	})
	if chat.Status != http.StatusOK {
		t.Fatalf("routing inspect (chat): want 200, got %s", chat)
	}
	var chatBody struct {
		Error  string `json:"error"`
		Result *struct {
			Plan *struct {
				SelectedChannelID string `json:"selected_channel_id"`
				Reason            string `json:"reason"`
			} `json:"plan"`
			Candidates []struct {
				ChannelID  string `json:"channel_id"`
				Selectable bool   `json:"selectable"`
				Reason     string `json:"reason"`
			} `json:"candidates"`
		} `json:"result"`
	}
	decodeJSON(t, chat.Body, &chatBody)
	if chatBody.Error != "" {
		t.Fatalf("routing inspect (chat) returned an error: %s", chatBody.Error)
	}
	if chatBody.Result == nil || chatBody.Result.Plan == nil {
		t.Fatalf("routing inspect (chat): no plan: %s", truncate(string(chat.Body), 500))
	}
	for _, c := range chatBody.Result.Candidates {
		if c.ChannelID == "baizhi-anthropic" && c.Selectable {
			t.Errorf("anthropic channel is selectable for /v1/chat/completions: %+v", c)
		}
	}
	if got := chatBody.Result.Plan.SelectedChannelID; got != "baizhi-openai" {
		t.Errorf("routing inspect (chat): selected_channel_id=%q, want baizhi-openai", got)
	}

	// A messages request must only consider the Anthropic target.
	msg := monitorRequest(t, http.MethodPost, "/api/routing/inspect", map[string]any{
		"endpoint": "/v1/messages",
		"model":    modelName(),
	})
	if msg.Status != http.StatusOK {
		t.Fatalf("routing inspect (messages): want 200, got %s", msg)
	}
	var msgBody struct {
		Error  string `json:"error"`
		Result *struct {
			Plan *struct {
				SelectedChannelID string `json:"selected_channel_id"`
			} `json:"plan"`
		} `json:"result"`
	}
	decodeJSON(t, msg.Body, &msgBody)
	if msgBody.Error != "" {
		t.Fatalf("routing inspect (messages) returned an error: %s", msgBody.Error)
	}
	if msgBody.Result == nil || msgBody.Result.Plan == nil {
		t.Fatalf("routing inspect (messages): no plan: %s", truncate(string(msg.Body), 500))
	}
	if got := msgBody.Result.Plan.SelectedChannelID; got != "baizhi-anthropic" {
		t.Errorf("routing inspect (messages): selected_channel_id=%q, want baizhi-anthropic", got)
	}
}

func TestLiveRoutingSummary(t *testing.T) {
	requireLive(t)

	res := monitorGET(t, "/api/routing/summary")
	if res.Status != http.StatusOK {
		t.Fatalf("routing summary: want 200, got %s", res)
	}
	var parsed map[string]any
	decodeJSON(t, res.Body, &parsed)
	if len(parsed) == 0 {
		t.Errorf("routing summary returned an empty object")
	}
}

// ---------------------------------------------------------------------------
// Sessions and sticky routing
// ---------------------------------------------------------------------------

func TestLiveSessionGrouping(t *testing.T) {
	requireLive(t)

	sessionID := fmt.Sprintf("live-session-%d", time.Now().UnixNano())
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "session grouping probe"}},
		"max_tokens": 64,
	}
	res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"X-Claude-Code-Session-Id": sessionID,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("session probe: want 200, got %s", res)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		list := monitorGET(t, "/api/sessions?limit=50")
		if list.Status == http.StatusOK {
			var parsed struct {
				Items []struct {
					SessionID     string `json:"session_id"`
					SessionSource string `json:"session_source"`
					RequestCount  int    `json:"request_count"`
				} `json:"items"`
			}
			if err := json.Unmarshal(list.Body, &parsed); err == nil {
				for _, item := range parsed.Items {
					if item.SessionID != sessionID {
						continue
					}
					if item.RequestCount < 1 {
						t.Errorf("session %s: request_count=%d", sessionID, item.RequestCount)
					}
					if !strings.Contains(item.SessionSource, "x_claude_code_session_id") {
						t.Errorf("session %s: session_source=%q, want the x_claude_code_session_id header source", sessionID, item.SessionSource)
					}
					detail := monitorGET(t, "/api/sessions/"+sessionID)
					if detail.Status != http.StatusOK {
						t.Errorf("session detail %s: want 200, got %s", sessionID, detail)
					}
					return
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Errorf("session %s never appeared in /api/sessions", sessionID)
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestLiveConcurrentChatRequests(t *testing.T) {
	requireLive(t)

	const workers = 8
	before := traceCount(t)
	token := proxyToken(t)
	client := &http.Client{Timeout: 180 * time.Second}

	var wg sync.WaitGroup
	errs := make([]error, workers)
	ids := make([]string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf("concurrency-%d-%d", time.Now().UnixNano(), i)
			payload, err := json.Marshal(map[string]any{
				"model":      modelName(),
				"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: ok. " + marker}},
				"max_tokens": 256,
			})
			if err != nil {
				errs[i] = fmt.Errorf("worker %d: marshal: %w", i, err)
				return
			}
			req, err := http.NewRequest(http.MethodPost, serverURL()+"/v1/chat/completions", bytes.NewReader(payload))
			if err != nil {
				errs[i] = fmt.Errorf("worker %d: build request: %w", i, err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := client.Do(req)
			if err != nil {
				errs[i] = fmt.Errorf("worker %d: %w", i, err)
				return
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				errs[i] = fmt.Errorf("worker %d: read body: %w", i, err)
				return
			}
			if resp.StatusCode != http.StatusOK {
				errs[i] = fmt.Errorf("worker %d: status %d: %s", i, resp.StatusCode, truncate(string(raw), 200))
				return
			}
			var parsed struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				errs[i] = fmt.Errorf("worker %d: decode: %w", i, err)
				return
			}
			ids[i] = parsed.ID
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	seen := map[string]int{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		seen[id]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("response id %q appeared %d times across concurrent requests", id, n)
		}
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if traceCount(t) >= before+workers {
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("trace count did not grow by %d within 30s (before=%d, after=%d)", workers, before, traceCount(t))
}

func traceCount(t *testing.T) int {
	t.Helper()
	res := monitorGET(t, "/api/traces?limit=1")
	if res.Status != http.StatusOK {
		t.Fatalf("/api/traces: want 200, got %s", res)
	}
	var parsed struct {
		Total int `json:"total"`
	}
	decodeJSON(t, res.Body, &parsed)
	return parsed.Total
}
