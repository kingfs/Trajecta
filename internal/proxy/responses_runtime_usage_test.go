package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestLocalResponsesRuntimeAsksForStreamedUsage pins the usage the local runtime path reports.
//
// OpenAI-compatible upstreams only send the usage chunk of a stream when the request sets
// `stream_options.include_usage`, which is why the proxy's pass-through path injects it for every
// streamed chat completion. The local Responses runtime builds its own internal chat request, and
// it did not: a streamed `/v1/responses` translated by the runtime reported zero usage while the
// same deployment reported the upstream's numbers through a native pass-through. The upstream here
// behaves like the real ones - it sends usage only when asked - so the completed event has to carry
// the numbers the upstream reported, and a non-streaming request must not grow a stream_options
// field it does not need.
func TestLocalResponsesRuntimeAsksForStreamedUsage(t *testing.T) {
	var (
		mu      sync.Mutex
		streams []streamedChatRequest
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Stream        bool `json:"stream"`
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		streams = append(streams, streamedChatRequest{
			stream:       payload.Stream,
			includeUsage: payload.StreamOptions != nil && payload.StreamOptions.IncludeUsage,
		})
		mu.Unlock()

		if !payload.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write := func(chunk string) {
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`{"id":"c1","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`)
		write(`{"id":"c1","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		if payload.StreamOptions != nil && payload.StreamOptions.IncludeUsage {
			write(`{"id":"c1","object":"chat.completion.chunk","model":"chat-model","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
		}
		write(`[DONE]`)
	}))
	t.Cleanup(upstream.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "chat-only",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"chat-model"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = t.TempDir()
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	srv := newStubProxyServer(t, handler)

	t.Run("streaming translation reports the upstream usage", func(t *testing.T) {
		mu.Lock()
		streams = nil
		mu.Unlock()

		resp, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"chat-model","input":"hi","stream":true}`))
		if err != nil {
			t.Fatalf("POST /v1/responses error = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/responses status = %d, want 200", resp.StatusCode)
		}

		usage, completed := completedResponseUsage(t, resp.Body)
		if completed != 1 {
			t.Fatalf("response.completed events = %d, want 1", completed)
		}
		if usage.InputTokens != 7 || usage.OutputTokens != 5 || usage.TotalTokens != 12 {
			t.Fatalf("response.completed usage = %+v, want the 7/5/12 the upstream reported: an upstream only sends usage in a stream when the request asks for it with stream_options.include_usage", usage)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(streams) == 0 {
			t.Fatalf("the upstream received no chat request")
		}
		for i, request := range streams {
			if !request.stream || !request.includeUsage {
				t.Fatalf("chat request %d = stream:%v include_usage:%v, want stream:true include_usage:true", i, request.stream, request.includeUsage)
			}
		}
	})

	t.Run("non-streaming request does not ask for stream options", func(t *testing.T) {
		mu.Lock()
		streams = nil
		mu.Unlock()

		resp, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"chat-model","input":"hi"}`))
		if err != nil {
			t.Fatalf("POST /v1/responses error = %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/responses status = %d, want 200, body = %s", resp.StatusCode, body)
		}
		var payload struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode body %s: %v", body, err)
		}
		if payload.Usage.InputTokens != 7 || payload.Usage.OutputTokens != 5 || payload.Usage.TotalTokens != 12 {
			t.Fatalf("non-streaming usage = %+v, want 7/5/12", payload.Usage)
		}

		mu.Lock()
		defer mu.Unlock()
		for i, request := range streams {
			if request.stream || request.includeUsage {
				t.Fatalf("chat request %d = stream:%v include_usage:%v, want stream:false include_usage:false: stream_options only belongs on a streamed request", i, request.stream, request.includeUsage)
			}
		}
	})
}

type streamedChatRequest struct {
	stream       bool
	includeUsage bool
}

func completedResponseUsage(t *testing.T, body io.Reader) (struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}, int) {
	t.Helper()
	usage := struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	}{}
	completed := 0
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type     string `json:"type"`
			Response struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					TotalTokens  int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if event.Type == "response.completed" {
			completed++
			usage = event.Response.Usage
		}
	}
	return usage, completed
}
