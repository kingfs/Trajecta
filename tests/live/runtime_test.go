package live

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// This file exercises the local Responses runtime through the public proxy
// entrypoint, including the strategy switch that selects native pass-through
// versus local orchestration.

type routingSettings struct {
	ResponsesStrategy string `json:"responses_strategy"`
}

func monitorRequest(t *testing.T, method, path string, body any) httpResult {
	t.Helper()
	return request(t, method, monitorURL()+path, body, map[string]string{
		"Authorization": "Bearer " + monitorToken(t),
	})
}

// withResponsesStrategy patches app_settings key routing.settings for the
// duration of the test and restores the previous value afterwards.
func withResponsesStrategy(t *testing.T, strategy string) {
	t.Helper()

	before := monitorRequest(t, http.MethodGet, "/api/settings/routing", nil)
	if before.Status != http.StatusOK {
		t.Fatalf("read routing settings: want 200, got %s", before)
	}
	var settings routingSettings
	decodeJSON(t, before.Body, &settings)

	patched := monitorRequest(t, http.MethodPatch, "/api/settings/routing", map[string]string{"responses_strategy": strategy})
	if patched.Status != http.StatusOK {
		t.Fatalf("patch routing settings to %q: want 200, got %s", strategy, patched)
	}

	t.Cleanup(func() {
		restore := monitorRequest(t, http.MethodPatch, "/api/settings/routing", map[string]string{"responses_strategy": settings.ResponsesStrategy})
		if restore.Status != http.StatusOK {
			t.Errorf("restoring responses_strategy=%q failed: %s", settings.ResponsesStrategy, restore)
		}
	})
}

func responsesRequest(t *testing.T, body map[string]any) httpResult {
	t.Helper()
	return proxyRequest(t, http.MethodPost, "/v1/responses", body, nil)
}

func TestLiveLocalResponsesRuntime(t *testing.T) {
	requireLive(t)
	withResponsesStrategy(t, "local_server_only")

	res := responsesRequest(t, map[string]any{
		"model": modelName(),
		"input": "Reply with exactly the word pong and nothing else.",
		"store": true,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("local responses: want 200, got %s", res)
	}

	var parsed struct {
		ID         string `json:"id"`
		Object     string `json:"object"`
		Status     string `json:"status"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	decodeJSON(t, res.Body, &parsed)

	if parsed.Object != "response" || parsed.Status != "completed" {
		t.Errorf("local responses envelope: object=%q status=%q", parsed.Object, parsed.Status)
	}
	if !strings.HasPrefix(parsed.ID, "resp_") {
		t.Errorf("local responses id=%q, want a resp_ prefix (local runtime)", parsed.ID)
	}
	if len(parsed.Output) == 0 {
		t.Fatalf("local responses: empty output; %s", res)
	}
	text := parsed.OutputText
	if text == "" {
		for _, item := range parsed.Output {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text += part.Text
				}
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		t.Errorf("local responses: no assistant text; %s", res)
	}
	if parsed.Usage.TotalTokens <= 0 {
		t.Errorf("local responses: total_tokens=%d", parsed.Usage.TotalTokens)
	}
}

func TestLiveLocalResponsesContinuation(t *testing.T) {
	requireLive(t)
	withResponsesStrategy(t, "local_server_only")

	first := responsesRequest(t, map[string]any{
		"model": modelName(),
		"input": "Remember the number 41. Reply with OK only.",
		"store": true,
	})
	if first.Status != http.StatusOK {
		t.Fatalf("first turn: want 200, got %s", first)
	}
	var firstBody struct {
		ID string `json:"id"`
	}
	decodeJSON(t, first.Body, &firstBody)
	if firstBody.ID == "" {
		t.Fatalf("first turn returned no id: %s", first)
	}

	second := responsesRequest(t, map[string]any{
		"model":                modelName(),
		"previous_response_id": firstBody.ID,
		"input":                "What number did I ask you to remember? Reply with just the number.",
		"store":                true,
	})
	if second.Status != http.StatusOK {
		t.Fatalf("continuation: want 200, got %s", second)
	}
	var secondBody struct {
		ID                 string `json:"id"`
		PreviousResponseID string `json:"previous_response_id"`
		OutputText         string `json:"output_text"`
		Output             []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	decodeJSON(t, second.Body, &secondBody)
	if secondBody.PreviousResponseID != firstBody.ID {
		t.Errorf("continuation: previous_response_id=%q, want %q", secondBody.PreviousResponseID, firstBody.ID)
	}
	text := secondBody.OutputText
	for _, item := range secondBody.Output {
		for _, part := range item.Content {
			text += part.Text
		}
	}
	if !strings.Contains(text, "41") {
		t.Errorf("continuation lost conversation state: answer=%q (%s)", text, truncate(string(second.Body), 400))
	}
}

func TestLiveLocalResponsesFunctionCall(t *testing.T) {
	requireLive(t)
	withResponsesStrategy(t, "local_server_only")

	tools := []map[string]any{{
		"type":        "function",
		"name":        "get_weather",
		"description": "Get the current weather for a city",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []string{"city"},
		},
	}}

	call := responsesRequest(t, map[string]any{
		"model": modelName(),
		"input": "What is the weather in Shanghai? You must call the get_weather tool.",
		"store": true,
		"tools": tools,
	})
	if call.Status != http.StatusOK {
		t.Fatalf("function call turn: want 200, got %s", call)
	}
	var callBody struct {
		ID     string `json:"id"`
		Output []struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	decodeJSON(t, call.Body, &callBody)

	var fn *struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	}
	for i := range callBody.Output {
		if callBody.Output[i].Type == "function_call" {
			fn = &callBody.Output[i]
			break
		}
	}
	if fn == nil {
		t.Skipf("model did not request the function; pass-through of the tool descriptor still worked: %s", truncate(string(call.Body), 400))
	}
	if fn.Name != "get_weather" {
		t.Errorf("function_call name=%q, want get_weather", fn.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn.Arguments), &args); err != nil {
		t.Errorf("function_call arguments are not JSON: %q", fn.Arguments)
	}

	// Feed the tool result back through the same conversation.
	finish := responsesRequest(t, map[string]any{
		"model":                modelName(),
		"previous_response_id": callBody.ID,
		"store":                true,
		"input": []map[string]any{{
			"type":    "function_call_output",
			"call_id": fn.CallID,
			"output":  "sunny, 25C",
		}},
	})
	if finish.Status != http.StatusOK {
		t.Fatalf("function_call_output turn: want 200, got %s", finish)
	}
	if !bytes.Contains(finish.Body, []byte("25")) && !bytes.Contains(finish.Body, []byte("sunny")) && !bytes.Contains(finish.Body, []byte("Sunny")) {
		t.Logf("tool result was not echoed verbatim (model wording); body=%s", truncate(string(finish.Body), 400))
	}
}

func TestLiveLocalResponsesCompact(t *testing.T) {
	requireLive(t)
	withResponsesStrategy(t, "local_server_only")

	create := responsesRequest(t, map[string]any{
		"model": modelName(),
		"input": "The project codename is pineapple. Reply with OK only.",
		"store": true,
	})
	if create.Status != http.StatusOK {
		t.Fatalf("create turn: want 200, got %s", create)
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeJSON(t, create.Body, &created)

	res := proxyRequest(t, http.MethodPost, "/v1/responses/compact", map[string]any{
		"response_id": created.ID,
		"model":       modelName(),
	}, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("compact: want 200, got %s", res)
	}
	var compacted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Metadata struct {
			Gateway struct {
				Compact struct {
					Auto              bool   `json:"auto"`
					Manual            bool   `json:"manual"`
					CompactResponseID string `json:"compact_response_id"`
				} `json:"compact"`
			} `json:"_gateway"`
		} `json:"metadata"`
	}
	decodeJSON(t, res.Body, &compacted)

	if compacted.Status != "completed" {
		t.Errorf("compact status=%q, want completed", compacted.Status)
	}
	foundSummary := false
	for _, item := range compacted.Output {
		if item.Type == "summary" {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Errorf("compact produced no summary output item: %s", truncate(string(res.Body), 400))
	}
	if compacted.Metadata.Gateway.Compact.CompactResponseID == "" {
		t.Errorf("compact provenance metadata missing _gateway.compact.compact_response_id: %s", truncate(string(res.Body), 600))
	}

	// A continuation pointing at the compact response must still work.
	next := responsesRequest(t, map[string]any{
		"model":                modelName(),
		"previous_response_id": compacted.ID,
		"input":                "What was the codename? Reply with just the word.",
		"store":                true,
	})
	if next.Status != http.StatusOK {
		t.Fatalf("continuation after compact: want 200, got %s", next)
	}
}

func TestLiveUnsupportedHostedToolRejected(t *testing.T) {
	requireLive(t)
	withResponsesStrategy(t, "local_server_only")

	for _, tool := range []string{"file_search", "code_interpreter", "computer_use_preview"} {
		tool := tool
		t.Run(tool, func(t *testing.T) {
			res := responsesRequest(t, map[string]any{
				"model":       modelName(),
				"input":       "go",
				"store":       true,
				"tools":       []map[string]any{{"type": tool}},
				"tool_choice": map[string]any{"type": tool},
			})
			if res.Status != http.StatusBadRequest {
				t.Fatalf("forced %s: want 400, got %s", tool, res)
			}
			var errBody struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			decodeJSON(t, res.Body, &errBody)
			if errBody.Error.Code != "unsupported_tool" {
				t.Errorf("forced %s: error.code=%q, want unsupported_tool", tool, errBody.Error.Code)
			}
			if !strings.Contains(errBody.Error.Message, tool) {
				t.Errorf("forced %s: error message %q does not name the tool", tool, errBody.Error.Message)
			}
		})
	}
}

func TestLiveLocalResponsesStream(t *testing.T) {
	requireLive(t)
	withResponsesStrategy(t, "local_server_only")

	res := responsesRequest(t, map[string]any{
		"model":  modelName(),
		"input":  "Reply with exactly the word pong.",
		"stream": true,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("local responses stream: want 200, got %s", res)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("local responses stream: content-type=%q", ct)
	}

	events := map[string]int{}
	scanner := bufio.NewScanner(bytes.NewReader(res.Body))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		events[name]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}

	for _, want := range []string{"response.created", "response.output_item.added", "response.output_text.delta", "response.completed"} {
		if events[want] == 0 {
			t.Errorf("local responses stream: missing %q event (seen: %v)", want, events)
		}
	}
}

// TestLiveResponsesNativeVsLocal pins the per-request execution-mode decision:
// with auto strategy and a channel that declares native Responses support, the
// response id comes from the upstream; with local_server_only it is minted by
// the local runtime.
func TestLiveResponsesNativeVsLocal(t *testing.T) {
	requireLive(t)

	withResponsesStrategy(t, "auto")
	native := responsesRequest(t, map[string]any{
		"model": modelName(),
		"input": "Reply with exactly the word pong.",
	})
	if native.Status != http.StatusOK {
		t.Fatalf("native responses: want 200, got %s", native)
	}
	var nativeBody struct {
		ID string `json:"id"`
	}
	decodeJSON(t, native.Body, &nativeBody)
	if strings.HasPrefix(nativeBody.ID, "resp_") {
		t.Errorf("auto strategy returned a locally minted id %q; expected native pass-through for this channel", nativeBody.ID)
	}

	withResponsesStrategy(t, "local_server_only")
	local := responsesRequest(t, map[string]any{
		"model": modelName(),
		"input": "Reply with exactly the word pong.",
	})
	if local.Status != http.StatusOK {
		t.Fatalf("local responses: want 200, got %s", local)
	}
	var localBody struct {
		ID string `json:"id"`
	}
	decodeJSON(t, local.Body, &localBody)
	if !strings.HasPrefix(localBody.ID, "resp_") {
		t.Errorf("local_server_only returned id %q, expected a local resp_ id", localBody.ID)
	}
}
