package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestResponsesRetryStaysOnTheNativeRoute pins the retry half of a documented invariant: a
// `/v1/responses` request that selected a native Responses target keeps that choice for the
// whole attempt, so a retryable upstream failure re-selects inside the same candidate set
// instead of falling back to the local runtime's Chat Completions translation.
//
// The fallback is a real alternative for this request whenever a Chat Completions target is
// configured, and it would change what the client gets: a native pass-through answer versus a
// translated one. Measured on this fixture: the 503 is retried against the same target, the
// retry succeeds, the Chat Completions upstream is never called, and the recorded route plan
// still says `proxy_pass`.
//
// The assertion that carries the test is `chat.calls == 0`. Removing the native target - the
// only change that makes the local runtime the correct answer - turns this request into a
// `responses_server` execution with one call to the Chat Completions upstream, which is
// exactly what the gate reports as a failure.
func TestResponsesRetryStaysOnTheNativeRoute(t *testing.T) {
	const failStatus = http.StatusServiceUnavailable

	var nativeCalls atomic.Int32
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if nativeCalls.Add(1) == 1 {
			w.WriteHeader(failStatus)
			_, _ = io.WriteString(w, `{"error":{"message":"native overloaded"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[],"model":"`+matrixModel+`"}`)
	}))
	t.Cleanup(native.Close)

	chat := newMatrixUpstream(t, protocolChatCompletions)

	nativeTarget := chat.target("native-responses", 200, matrixModel)
	nativeTarget.Upstream.BaseURL = native.URL + "/v1"
	nativeTarget.Upstream.APIType = "responses"
	nativeTarget.Upstream.ProviderPreset = "openai"

	server, outputDir := newMatrixHandlerWithOutputDir(t, nativeTarget, chat.target("chat-backend", 100, matrixModel))

	resp, err := http.Post(server.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"`+matrixModel+`","input":"hi"}`))
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the native target's retry; body = %s", resp.StatusCode, body)
	}
	if got := nativeCalls.Load(); got != 2 {
		t.Fatalf("native upstream received %d request(s), want 2 (the original and the retry); body = %s", got, body)
	}
	if got := chat.callCount(); got != 0 {
		t.Fatalf("the Chat Completions upstream received %d request(s), want 0: the retry fell back to the local runtime instead of re-selecting the native route", got)
	}
	if !waitForRecordedExecutionMode(t, outputDir, "proxy_pass", 5*time.Second) {
		t.Fatal("the recorded route plan does not report a proxy_pass execution mode, so the answer did not come from the native pass-through path")
	}
}
