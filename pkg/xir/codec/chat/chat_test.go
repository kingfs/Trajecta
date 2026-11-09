package chat

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

var _ xir.Codec = New()

func parseDoc(t *testing.T, raw string) *xir.Document {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolChatCompletions, xir.OperationChat, []byte(raw))
	if err != nil {
		t.Fatalf("parse document: %v\nraw: %s", err, raw)
	}
	return doc
}

func decodeRequestDoc(t *testing.T, c *Codec, raw string) *xir.Request {
	t.Helper()
	req, err := c.DecodeRequest(context.Background(), parseDoc(t, raw), xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeRequest: %v\nraw: %s", err, raw)
	}
	if req == nil {
		t.Fatal("DecodeRequest returned nil request")
	}
	return req
}

func decodeResponseDoc(t *testing.T, c *Codec, raw string) *xir.Response {
	t.Helper()
	resp, err := c.DecodeResponse(context.Background(), parseDoc(t, raw), xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeResponse: %v\nraw: %s", err, raw)
	}
	if resp == nil {
		t.Fatal("DecodeResponse returned nil response")
	}
	return resp
}

func clearPartRefs(p *xir.Part) {
	p.Ref = nil
	if p.ToolCall != nil {
		p.ToolCall.Ref = nil
	}
	if p.ToolResult != nil {
		p.ToolResult.Ref = nil
		for i := range p.ToolResult.Content {
			clearPartRefs(&p.ToolResult.Content[i])
		}
	}
}

func clearRequestRefs(req *xir.Request) {
	req.Ref = nil
	for i := range req.Messages {
		msg := &req.Messages[i]
		msg.Ref = nil
		for j := range msg.Parts {
			clearPartRefs(&msg.Parts[j])
		}
	}
	for i := range req.Tools {
		req.Tools[i].Ref = nil
	}
}

func clearResponseRefs(resp *xir.Response) {
	resp.Ref = nil
	for i := range resp.Parts {
		clearPartRefs(&resp.Parts[i])
	}
}

func TestRequestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "simple user text and params",
			raw: `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}],` +
				`"temperature":0.2,"top_p":0.9,"top_k":40,"max_tokens":256,"stop":["\n\n"],` +
				`"seed":7,"stream":true,"unknown_field":{"a":1}}`,
		},
		{
			name: "roles and scalar penalties",
			raw: `{"model":"m","messages":[{"role":"system","content":"be brief"},` +
				`{"role":"developer","content":"use tools"},{"role":"user","content":"hi"},` +
				`{"role":"assistant","content":"hello"}],"presence_penalty":0.1,` +
				`"frequency_penalty":0.2,"logprobs":true,"top_logprobs":3,` +
				`"parallel_tool_calls":false,"user":"u1","max_completion_tokens":64}`,
		},
		{
			name: "multimodal parts",
			raw: `{"model":"m","messages":[{"role":"user","content":[` +
				`{"type":"text","text":"describe"},` +
				`{"type":"image_url","image_url":{"url":"https://example.test/a.png","detail":"high"}},` +
				`{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}},` +
				`{"type":"file","file":{"file_data":"data:application/pdf;base64,AAA","filename":"a.pdf"}}]}]}`,
		},
		{
			name: "multiple text parts",
			raw: `{"model":"m","messages":[{"role":"user","content":[` +
				`{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
		},
		{
			name: "tools calls and tool results",
			raw: `{"model":"m","messages":[{"role":"user","content":"weather?"},` +
				`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function",` +
				`"function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},` +
				`{"role":"tool","tool_call_id":"call_1","content":"{\"temp\":18}"}],` +
				`"tools":[{"type":"function","function":{"name":"get_weather","description":"Weather",` +
				`"parameters":{"type":"object","properties":{"city":{"type":"string"}}},"strict":true}}],` +
				`"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`,
		},
		{
			name: "legacy functions and state",
			raw: `{"model":"m","messages":[{"role":"user","content":"hi"}],` +
				`"functions":[{"name":"lookup","description":"Lookup","parameters":{"type":"object"}}],` +
				`"tool_choice":"required","response_format":{"type":"json_object"},` +
				`"metadata":{"session_id":"sess-1","tenant":"t"},"reasoning":{"effort":"high"},` +
				`"prompt_cache_key":"cache-1","store":true}`,
		},
		{
			name: "legacy assistant function_call",
			raw: `{"model":"m","messages":[{"role":"assistant","content":"",` +
				`"function_call":{"name":"lookup","arguments":"{\"q\":1}"}}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			first := decodeRequestDoc(t, c, tc.raw)
			encoded, err := c.EncodeRequest(context.Background(), first, xir.EncodeOptions{})
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			second := decodeRequestDoc(t, c, string(encoded))
			clearRequestRefs(first)
			clearRequestRefs(second)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("round trip mismatch\nraw: %s\nencoded: %s\nfirst:  %#v\nsecond: %#v",
					tc.raw, encoded, first, second)
			}
		})
	}
}

func TestResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "text",
			raw: `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o","choices":[` +
				`{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		},
		{
			name: "tool calls",
			raw: `{"id":"c2","model":"m","choices":[{"index":0,"message":{"role":"assistant",` +
				`"content":null,"tool_calls":[{"id":"call_9","type":"function",` +
				`"function":{"name":"f","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}],` +
				`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		},
		{
			name: "refusal",
			raw: `{"id":"c3","model":"m","choices":[{"index":0,"message":{"role":"assistant",` +
				`"content":null,"refusal":"I cannot help with that"},"finish_reason":"stop"}]}`,
		},
		{
			name: "multiple text parts and length",
			raw: `{"id":"c4","model":"m","choices":[{"index":0,"message":{"role":"assistant",` +
				`"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},` +
				`"finish_reason":"length"}]}`,
		},
		{
			name: "cache and reasoning usage",
			raw: `{"id":"c5","model":"m","choices":[{"index":0,"message":{"role":"assistant",` +
				`"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,` +
				`"completion_tokens":20,"total_tokens":120,` +
				`"prompt_tokens_details":{"cached_tokens":80},` +
				`"completion_tokens_details":{"reasoning_tokens":12}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			first := decodeResponseDoc(t, c, tc.raw)
			encoded, err := c.EncodeResponse(context.Background(), first, xir.EncodeOptions{})
			if err != nil {
				t.Fatalf("EncodeResponse: %v", err)
			}
			second := decodeResponseDoc(t, c, string(encoded))
			clearResponseRefs(first)
			clearResponseRefs(second)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("round trip mismatch\nraw: %s\nencoded: %s\nfirst:  %#v\nsecond: %#v",
					tc.raw, encoded, first, second)
			}
		})
	}
}

func TestDecodeRequestContentParts(t *testing.T) {
	req := decodeRequestDoc(t, New(), `{"model":"m","messages":[{"role":"user","content":[`+
		`{"type":"text","text":"look"},`+
		`{"type":"image_url","image_url":{"url":"https://example.test/a.png","detail":"low"}},`+
		`{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}},`+
		`{"type":"file","file":{"file_data":"data:application/pdf;base64,AAA","filename":"a.pdf"}}]}]}`)
	if len(req.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(req.Messages))
	}
	parts := req.Messages[0].Parts
	if len(parts) != 4 {
		t.Fatalf("parts = %d, want 4: %#v", len(parts), parts)
	}
	if parts[0].Kind != xir.PartText || parts[0].Text != "look" || parts[0].Ref == nil {
		t.Fatalf("text part = %#v", parts[0])
	}
	image := parts[1]
	if image.Kind != xir.PartImage || image.Media == nil || image.Media.Kind != xir.MediaImage {
		t.Fatalf("image part = %#v", image)
	}
	if image.Media.URL != "https://example.test/a.png" || image.Media.Detail != "low" {
		t.Fatalf("image media = %#v", image.Media)
	}
	audio := parts[2]
	if audio.Kind != xir.PartAudio || audio.Media == nil || audio.Media.Data != "UklGRg==" || audio.Media.MediaType != "wav" {
		t.Fatalf("audio part = %#v", audio)
	}
	file := parts[3]
	if file.Kind != xir.PartFile || file.Media == nil || file.Media.Data != "data:application/pdf;base64,AAA" ||
		file.Media.Filename != "a.pdf" {
		t.Fatalf("file part = %#v", file)
	}

	encoded, err := New().EncodeRequest(context.Background(), req, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var shape struct {
		Messages []struct {
			Content []map[string]json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("unmarshal encoded: %v\n%s", err, encoded)
	}
	if len(shape.Messages) != 1 || len(shape.Messages[0].Content) != 4 {
		t.Fatalf("encoded content = %#v", shape.Messages)
	}
	img := shape.Messages[0].Content[1]
	if _, ok := img["image_url"]; !ok {
		t.Fatalf("image part missing image_url object: %s", encoded)
	}
	var imageObj struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(img["image_url"], &imageObj); err != nil {
		t.Fatalf("unmarshal image_url: %v", err)
	}
	if imageObj.URL != "https://example.test/a.png" || imageObj.Detail != "low" {
		t.Fatalf("encoded image_url = %#v", imageObj)
	}
	if _, ok := shape.Messages[0].Content[2]["input_audio"]; !ok {
		t.Fatal("audio part missing input_audio object")
	}
	if _, ok := shape.Messages[0].Content[3]["file"]; !ok {
		t.Fatal("file part missing file object")
	}
}

func TestDecodeStringContent(t *testing.T) {
	req := decodeRequestDoc(t, New(), `{"model":"m","messages":[{"role":"system","content":"sys"},`+
		`{"role":"user","content":"hello"}]}`)
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	for i, want := range []string{"sys", "hello"} {
		parts := req.Messages[i].Parts
		if len(parts) != 1 || parts[0].Kind != xir.PartText || parts[0].Text != want {
			t.Fatalf("message %d parts = %#v", i, parts)
		}
	}
	if req.Messages[0].Role != xir.RoleSystem || req.Messages[1].Role != xir.RoleUser {
		t.Fatalf("roles = %v/%v", req.Messages[0].Role, req.Messages[1].Role)
	}
}

func TestToolCallAndResultLoop(t *testing.T) {
	raw := `{"model":"m","messages":[{"role":"user","content":"weather?"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function",` +
		`"function":{"name":"get_weather","arguments":"{\"city\":\"Paris\",\"unit\":\"c\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"{\"temp\":18}"}],` +
		`"tools":[{"type":"function","function":{"name":"get_weather",` +
		`"parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
	c := New()
	req := decodeRequestDoc(t, c, raw)
	if len(req.Messages) != 3 {
		t.Fatalf("messages = %d", len(req.Messages))
	}
	assistant := req.Messages[1]
	if assistant.Role != xir.RoleAssistant {
		t.Fatalf("assistant role = %q", assistant.Role)
	}
	var call *xir.Part
	for i := range assistant.Parts {
		if assistant.Parts[i].Kind == xir.PartToolCall {
			call = &assistant.Parts[i]
		}
	}
	if call == nil || call.ToolCall == nil {
		t.Fatalf("assistant tool call missing: %#v", assistant.Parts)
	}
	tc := call.ToolCall
	if tc.ID != "call_1" || tc.Name != "get_weather" || tc.Type != "function" || tc.Ref == nil {
		t.Fatalf("tool call = %#v", tc)
	}
	if string(tc.Args) != `{"city":"Paris","unit":"c"}` {
		t.Fatalf("tool args = %s", tc.Args)
	}
	if tc.ArgsText != `{"city":"Paris","unit":"c"}` {
		t.Fatalf("tool args text = %q", tc.ArgsText)
	}

	result := req.Messages[2]
	if result.Role != xir.RoleTool || len(result.Parts) != 1 || result.Parts[0].ToolResult == nil {
		t.Fatalf("tool result message = %#v", result)
	}
	tr := result.Parts[0].ToolResult
	if tr.CallID != "call_1" || tr.Text != `{"temp":18}` || tr.Ref == nil {
		t.Fatalf("tool result = %#v", tr)
	}

	encoded, err := c.EncodeRequest(context.Background(), req, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    any    `json:"content"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal encoded: %v\n%s", err, encoded)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("encoded messages = %d", len(payload.Messages))
	}
	encodedCall := payload.Messages[1].ToolCalls
	if len(encodedCall) != 1 || encodedCall[0].Function.Arguments != `{"city":"Paris","unit":"c"}` {
		t.Fatalf("encoded tool_calls = %#v", encodedCall)
	}
	if payload.Messages[2].Role != "tool" || payload.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("encoded tool message = %#v", payload.Messages[2])
	}
	if payload.Messages[2].Content != `{"temp":18}` {
		t.Fatalf("encoded tool content = %#v", payload.Messages[2].Content)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("encoded tools = %#v", payload.Tools)
	}
	// The arguments member must be a JSON string even though Args is an object.
	if !strings.Contains(string(encoded), `"arguments":"{\"city\":\"Paris\",\"unit\":\"c\"}"`) {
		t.Fatalf("encoded arguments are not a JSON string: %s", encoded)
	}
}

func TestToolResultArrayContent(t *testing.T) {
	raw := `{"model":"m","messages":[{"role":"tool","tool_call_id":"call_1","content":[` +
		`{"type":"text","text":"part one"},{"type":"text","text":"part two"}]}]}`
	c := New()
	req := decodeRequestDoc(t, c, raw)
	tr := req.Messages[0].Parts[0].ToolResult
	if tr == nil || tr.CallID != "call_1" || len(tr.Content) != 2 || tr.Text != "" {
		t.Fatalf("tool result = %#v", tr)
	}
	encoded, err := c.EncodeRequest(context.Background(), req, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	again := decodeRequestDoc(t, c, string(encoded))
	got := again.Messages[0].Parts[0].ToolResult
	if got == nil || len(got.Content) != 2 ||
		got.Content[0].Text != "part one" || got.Content[1].Text != "part two" {
		t.Fatalf("tool result round trip = %#v\nencoded: %s", got, encoded)
	}
}

func TestDecodeToolsAndToolChoice(t *testing.T) {
	t.Run("modern tools", func(t *testing.T) {
		req := decodeRequestDoc(t, New(), `{"model":"m","tools":[{"type":"function","function":{`+
			`"name":"f","description":"d","strict":false,"parameters":{"type":"object"}}},`+
			`{"type":"web_search"}]}`)
		if len(req.Tools) != 2 {
			t.Fatalf("tools = %d", len(req.Tools))
		}
		fn := req.Tools[0]
		if fn.Name != "f" || fn.Description != "d" || fn.Kind != "function" || fn.Ref == nil {
			t.Fatalf("function tool = %#v", fn)
		}
		if fn.Strict == nil || *fn.Strict {
			t.Fatalf("strict = %#v", fn.Strict)
		}
		if string(fn.Schema) != `{"type":"object"}` {
			t.Fatalf("schema = %s", fn.Schema)
		}
		if req.Tools[1].Kind != "web_search" {
			t.Fatalf("builtin tool = %#v", req.Tools[1])
		}
	})

	t.Run("legacy functions", func(t *testing.T) {
		req := decodeRequestDoc(t, New(), `{"model":"m","functions":[{"name":"lookup",`+
			`"description":"Lookup","parameters":{"type":"object"}}]}`)
		if len(req.Tools) != 1 || req.Tools[0].Name != "lookup" || req.Tools[0].Kind != "function" {
			t.Fatalf("tools = %#v", req.Tools)
		}
	})

	t.Run("tool choice strings", func(t *testing.T) {
		for _, mode := range []string{"auto", "none", "required"} {
			req := decodeRequestDoc(t, New(), `{"model":"m","tool_choice":"`+mode+`"}`)
			if req.ToolChoice.Mode != mode {
				t.Fatalf("mode = %#v", req.ToolChoice)
			}
		}
	})

	t.Run("tool choice object", func(t *testing.T) {
		req := decodeRequestDoc(t, New(), `{"model":"m","tool_choice":{"type":"function",`+
			`"function":{"name":"get_weather"}}}`)
		if req.ToolChoice.Mode != "tool" || req.ToolChoice.Name != "get_weather" {
			t.Fatalf("tool choice = %#v", req.ToolChoice)
		}
	})
}

func TestDecodeParamsAndState(t *testing.T) {
	req := decodeRequestDoc(t, New(), `{"model":"m","messages":[{"role":"user","content":"hi"}],`+
		`"temperature":0.5,"top_p":0.8,"top_k":20,"max_tokens":10,"max_completion_tokens":20,`+
		`"stop":"END","seed":42,"presence_penalty":1.5,"frequency_penalty":2.5,`+
		`"logprobs":true,"top_logprobs":4,"parallel_tool_calls":true,`+
		`"response_format":{"type":"json_schema"},"reasoning_effort":"medium",`+
		`"reasoning":{"effort":"low"},"user":"u-1","metadata":{"session_id":"sess-9"},`+
		`"stream":true,"store":false}`)
	p := req.Params
	if p.Temperature == nil || *p.Temperature != 0.5 || p.TopP == nil || *p.TopP != 0.8 {
		t.Fatalf("temperature/top_p = %#v/%#v", p.Temperature, p.TopP)
	}
	if p.TopK == nil || *p.TopK != 20 || p.MaxTokens == nil || *p.MaxTokens != 10 ||
		p.MaxCompletion == nil || *p.MaxCompletion != 20 {
		t.Fatalf("top_k/max tokens = %#v/%#v/%#v", p.TopK, p.MaxTokens, p.MaxCompletion)
	}
	if !reflect.DeepEqual(p.Stop, []string{"END"}) {
		t.Fatalf("stop = %#v", p.Stop)
	}
	if p.Seed == nil || *p.Seed != 42 || p.PresencePenalty == nil || *p.PresencePenalty != 1.5 ||
		p.FrequencyPenalty == nil || *p.FrequencyPenalty != 2.5 {
		t.Fatalf("seed/penalties = %#v/%#v/%#v", p.Seed, p.PresencePenalty, p.FrequencyPenalty)
	}
	if p.Logprobs == nil || !*p.Logprobs || p.TopLogprobs == nil || *p.TopLogprobs != 4 {
		t.Fatalf("logprobs = %#v/%#v", p.Logprobs, p.TopLogprobs)
	}
	if p.ParallelTools == nil || !*p.ParallelTools {
		t.Fatalf("parallel_tool_calls = %#v", p.ParallelTools)
	}
	if string(p.ResponseFormat) != `{"type":"json_schema"}` {
		t.Fatalf("response_format = %s", p.ResponseFormat)
	}
	if p.ReasoningEffort != "medium" || string(p.Extra["reasoning"]) != `{"effort":"low"}` {
		t.Fatalf("reasoning = %q/%s", p.ReasoningEffort, p.Extra["reasoning"])
	}
	if p.User != "u-1" || string(p.Metadata) != `{"session_id":"sess-9"}` {
		t.Fatalf("user/metadata = %q/%s", p.User, p.Metadata)
	}
	if !req.Stream {
		t.Fatal("stream flag lost")
	}
	if req.State.PromptCacheKey != "sess-9" {
		t.Fatalf("prompt cache key from metadata.session_id = %q", req.State.PromptCacheKey)
	}
	if req.State.Store == nil || *req.State.Store {
		t.Fatalf("store = %#v", req.State.Store)
	}

	explicit := decodeRequestDoc(t, New(), `{"model":"m","prompt_cache_key":"cache-7",`+
		`"metadata":{"session_id":"sess-9"},"previous_response_id":"resp-1"}`)
	if explicit.State.PromptCacheKey != "cache-7" {
		t.Fatalf("explicit prompt cache key = %q", explicit.State.PromptCacheKey)
	}
	if explicit.State.PreviousResponseID != "resp-1" || !explicit.State.HasServerState() {
		t.Fatalf("state refs = %#v", explicit.State)
	}
}

func TestDecodeResponseUsage(t *testing.T) {
	resp := decodeResponseDoc(t, New(), `{"id":"c","model":"m","choices":[{"index":0,`+
		`"message":{"role":"assistant","content":"ok"},"finish_reason":"tool_calls"}],`+
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,`+
		`"prompt_tokens_details":{"cached_tokens":80},`+
		`"completion_tokens_details":{"reasoning_tokens":12}}}`)
	u := resp.Usage
	if u == nil {
		t.Fatal("usage missing")
	}
	if u.InputTokens != 100 || u.OutputTokens != 20 || u.TotalTokens != 120 {
		t.Fatalf("usage = %#v", u)
	}
	if u.CacheReadTokens != 80 || u.ReasoningTokens != 12 {
		t.Fatalf("cache/reasoning = %#v", u)
	}
	if u.Source != "chat" {
		t.Fatalf("source = %q", u.Source)
	}
	if resp.StopReason != xir.FinishToolUse {
		t.Fatalf("stop reason = %q", resp.StopReason)
	}

	// DeepSeek-compatible cache counters are accepted as a fallback.
	deepseek := decodeResponseDoc(t, New(), `{"choices":[{"message":{"role":"assistant","content":"x"}}],`+
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":6}}`)
	if deepseek.Usage.CacheReadTokens != 6 || deepseek.Usage.TotalTokens != 12 {
		t.Fatalf("deepseek usage = %#v", deepseek.Usage)
	}

	encoded, err := New().EncodeResponse(context.Background(), resp, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var shape struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("unmarshal encoded usage: %v\n%s", err, encoded)
	}
	if shape.Usage.PromptTokens != 100 || shape.Usage.CompletionTokens != 20 || shape.Usage.TotalTokens != 120 {
		t.Fatalf("encoded usage = %#v", shape.Usage)
	}
	if !strings.Contains(string(encoded), `"cached_tokens":80`) ||
		!strings.Contains(string(encoded), `"reasoning_tokens":12`) {
		t.Fatalf("encoded usage lost cache/reasoning counters: %s", encoded)
	}
}

func TestErrorEnvelope(t *testing.T) {
	raw := `{"error":{"message":"Invalid model","type":"invalid_request_error",` +
		`"param":"model","code":"model_not_found"}}`
	c := New()
	resp := decodeResponseDoc(t, c, raw)
	if resp.Error == nil {
		t.Fatal("error not decoded")
	}
	e := resp.Error
	if e.Message != "Invalid model" || e.Type != "invalid_request_error" ||
		e.Param != "model" || e.Code != "model_not_found" {
		t.Fatalf("error = %#v", e)
	}
	if len(resp.Parts) != 0 {
		t.Fatalf("error response parts = %#v", resp.Parts)
	}
	if resp.Ref == nil {
		t.Fatal("response ref missing")
	}

	encoded, err := c.EncodeResponse(context.Background(), resp, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	if !strings.Contains(string(encoded), `"error"`) || !strings.Contains(string(encoded), "Invalid model") {
		t.Fatalf("encoded error = %s", encoded)
	}
	again := decodeResponseDoc(t, c, string(encoded))
	if again.Error == nil || again.Error.Message != e.Message || again.Error.Code != e.Code ||
		again.Error.Type != e.Type || again.Error.Param != e.Param {
		t.Fatalf("error round trip = %#v", again.Error)
	}

	// A numeric code is normalized to its textual form.
	numeric := decodeResponseDoc(t, c, `{"error":{"message":"bad","code":400}}`)
	if numeric.Error.Code != "400" {
		t.Fatalf("numeric code = %q", numeric.Error.Code)
	}

	// A bare string envelope is tolerated.
	bare := decodeResponseDoc(t, c, `{"error":"boom"}`)
	if bare.Error == nil || bare.Error.Message != "boom" {
		t.Fatalf("string error = %#v", bare.Error)
	}
}

func TestMalformedInput(t *testing.T) {
	c := New()

	requestCases := []struct {
		name string
		raw  string
	}{
		{name: "root array", raw: `[]`},
		{name: "root null", raw: `null`},
		{name: "messages object", raw: `{"messages":{}}`},
		{name: "messages string", raw: `{"messages":"hi"}`},
		{name: "tools object", raw: `{"tools":{}}`},
		{name: "functions string", raw: `{"functions":"x"}`},
	}
	for _, tc := range requestCases {
		t.Run("request/"+tc.name, func(t *testing.T) {
			req, err := c.DecodeRequest(context.Background(), parseDoc(t, tc.raw), xir.DecodeOptions{})
			if err == nil {
				t.Fatalf("DecodeRequest accepted malformed input: %#v", req)
			}
		})
	}

	responseCases := []struct {
		name string
		raw  string
	}{
		{name: "no choices", raw: `{}`},
		{name: "choices object", raw: `{"choices":{}}`},
		{name: "empty choices", raw: `{"choices":[]}`},
		{name: "choice not object", raw: `{"choices":[1]}`},
		{name: "message not object", raw: `{"choices":[{"message":[]}]}`},
		{name: "error not object", raw: `{"error":42}`},
	}
	for _, tc := range responseCases {
		t.Run("response/"+tc.name, func(t *testing.T) {
			resp, err := c.DecodeResponse(context.Background(), parseDoc(t, tc.raw), xir.DecodeOptions{})
			if err == nil {
				t.Fatalf("DecodeResponse accepted malformed input: %#v", resp)
			}
		})
	}

	if _, err := c.DecodeRequest(context.Background(), nil, xir.DecodeOptions{}); err == nil {
		t.Fatal("nil document accepted")
	}
	if _, err := c.DecodeResponse(context.Background(), &xir.Document{}, xir.DecodeOptions{}); err == nil {
		t.Fatal("document without root accepted")
	}
	if _, err := xir.ParseDocument(xir.ProtocolChatCompletions, xir.OperationChat, []byte(`{"messages":`)); err == nil {
		t.Fatal("truncated JSON accepted")
	}
	if _, err := c.EncodeRequest(context.Background(), nil, xir.EncodeOptions{}); err == nil {
		t.Fatal("nil request encoded")
	}
	if _, err := c.EncodeResponse(context.Background(), nil, xir.EncodeOptions{}); err == nil {
		t.Fatal("nil response encoded")
	}
}

func TestTolerantDecode(t *testing.T) {
	// Missing optional fields, wrong scalar types and arrays where an object
	// was expected must not fail the decode.
	req := decodeRequestDoc(t, New(), `{"model":"m","messages":[{"role":"user",`+
		`"content":123,"tool_calls":{"bad":true}},{"role":"user","content":"ok"}],`+
		`"temperature":"hot","tools":[{"type":"function"},{"type":"function","function":"nope"}],`+
		`"tool_choice":9}`)
	if req.Model != "m" || len(req.Messages) != 2 {
		t.Fatalf("request = %#v", req)
	}
	if req.Params.Temperature != nil {
		t.Fatalf("temperature = %#v", req.Params.Temperature)
	}
	if len(req.Messages[0].Parts) != 0 {
		t.Fatalf("malformed content produced parts: %#v", req.Messages[0].Parts)
	}
	if req.ToolChoice.Mode != "" {
		t.Fatalf("tool choice = %#v", req.ToolChoice)
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != "" {
		t.Fatalf("tools = %#v", req.Tools)
	}

	// Unknown content part types survive as opaque parts with their text when
	// one is present.
	unknown := decodeRequestDoc(t, New(), `{"messages":[{"role":"user","content":[`+
		`{"type":"mystery","text":"kept"}]}]}`)
	part := unknown.Messages[0].Parts[0]
	if part.Kind != xir.PartUnknown || part.Text != "kept" || part.Ref == nil {
		t.Fatalf("unknown part = %#v", part)
	}

	// A response without finish_reason or usage is still valid.
	resp := decodeResponseDoc(t, New(), `{"choices":[{"message":{"role":"assistant",`+
		`"content":"hi"}}]}`)
	if resp.StopReason != "" || resp.Usage != nil || len(resp.Parts) != 1 {
		t.Fatalf("response = %#v", resp)
	}

	// Malformed message content is ignored, not fatal.
	lax := decodeResponseDoc(t, New(), `{"choices":[{"message":{"content":123}}]}`)
	if len(lax.Parts) != 0 {
		t.Fatalf("parts = %#v", lax.Parts)
	}
}

func TestDecodeRefs(t *testing.T) {
	doc := parseDoc(t, `{"model":"m","messages":[{"role":"user","content":"hi"},`+
		`{"role":"assistant","tool_calls":[{"id":"c","type":"function",`+
		`"function":{"name":"f","arguments":"{}"}}]}],`+
		`"tools":[{"type":"function","function":{"name":"f"}}]}`)
	req, err := New().DecodeRequest(context.Background(), doc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.Ref != doc.Root {
		t.Fatal("request ref is not the document root")
	}
	messages, _ := doc.Root.Get("messages")
	if req.Messages[0].Ref != messages.Items[0] {
		t.Fatal("message ref mismatch")
	}
	if req.Messages[0].Parts[0].Ref != messages.Items[0].Members[1].Value {
		t.Fatal("text part ref does not point at the content value")
	}
	call := req.Messages[1].Parts[0]
	toolCalls, _ := messages.Items[1].Get("tool_calls")
	if call.Ref != toolCalls.Items[0] {
		t.Fatal("tool call ref does not point at the call object")
	}
	tools, _ := doc.Root.Get("tools")
	function, _ := tools.Items[0].Get("function")
	if req.Tools[0].Ref != function {
		t.Fatal("tool ref does not point at the function object")
	}

	respDoc := parseDoc(t, `{"choices":[{"message":{"content":"hi"}}]}`)
	resp, err := New().DecodeResponse(context.Background(), respDoc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Ref != respDoc.Root || len(resp.Parts) != 1 || resp.Parts[0].Ref == nil {
		t.Fatalf("response refs = %#v", resp)
	}
}

func TestEncodeRequestMinimal(t *testing.T) {
	req := &xir.Request{
		Model: "gpt-4o",
		Messages: []xir.Message{{
			Role:  xir.RoleUser,
			Parts: []xir.Part{{Kind: xir.PartText, Text: "hi"}},
		}},
	}
	encoded, err := New().EncodeRequest(context.Background(), req, xir.EncodeOptions{})
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, encoded)
	}
	if len(payload) != 2 {
		t.Fatalf("unexpected fields: %s", encoded)
	}
	if string(payload["model"]) != `"gpt-4o"` {
		t.Fatalf("model = %s", payload["model"])
	}
	if !strings.Contains(string(encoded), `{"role":"user","content":"hi"}`) {
		t.Fatalf("message shape = %s", encoded)
	}

	// EncodeOptions.Model overrides the request model.
	overridden, err := New().EncodeRequest(context.Background(), req, xir.EncodeOptions{Model: "other", Stream: true})
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !strings.Contains(string(overridden), `"model":"other"`) || !strings.Contains(string(overridden), `"stream":true`) {
		t.Fatalf("override/stream = %s", overridden)
	}
}

func TestCodecConcurrentUse(t *testing.T) {
	c := New()
	doc := parseDoc(t, `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},`+
		`{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}],"stream":true}`)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				req, err := c.DecodeRequest(context.Background(), doc, xir.DecodeOptions{})
				if err != nil {
					t.Errorf("DecodeRequest: %v", err)
					return
				}
				if _, err := c.EncodeRequest(context.Background(), req, xir.EncodeOptions{}); err != nil {
					t.Errorf("EncodeRequest: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
