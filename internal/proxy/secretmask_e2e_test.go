package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	"github.com/kingfs/Trajecta/pkg/replay"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

const (
	testOpenAIProjectKey = "sk-proj-" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
		"T3BlbkFJ" + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	// google.api_key pins an exact 39-byte length, so the fixture must too.
	testGoogleAPIKey = "AIza" + "SyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q"
)

// TestSecretMaskingFixtures guards the fixture constants against the rule
// bounds: a key that is one byte too long simply never matches, and the test
// would then pass while asserting nothing.
func TestSecretMaskingFixtures(t *testing.T) {
	if got := len(testOpenAIProjectKey); got != 132 {
		t.Errorf("openai project key is %d bytes, want 132", got)
	}
	if got := len(testGoogleAPIKey); got != 39 {
		t.Errorf("google api key is %d bytes, want 39", got)
	}
}

// secretMaskingBody is a request body carrying two credentials of different
// strengths: the OpenAI project key is conclusive on shape alone, the Google key
// is conclusive on prefix plus exact length.
func secretMaskingBody() string {
	return `{"model":"gpt-5","messages":[{"role":"user","content":"key one ` +
		testOpenAIProjectKey + ` and key two ` + testGoogleAPIKey + `"}]}`
}

// TestHandlerMasksCredentialsOnTheWire is the end-to-end contract: the upstream
// must never receive the real value, must receive something the same length and
// shape, and a value it echoes back must reach the client as the original.
//
// The upstream deliberately echoes the key it was given rather than a
// precomputed stand-in, so the assertion that the client sees the original key
// exercises both directions and depends on the same-length property: the test
// recovers the stand-in by slicing the request body at the real key's offset and
// length.
func TestHandlerMasksCredentialsOnTheWire(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var upstreamBody string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)

		// Echo back whatever stood in for the OpenAI project key, located by
		// prefix and sized by the real value's length.
		seen := "(missing)"
		if i := bytes.Index(body, []byte("sk-proj-")); i >= 0 {
			if i+len(testOpenAIProjectKey) <= len(body) {
				seen = string(body[i : i+len(testOpenAIProjectKey)])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"echo": seen})
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
	cfg.Debug.OutputDir = outputDir
	cfg.SecretMasking.Enabled = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(secretMaskingBody()))
	if err != nil {
		t.Fatalf("client.Post() error = %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, respBody)
	}

	// The upstream saw stand-ins, never the real values.
	for _, secret := range []string{testOpenAIProjectKey, testGoogleAPIKey} {
		if strings.Contains(upstreamBody, secret) {
			t.Errorf("the upstream received the real value %q", secret)
		}
	}
	if !strings.Contains(upstreamBody, "sk-proj-") {
		t.Errorf("the stand-in lost the vendor prefix: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "T3BlbkFJ") {
		t.Errorf("the stand-in lost the vendor watermark: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "AIza") {
		t.Errorf("the stand-in for the Google key lost its prefix: %s", upstreamBody)
	}
	if len(upstreamBody) != len(secretMaskingBody()) {
		t.Errorf("body length changed: got %d, want %d; content-length would desynchronise",
			len(upstreamBody), len(secretMaskingBody()))
	}

	// The model's echo came back as the original value.
	var echoed struct {
		Echo string `json:"echo"`
	}
	if err := json.Unmarshal(respBody, &echoed); err != nil {
		t.Fatalf("decode response %q: %v", respBody, err)
	}
	if echoed.Echo != testOpenAIProjectKey {
		t.Errorf("client received %q, want the original key restored", echoed.Echo)
	}

	// The cassette holds the original bytes, so replay and inspection see the
	// real request.
	path := findRecordedHTTP(t, outputDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cassette %q: %v", path, err)
	}
	parsed, err := waitForRecordedPrelude(path, time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude() error = %v", err)
	}
	_, recordedBody, _, _ := recordfile.ExtractSections(raw, parsed)
	if !strings.Contains(string(recordedBody), testOpenAIProjectKey) {
		t.Errorf("the cassette did not record the original body: %s", recordedBody)
	}

	// The audit trail names the rule without carrying the value.
	var masked, restored int
	for _, event := range parsed.Events {
		switch event.Type {
		case "secret.masked":
			masked++
			if rule, _ := event.Attributes["rule"].(string); rule == "" {
				t.Errorf("secret.masked event has no rule: %+v", event.Attributes)
			}
			for _, secret := range []string{testOpenAIProjectKey, testGoogleAPIKey} {
				if strings.Contains(recordfileEventText(event), secret) {
					t.Errorf("secret.masked event leaked the value %q", secret)
				}
			}
		case "secret.restored":
			restored++
		}
	}
	if masked != 2 {
		t.Errorf("recorded %d secret.masked events, want 2", masked)
	}
	if restored != 1 {
		t.Errorf("recorded %d secret.restored events, want 1", restored)
	}
}

// TestHandlerLeavesBodiesAloneWhenMaskingIsOff pins that the switch is
// authoritative in both directions and that a disabled vault costs nothing: the
// upstream sees the exact client bytes and no masking event is written. The
// feature is on by default, so this covers the explicit opt-out - a bare
// config.Config has the zero value and the proxy treats that as off.
func TestHandlerLeavesBodiesAloneWhenMaskingIsOff(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var upstreamBody string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
	cfg.Debug.OutputDir = outputDir

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(secretMaskingBody()))
	if err != nil {
		t.Fatalf("client.Post() error = %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if upstreamBody != secretMaskingBody() {
		t.Fatalf("upstream body changed with masking off\n got %s\nwant %s", upstreamBody, secretMaskingBody())
	}

	parsed, err := waitForRecordedPrelude(findRecordedHTTP(t, outputDir), time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude() error = %v", err)
	}
	for _, event := range parsed.Events {
		if event.Type == "secret.masked" || event.Type == "secret.restored" {
			t.Errorf("masking is off but %q was recorded", event.Type)
		}
	}
}

// recordfileEventText renders an event for a leak check.
func recordfileEventText(event recordfile.RecordEvent) string {
	raw, err := json.Marshal(event.Attributes)
	if err != nil {
		return ""
	}
	return string(raw)
}

// TestHandlerRestoresAStreamedResponseAcrossChunkBoundaries covers the path a
// non-streamed body cannot: an SSE body arrives in many small reads, so the
// restorer has to hold back enough of each read to never emit a partial
// stand-in, and it has to release what it held when the stream ends.
//
// The upstream writes the stand-in in three-byte pieces with a flush after
// each, which is what makes the stand-in straddle read boundaries, and the last
// case ends the body on the stand-in itself so the end-of-stream flush is the
// only thing that can deliver those bytes.
func TestHandlerRestoresAStreamedResponseAcrossChunkBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{name: "stand-in followed by a terminator", suffix: "\n\n"},
		{name: "stand-in is the last bytes of the stream", suffix: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputDir := t.TempDir()
			st, err := store.New(outputDir)
			if err != nil {
				t.Fatalf("store.New() error = %v", err)
			}
			defer st.Close()

			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				standIn := ""
				if i := bytes.Index(body, []byte("sk-proj-")); i >= 0 && i+len(testOpenAIProjectKey) <= len(body) {
					standIn = string(body[i : i+len(testOpenAIProjectKey)])
				}
				if standIn == testOpenAIProjectKey {
					t.Errorf("the upstream received the real key")
				}

				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Errorf("httptest ResponseWriter does not flush")
					return
				}
				_, _ = io.WriteString(w, "data: ")
				flusher.Flush()
				for i := 0; i < len(standIn); i += 3 {
					end := i + 3
					if end > len(standIn) {
						end = len(standIn)
					}
					_, _ = io.WriteString(w, standIn[i:end])
					flusher.Flush()
				}
				if tc.suffix != "" {
					_, _ = io.WriteString(w, tc.suffix)
					flusher.Flush()
				}
			}))
			defer upstreamServer.Close()

			cfg := &config.Config{}
			cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
			cfg.Debug.OutputDir = outputDir
			cfg.SecretMasking.Enabled = true

			handler, err := NewHandler(cfg, st)
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}
			proxyServer := httptest.NewServer(handler)
			defer proxyServer.Close()

			resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
				strings.NewReader(secretMaskingBody()))
			if err != nil {
				t.Fatalf("client.Post() error = %v", err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			want := "data: " + testOpenAIProjectKey + tc.suffix
			if string(got) != want {
				t.Fatalf("streamed body\n got %q\nwant %q", got, want)
			}
			if !strings.Contains(string(got), testOpenAIProjectKey) {
				t.Error("the client never received the real key")
			}
		})
	}
}

// TestHandlerMasksAndRestoresOnTheLocalResponsesPath covers the second real
// upstream send path: a /v1/responses request with no native Responses target
// is orchestrated by the local runtime as an internal chat completion, and that
// hop has its own mask and restore wiring in responses_server.go.
//
// The chat backend echoes whatever stood in for the key, so the assertion that
// the client's Responses output carries the real key proves both that the
// stand-in travelled through the local runtime and that the nested response was
// restored.
func TestHandlerMasksAndRestoresOnTheLocalResponsesPath(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var chatBody, standIn string
	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		chatBody = string(body)

		// Slicing the stand-in out at the real key's offset and length is what
		// makes this a length assertion as well as a shape one.
		if i := bytes.Index(body, []byte("sk-proj-")); i >= 0 && i+len(testOpenAIProjectKey) <= len(body) {
			standIn = string(body[i : i+len(testOpenAIProjectKey)])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-5",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"echo `+standIn+`"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer chatServer.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "chat-backend",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        chatServer.URL + "/v1",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
				Mode:           "responses_server",
				Capabilities: config.UpstreamCapabilitiesConfig{
					ChatCompletions: boolPtr(true),
				},
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.SecretMasking.Enabled = true
	if err := st.SaveAppSettingJSON(context.Background(), "routing.settings", map[string]string{"responses_strategy": "prefer_local_server"}); err != nil {
		t.Fatalf("SaveAppSettingJSON() error = %v", err)
	}

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	reqBody := `{"model":"gpt-5","input":"here is my key ` + testOpenAIProjectKey + `"}`
	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/responses", bytes.NewBufferString(reqBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	clientBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resp.StatusCode = %d, want 200; body=%s", resp.StatusCode, clientBody)
	}

	if chatBody == "" {
		t.Fatal("the chat backend received no request; the local runtime did not run")
	}
	if strings.Contains(chatBody, testOpenAIProjectKey) {
		t.Errorf("the chat backend received the real key: %s", chatBody)
	}
	if !strings.Contains(chatBody, "sk-proj-") || !strings.Contains(chatBody, "T3BlbkFJ") {
		t.Errorf("the stand-in lost its prefix or watermark: %s", chatBody)
	}
	// The stand-in kept the prefix, the watermark and the length, so it is
	// distinguishable from the real key only by its body bytes. Comparing
	// against the stand-in itself is therefore the assertion that separates
	// "restored" from "leaked through".
	if standIn == "" {
		t.Fatal("the chat backend never saw a stand-in to echo")
	}
	if standIn == testOpenAIProjectKey {
		t.Fatal("the stand-in is byte-identical to the real key")
	}
	if !strings.Contains(string(clientBody), testOpenAIProjectKey) {
		t.Errorf("the client never received the real key back: %s", clientBody)
	}
	if strings.Contains(string(clientBody), standIn) {
		t.Errorf("the client received the stand-in instead of the real key: %s", clientBody)
	}

	// The local path is a separate code path from the pass-through one, and it
	// used to build its restorer and drop it, so the restore happened but left
	// no trace on the cassette. Asserting the count here keeps that from
	// regressing: a session reading the trace has to be able to tell that a
	// credential passed through it.
	// The cassette's response section carries the restored value, not the
	// stand-in. This is the assertion that makes the restorer's position
	// underneath UsageSniffer load-bearing rather than incidental: the sniffer
	// writes the bytes it read into the cassette, so a restorer wrapped around
	// it instead would record the stand-in while the client still received the
	// real value, and every replay and audit view would be wrong in a way the
	// client-facing assertions above cannot see.
	cassettePath := findRecordedHTTP(t, outputDir)
	raw, err := os.ReadFile(cassettePath)
	if err != nil {
		t.Fatalf("read cassette %q: %v", cassettePath, err)
	}
	parsed, err := waitForRecordedPrelude(cassettePath, time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude() error = %v", err)
	}
	_, _, _, recordedRespBody := recordfile.ExtractSections(raw, parsed)
	if !strings.Contains(string(recordedRespBody), testOpenAIProjectKey) {
		t.Errorf("the cassette response body did not record the restored value: %s", recordedRespBody)
	}
	if standIn != "" && strings.Contains(string(recordedRespBody), standIn) {
		t.Errorf("the cassette response body recorded the stand-in instead of the value: %s", recordedRespBody)
	}
	var masked, restored int
	var restoredCount float64
	for _, event := range parsed.Events {
		switch event.Type {
		case "secret.masked":
			masked++
		case "secret.restored":
			restored++
			restoredCount, _ = event.Attributes["count"].(float64)
		}
	}
	if masked != 1 {
		t.Errorf("recorded %d secret.masked events, want 1", masked)
	}
	if restored != 1 {
		t.Errorf("recorded %d secret.restored events, want 1; the local Responses path dropped its restorer", restored)
	}
	if restoredCount != 1 {
		t.Errorf("secret.restored count = %v, want 1", restoredCount)
	}
}

// assembledSSE returns the text a client ends up with after it applies the
// deltas of a streamed body: the message content in order, and the tool-call
// arguments per index. Providers cut the logical string at token boundaries, so
// this concatenation - not the raw body - is what has to carry the real value.
func assembledSSE(t *testing.T, body []byte) (content, arguments string) {
	t.Helper()
	for _, line := range strings.Split(string(body), "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("streamed payload is not JSON: %q", payload)
		}
		for _, choice := range event.Choices {
			content += choice.Delta.Content
			for _, call := range choice.Delta.ToolCalls {
				arguments += call.Function.Arguments
			}
		}
	}
	return content, arguments
}

// TestHandlerRestoresAStreamedStandInSplitAcrossDeltas is the realistic version
// of the streamed case. A provider streams one message as many JSON deltas and
// cuts the string at token boundaries, so a stand-in arrives in pieces with the
// JSON envelope between them - which a matcher that only accepts contiguous
// bytes cannot see. Content and tool-call arguments are both covered, because
// both are how a model hands a value back to the client.
func TestHandlerRestoresAStreamedStandInSplitAcrossDeltas(t *testing.T) {
	// A tool-call arguments value is JSON nested inside a JSON string, so its
	// own quotes are escaped on the wire; argsHead and argsTail are the wire
	// form and argsWant is what they decode to once a client applies the deltas.
	const (
		argsHead = `{\"path\":\"/tmp/k\",\"content\":\"`
		argsTail = `\"}`
		argsWant = `{"path":"/tmp/k","content":"`
	)
	for _, field := range []string{"content", "tool_calls"} {
		t.Run(field, func(t *testing.T) {
			outputDir := t.TempDir()
			st, err := store.New(outputDir)
			if err != nil {
				t.Fatalf("store.New() error = %v", err)
			}
			defer st.Close()

			var standIn string
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if i := bytes.Index(body, []byte("sk-proj-")); i >= 0 && i+len(testOpenAIProjectKey) <= len(body) {
					standIn = string(body[i : i+len(testOpenAIProjectKey)])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)
				emit := func(delta string) {
					fmt.Fprintf(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-5\","+
						"\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
					if flusher != nil {
						flusher.Flush()
					}
				}
				// The split is the whole point: one half per delta.
				half := len(standIn) / 2
				if field == "content" {
					emit(`{"content":"` + standIn[:half] + `"}`)
					emit(`{"content":"` + standIn[half:] + `"}`)
				} else {
					emit(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write_file","arguments":"` +
						argsHead + standIn[:half] + `"}}]}`)
					emit(`{"tool_calls":[{"index":0,"function":{"arguments":"` + standIn[half:] + argsTail + `"}}]}`)
				}
				emit(`{}`)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer upstreamServer.Close()

			cfg := &config.Config{}
			cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
			cfg.Debug.OutputDir = outputDir
			cfg.SecretMasking.Enabled = true
			handler, err := NewHandler(cfg, st)
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}
			proxyServer := httptest.NewServer(handler)
			defer proxyServer.Close()

			resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
				strings.NewReader(secretMaskingBody()))
			if err != nil {
				t.Fatalf("client.Post() error = %v", err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if standIn == "" {
				t.Fatal("the upstream never received a stand-in")
			}
			// Neither half of the stand-in may survive anywhere in the stream.
			half := len(standIn) / 2
			for _, piece := range []string{standIn[:half], standIn[half:]} {
				if strings.Contains(string(got), piece) {
					t.Errorf("a piece of the stand-in reached the client: %q", piece)
				}
			}

			content, arguments := assembledSSE(t, got)
			switch field {
			case "content":
				if content != testOpenAIProjectKey {
					t.Errorf("assembled content\n got %q\nwant %q", content, testOpenAIProjectKey)
				}
			case "tool_calls":
				want := argsWant + testOpenAIProjectKey + `"}`
				if arguments != want {
					t.Errorf("assembled tool-call arguments\n got %q\nwant %q", arguments, want)
				}
			}
		})
	}
}

// TestMaskedCassetteStillReplays is what makes "the cassette records the
// original body" a testable claim rather than a comment. The replay transport in
// strict mode compares the request body against the cassette, so replaying the
// request the client actually sent has to succeed and replaying what really went
// on the wire has to fail. If the cassette held the stand-in instead, those two
// outcomes would swap - which is the compatibility break this pins down.
func TestMaskedCassetteStillReplays(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var standIn string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if i := bytes.Index(body, []byte("sk-proj-")); i >= 0 && i+len(testOpenAIProjectKey) <= len(body) {
			standIn = string(body[i : i+len(testOpenAIProjectKey)])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-5",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
	cfg.Debug.OutputDir = outputDir
	cfg.SecretMasking.Enabled = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	original := secretMaskingBody()
	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(original))
	if err != nil {
		t.Fatalf("client.Post() error = %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resp.StatusCode = %d, want 200", resp.StatusCode)
	}
	if standIn == "" {
		t.Fatal("the upstream never received a stand-in, so nothing was masked")
	}

	recordPath := waitForRecordedHTTPByEndpoint(t, outputDir, "/v1/chat/completions", time.Second)
	rt := replay.NewTransport(recordPath)
	rt.StrictRequest = true

	replay := func(body string) error {
		req, err := http.NewRequest(http.MethodPost, "http://replay.invalid/v1/chat/completions", strings.NewReader(body))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	}

	if err := replay(original); err != nil {
		t.Fatalf("replaying the request the client sent did not match the cassette: %v", err)
	}
	masked := strings.Replace(original, testOpenAIProjectKey, standIn, 1)
	if masked == original {
		t.Fatal("the fixture does not contain the key, so the check below proves nothing")
	}
	if err := replay(masked); err == nil {
		t.Fatal("replaying the stand-in matched the cassette, so the cassette did not record the original body")
	}
}

// TestHandlerMasksChineseIDOnTheWire covers the personal-data path end to end.
// The provider must receive a well-formed stand-in, the echo must come back as
// the original number, the cassette must keep the original for replay and
// inspection, and the audit table must keep the pair. The stand-in is located
// by scanning the bytes the provider actually received, not by re-deriving it,
// so the assertion also covers the renderer.
func TestHandlerMasksChineseIDOnTheWire(t *testing.T) {
	const id = "11010519491231002X"
	requestBody := `{"model":"gpt-5","messages":[{"role":"user","content":"我的身份证号是 ` + id + `，请帮我核对。"}]}`

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var upstreamBody string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		seen := "(missing)"
		if findings := secretmask.Scan(body); len(findings) > 0 {
			seen = string(findings[0].Bytes(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"echo": seen})
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
	cfg.Debug.OutputDir = outputDir
	cfg.SecretMasking.Enabled = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("client.Post() error = %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, respBody)
	}

	if strings.Contains(upstreamBody, id) {
		t.Errorf("the upstream received the real identity number: %s", upstreamBody)
	}
	if len(upstreamBody) != len(requestBody) {
		t.Errorf("body length changed: got %d, want %d; content-length would desynchronise",
			len(upstreamBody), len(requestBody))
	}
	var standIn string
	if findings := secretmask.Scan([]byte(upstreamBody)); len(findings) > 0 {
		standIn = string(findings[0].Bytes([]byte(upstreamBody)))
	}
	if len(standIn) != len(id) {
		t.Fatalf("the upstream body carries no identity-number-shaped stand-in: %s", upstreamBody)
	}

	var echoed struct {
		Echo string `json:"echo"`
	}
	if err := json.Unmarshal(respBody, &echoed); err != nil {
		t.Fatalf("decode response %q: %v", respBody, err)
	}
	if echoed.Echo != id {
		t.Errorf("client received %q, want the original number restored", echoed.Echo)
	}

	path := findRecordedHTTP(t, outputDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cassette %q: %v", path, err)
	}
	parsed, err := waitForRecordedPrelude(path, time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude() error = %v", err)
	}
	_, recordedBody, _, _ := recordfile.ExtractSections(raw, parsed)
	if !strings.Contains(string(recordedBody), id) {
		t.Errorf("the cassette did not record the original body: %s", recordedBody)
	}
	var maskedEvents int
	for _, event := range parsed.Events {
		if event.Type != "secret.masked" {
			continue
		}
		maskedEvents++
		if rule, _ := event.Attributes["rule"].(string); rule != "pii.chinese_id" {
			t.Errorf("secret.masked rule = %q, want pii.chinese_id", rule)
		}
		if strings.Contains(recordfileEventText(event), id) || strings.Contains(recordfileEventText(event), standIn) {
			t.Errorf("secret.masked event carries a value: %s", recordfileEventText(event))
		}
	}
	if maskedEvents != 1 {
		t.Errorf("recorded %d secret.masked events, want 1", maskedEvents)
	}

	// The pair is written to the audit table by the recorder's finalize, which
	// can finish a moment after the client has its response.
	deadline := time.Now().Add(2 * time.Second)
	var audits store.CredentialMaskAuditPage
	for {
		audits, err = st.ListCredentialMaskAudits(store.CredentialMaskAuditFilter{PageSize: 10})
		if err != nil {
			t.Fatalf("ListCredentialMaskAudits() error = %v", err)
		}
		if len(audits.Items) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(audits.Items) != 1 {
		t.Fatalf("audit rows = %d, want 1: %+v", len(audits.Items), audits)
	}
	row := audits.Items[0]
	if row.Rule != "pii.chinese_id" || row.Kind != "national_id" || row.Vendor != "" {
		t.Errorf("audit rule/kind/vendor = %q/%q/%q", row.Rule, row.Kind, row.Vendor)
	}
	if row.Original != id || row.StandIn != standIn {
		t.Errorf("audit pair = (%q, %q), want (%q, %q)", row.Original, row.StandIn, id, standIn)
	}
	if row.TraceID == "" || row.End-row.Start != len(id) {
		t.Errorf("audit row lost its trace or offsets: %+v", row)
	}
}
