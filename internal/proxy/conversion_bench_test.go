package proxy

// End-to-end proxy benchmarks for the conversion hot path: the same client
// request through the whole handler (auth, routing, conversion, recording) with
// a local stub upstream, once per conversion engine, plus a native passthrough
// baseline that runs the same handler without conversion.
//
//	go test -run '^$' -bench 'BenchmarkHandlerConversion' -benchmem -count 6 ./internal/proxy

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/benchcorpus"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
)

// conversionBenchHandler builds a handler whose only upstream serves one wire
// protocol. A chat entrypoint then has to be converted to it.
func conversionBenchHandler(b *testing.B, engine, upstreamAPIType, upstreamBody string) *httptest.Server {
	b.Helper()
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(oldLogger) })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	b.Cleanup(upstream.Close)

	outputDir := b.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })

	enabled := true
	protocolFamily := "openai_compatible"
	providerPreset := "openai"
	if upstreamAPIType == "messages" {
		protocolFamily = "anthropic_messages"
		providerPreset = "anthropic"
	}
	cfg := &config.Config{}
	cfg.Upstreams = []config.UpstreamTargetConfig{
		{
			ID:             "bench-upstream",
			Enabled:        &enabled,
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"bench-model"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ProviderPreset: providerPreset,
				APIType:        upstreamAPIType,
				ProtocolFamily: protocolFamily,
			},
		},
	}
	cfg.Router.Selection.ProtocolConversion = true
	cfg.Router.Selection.ProtocolConversionEngine = engine
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true

	handler, err := NewHandlerWithAuth(cfg, st, nil, nil)
	if err != nil {
		b.Fatalf("NewHandlerWithAuth() error = %v", err)
	}
	server := httptest.NewServer(handler)
	b.Cleanup(server.Close)
	return server
}

const (
	benchChatBody     = `{"model":"bench-model","max_tokens":32,"messages":[{"role":"user","content":"hello conversion benchmark"}]}`
	benchMessagesBody = `{"id":"msg_bench","type":"message","role":"assistant","model":"bench-model","content":[{"type":"text","text":"bench answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
	benchChatUpstream = `{"id":"chatcmpl-bench","object":"chat.completion","created":1,"model":"bench-model","choices":[{"index":0,"message":{"role":"assistant","content":"bench answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
)

func runBenchRequests(b *testing.B, server *httptest.Server, path, body string) {
	b.Helper()
	client := server.Client()
	payload := []byte(body)
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			b.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			b.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, respBody)
		}
	}
}

// BenchmarkHandlerConversion measures the whole request path when the client
// speaks chat and the only upstream speaks Messages, so both engines convert
// the request and the response.
func BenchmarkHandlerConversion(b *testing.B) {
	for _, engine := range []string{"legacy", "xir"} {
		b.Run(engine, func(b *testing.B) {
			server := conversionBenchHandler(b, engine, "messages", benchMessagesBody)
			b.ReportAllocs()
			b.ResetTimer()
			runBenchRequests(b, server, "/v1/chat/completions", benchChatBody)
		})
	}
}

// BenchmarkHandlerNativePassthrough is the no-conversion baseline for the same
// handler shape: the upstream serves Chat Completions natively.
func BenchmarkHandlerNativePassthrough(b *testing.B) {
	server := conversionBenchHandler(b, "xir", "chat_completions", benchChatUpstream)
	b.ReportAllocs()
	b.ResetTimer()
	runBenchRequests(b, server, "/v1/chat/completions", benchChatBody)
}

// BenchmarkHandlerConversionLargeBody drives a ~270 KB prompt through the same
// chat -> Messages conversion, where the JSON parser and encoder cost dominates
// the handler bookkeeping.
func BenchmarkHandlerConversionLargeBody(b *testing.B) {
	large := fmt.Sprintf(`{"model":"bench-model","max_tokens":512,"messages":[{"role":"user","content":%q}]}`, benchLongPrompt())
	for _, engine := range []string{"legacy", "xir"} {
		b.Run(engine, func(b *testing.B) {
			server := conversionBenchHandler(b, engine, "messages", benchMessagesBody)
			b.ReportAllocs()
			b.ResetTimer()
			runBenchRequests(b, server, "/v1/chat/completions", large)
		})
	}
}

func benchLongPrompt() string {
	repeated := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 6000)
	return string(repeated)
}

// BenchmarkHandlerAgentTrajectory256K drives a 256 KiB multi-step agent request
// through the whole handler (auth, routing, chat -> Messages conversion,
// recording) for both engines. This is the deployment-shape cost a converted
// long-context request pays; the library benchmarks isolate the conversion part.
func BenchmarkHandlerAgentTrajectory256K(b *testing.B) {
	trajectory := benchcorpus.Agent(benchcorpus.Agent256KBytes, benchcorpus.Options{Secrets: true})
	trajectory.Model = "bench-model" // the benchmark channel's declared model
	body := string(benchcorpus.EncodeChat(trajectory))
	for _, engine := range []string{"legacy", "xir"} {
		b.Run(engine, func(b *testing.B) {
			server := conversionBenchHandler(b, engine, "messages", benchMessagesBody)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			runBenchRequests(b, server, "/v1/chat/completions", body)
		})
	}
}
