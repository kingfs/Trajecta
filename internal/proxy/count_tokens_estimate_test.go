package proxy

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// The two helpers below are the pre-refactor count_tokens estimator, kept
// verbatim as the parity oracle for the single-parse implementation in
// handler.go. They live in test code only: the production path decodes the body
// once and derives both the canonical length and the structural overhead from
// that one payload, and must keep producing exactly the number the two-pass
// form produced for every body shape.

func estimateAnthropicCountTokensInputLegacy(body []byte) int {
	if len(body) == 0 {
		return 1
	}
	canonical := body
	var payload any
	if err := json.Unmarshal(body, &payload); err == nil {
		if encoded, marshalErr := json.Marshal(payload); marshalErr == nil && len(encoded) > 0 {
			canonical = encoded
		}
	}
	estimated := (len(canonical) + 3) / 4
	if estimated < 1 {
		return 1
	}
	return estimated + anthropicCountTokensStructuralOverheadLegacy(body)
}

func anthropicCountTokensStructuralOverheadLegacy(body []byte) int {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return 8
	}
	overhead := 8
	if raw := payload["system"]; len(raw) > 0 {
		overhead += 4
	}
	if raw := payload["tools"]; len(raw) > 0 {
		overhead += 8
	}
	if raw := payload["messages"]; len(raw) > 0 {
		var messages []json.RawMessage
		if err := json.Unmarshal(raw, &messages); err == nil {
			overhead += len(messages) * 4
		}
	}
	return overhead
}

func TestEstimateAnthropicCountTokensInputParity(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// Ordinary messages, the shape the SDK and Claude Code send.
		{"plain_messages", `{"model":"glm-5.1","messages":[{"role":"user","content":[{"type":"text","text":"hello count tokens"}]}]}`},
		{"string_content", `{"model":"claude-sonnet-4","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`},
		{"multi_turn", `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`},
		// system as a string and as an array of text blocks.
		{"system_string", `{"model":"m","system":"You are helpful.","messages":[{"role":"user","content":"hi"}]}`},
		{"system_array", `{"model":"m","system":[{"type":"text","text":"You are helpful."},{"type":"text","text":"Be concise.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"system_empty_array", `{"model":"m","system":[],"messages":[]}`},
		// tools.
		{"tools", `{"model":"m","tools":[{"name":"read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"system_tools_messages", `{"model":"m","system":[{"type":"text","text":"sys"}],"tools":[{"name":"t1"},{"name":"t2"}],"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`},
		// Empty and degenerate bodies.
		{"empty", ``},
		{"empty_object", `{}`},
		{"null", `null`},
		{"number", `123`},
		{"string_scalar", `"hello"`},
		{"bool_scalar", `true`},
		{"top_level_array", `[{"role":"user","content":"hi"}]`},
		{"empty_array", `[]`},
		// Malformed bodies: the canonical length stays the raw length and the
		// overhead stays the base 8.
		{"truncated", `{"model":"m","messages":[`},
		{"garbage", `not json at all`},
		{"trailing_bytes", `{"messages":[]} trailing`},
		{"only_open_brace", `{`},
		// messages shapes that are not a usable array.
		{"messages_null", `{"messages":null}`},
		{"messages_string", `{"messages":"nope"}`},
		{"messages_object", `{"messages":{"role":"user"}}`},
		{"messages_number", `{"messages":7}`},
		{"messages_mixed_elements", `{"messages":[null,1,"x",{"role":"user"},[1,2]]}`},
		{"messages_empty", `{"messages":[]}`},
		// Present-but-null system/tools still counted: a present key always
		// carried a non-empty RawMessage.
		{"system_null", `{"system":null}`},
		{"tools_null", `{"tools":null}`},
		{"system_tools_null", `{"system":null,"tools":null,"messages":null}`},
		// Canonicalization effects: whitespace, key order, HTML escaping,
		// unicode and duplicate keys all change the re-encoded length.
		{"pretty_printed", "{\n  \"model\" : \"m\",\n  \"messages\" : [ {\"role\":\"user\",\"content\":\"hi\"} ]\n}"},
		{"html_chars", `{"messages":[{"role":"user","content":"<b>a & b</b> > c"}]}`},
		{"unicode", `{"messages":[{"role":"user","content":"你好，世界 🌍 café"}]}`},
		{"escaped_unicode", `{"messages":[{"role":"user","content":"\u4f60\u597d"}]}`},
		{"duplicate_keys", `{"messages":[],"messages":[{"role":"user"},{"role":"user"}],"system":"a","system":"b"}`},
		{"key_order", `{"tools":[],"system":"s","messages":[],"model":"m"}`},
		// Numbers that overflow float64: the any-decoder rejects the whole
		// body while the raw-map decode still succeeds, so the estimator must
		// fall back to the raw canonical length and the raw-map overhead.
		{"number_overflow", `{"x":1e999}`},
		{"number_overflow_with_messages", `{"messages":[{},{}],"system":"s","x":1e999}`},
		{"number_overflow_in_messages", `{"messages":[{"n":1e999}]}`},
		{"negative_number_overflow", `{"x":-1e999,"tools":[1]}`},
		// A realistic Claude Code shaped request: long system array, tool
		// catalog and a multi-turn history with tool_use/tool_result blocks.
		{"claude_code_shaped", claudeCodeShapedCountTokensBody()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			want := estimateAnthropicCountTokensInputLegacy(body)
			got := estimateAnthropicCountTokensInput(body)
			if got != want {
				t.Fatalf("estimate(%q) = %d, legacy = %d", tc.body, got, want)
			}
		})
	}
	t.Run("nil_body", func(t *testing.T) {
		if got, want := estimateAnthropicCountTokensInput(nil), estimateAnthropicCountTokensInputLegacy(nil); got != want {
			t.Fatalf("estimate(nil) = %d, legacy = %d", got, want)
		}
	})
}

// TestEstimateAnthropicCountTokensInputParityRandom compares the single-parse
// estimator against the legacy two-pass oracle on a deterministic corpus of
// randomly shaped bodies, including random whitespace and key orders.
func TestEstimateAnthropicCountTokensInputParityRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(20261116))
	randomString := func(rng *rand.Rand) string {
		alphabet := []rune("abc XYZ019<>&\"\\\n\t你好🌍")
		n := rng.Intn(12)
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		return b.String()
	}
	var randomValue func(depth int) any
	randomValue = func(depth int) any {
		kind := rng.Intn(8)
		if depth > 3 {
			kind = rng.Intn(4)
		}
		switch kind {
		case 0:
			return nil
		case 1:
			return rng.Intn(2) == 0
		case 2:
			return float64(rng.Intn(100000)) / 7
		case 3:
			return randomString(rng)
		case 4:
			n := rng.Intn(4)
			arr := make([]any, n)
			for i := range arr {
				arr[i] = randomValue(depth + 1)
			}
			return arr
		default:
			obj := map[string]any{}
			for i, n := 0, rng.Intn(4); i < n; i++ {
				obj[randomString(rng)] = randomValue(depth + 1)
			}
			return obj
		}
	}
	// The interesting keys appear often so the overhead branches are exercised.
	interestingKeys := []string{"system", "tools", "messages", "model", "max_tokens", "metadata"}
	for i := 0; i < 500; i++ {
		var body []byte
		switch rng.Intn(4) {
		case 0:
			obj := map[string]any{}
			for _, key := range interestingKeys {
				if rng.Intn(2) == 0 {
					obj[key] = randomValue(1)
				}
			}
			body, _ = json.Marshal(obj)
		case 1:
			body, _ = json.Marshal(randomValue(0))
		case 2:
			obj := map[string]any{}
			for _, key := range interestingKeys {
				if rng.Intn(2) == 0 {
					obj[key] = randomValue(1)
				}
			}
			body, _ = json.MarshalIndent(obj, "", "  ")
		default:
			// Raw shapes the generator cannot produce: overflow numbers and
			// malformed truncations of an otherwise valid body.
			base := `{"system":[{"type":"text","text":"s"}],"tools":[{"name":"t"}],"messages":[{"role":"user"},{"role":"assistant"}]}`
			switch rng.Intn(3) {
			case 0:
				body = []byte(`{"x":1e999,` + base[1:])
			case 1:
				body = []byte(base[:rng.Intn(len(base))])
			default:
				body = []byte(base + " junk")
			}
		}
		if got, want := estimateAnthropicCountTokensInput(body), estimateAnthropicCountTokensInputLegacy(body); got != want {
			t.Fatalf("iteration %d: estimate(%q) = %d, legacy = %d", i, body, got, want)
		}
	}
}

func claudeCodeShapedCountTokensBody() string {
	system := []map[string]any{
		{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
		{"type": "text", "text": strings.Repeat("Follow the repository conventions. ", 40), "cache_control": map[string]any{"type": "ephemeral"}},
	}
	tools := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		tools = append(tools, map[string]any{
			"name":        fmt.Sprintf("tool_%d", i),
			"description": strings.Repeat("Do something useful. ", 8),
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
			},
		})
	}
	messages := make([]map[string]any, 0, 16)
	for i := 0; i < 16; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{
			"role": role,
			"content": []map[string]any{
				{"type": "text", "text": strings.Repeat("turn content ", 20)},
			},
		})
	}
	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4",
		"max_tokens": 1024,
		"system":     system,
		"tools":      tools,
		"messages":   messages,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}
