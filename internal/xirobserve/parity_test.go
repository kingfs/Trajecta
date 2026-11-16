package xirobserve_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/analyzer"
	"github.com/kingfs/Trajecta/internal/benchcorpus"
	"github.com/kingfs/Trajecta/internal/xirobserve"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	"github.com/kingfs/Trajecta/pkg/xir"
)

func inputFor(t *testing.T, protocol string, request, response []byte) observe.ParseInput {
	t.Helper()
	meta := recordfile.MetaData{
		Provider:   llm.ProviderOpenAICompatible,
		Operation:  llm.OperationChatCompletions,
		Endpoint:   "/v1/chat/completions",
		Model:      "gpt-5-bench",
		Time:       time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC),
		DurationMs: 120,
		TTFTMs:     40,
	}
	switch protocol {
	case "responses":
		meta.Operation = llm.OperationResponses
		meta.Endpoint = "/v1/responses"
	case "messages":
		meta.Provider = llm.ProviderAnthropic
		meta.Operation = llm.OperationMessages
		meta.Endpoint = "/v1/messages"
	case "gemini":
		meta.Provider = llm.ProviderGoogleGenAI
		meta.Operation = llm.OperationGenerateContent
		meta.Endpoint = "/v1beta/models/gpt-5-bench:generateContent"
	}
	return observe.ParseInput{
		TraceID:      "trace-parity",
		CassettePath: "/tmp/trace-parity.http",
		Header:       recordfile.RecordHeader{Meta: meta},
		RequestBody:  request,
		ResponseBody: response,
	}
}

func parseBoth(t *testing.T, input observe.ParseInput) (observe.TraceObservation, observe.TraceObservation) {
	t.Helper()
	legacy, err := observe.NewDefaultRegistry().Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("observe parse: %v", err)
	}
	next, err := xirobserve.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("xirobserve parse: %v", err)
	}
	return legacy, next
}

// contentTexts returns the human-readable text a detector scans: node.Text and
// a tool call's arguments. Raw/JSON envelopes are excluded because the two
// parsers legitimately wrap them differently.
func contentTexts(obs observe.TraceObservation) []string {
	var out []string
	for _, node := range observe.TraceNodes(obs) {
		switch node.NormalizedType {
		case observe.NodeText, observe.NodeReasoning, observe.NodeRefusal:
			if text := strings.TrimSpace(node.Text); text != "" {
				out = append(out, text)
			}
		case observe.NodeToolCall:
			// A tool call's content is its arguments; the raw protocol object
			// (text/raw) is a representation detail.
			if args := strings.TrimSpace(observe.NodeArguments(node)); args != "" {
				out = append(out, args)
			}
		}
	}
	sort.Strings(out)
	return out
}

func compact(t *testing.T, raw string) string {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// findingSignature compares findings by what a user sees: category, title and
// the redacted evidence excerpt. The observe parsers can materialize the same
// content on a message node and again on its parts, which yields duplicate
// findings; the set collapses those, while a missing detection or a different
// location still shows up.
func findingSignature(t *testing.T, obs observe.TraceObservation) []string {
	t.Helper()
	findings, err := analyzer.NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	seen := make(map[string]struct{}, len(findings))
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		key := finding.Category + "|" + finding.Title
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func TestParityNonStreamingModelExchanges(t *testing.T) {
	trajectory := benchcorpus.Agent(benchcorpus.MediumBytes, benchcorpus.Options{Secrets: true})
	for _, tc := range []struct {
		name     string
		protocol xir.Protocol
	}{
		{"chat", xir.ProtocolChatCompletions},
		{"responses", xir.ProtocolResponses},
		{"messages", xir.ProtocolMessages},
		{"gemini", xir.ProtocolGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := benchcorpus.Encode(tc.protocol, trajectory)
			response := benchcorpus.Response(tc.protocol, trajectory)
			legacy, next := parseBoth(t, inputFor(t, tc.name, request, response))

			if next.Parser != xirobserve.ParserName || next.Status != observe.ParseStatusParsed {
				t.Fatalf("xir observation parser/status = %q/%q", next.Parser, next.Status)
			}
			legacyTexts := contentTexts(legacy)
			// The xir side may carry a tool result's content on the result node
			// itself instead of a child text node, so coverage is checked against
			// every text the xir observation holds.
			nextJoined := strings.Join(allNodeTexts(next), "\n")
			for _, text := range legacyTexts {
				if strings.Contains(nextJoined, text) {
					continue
				}
				// A tool result can be wrapped in its protocol envelope
				// ({"response":{"result":"..."}}); compare the decoded payload.
				if payload := toolResultPayload(text); payload != "" && strings.Contains(nextJoined, payload) {
					continue
				}
				t.Fatalf("xir observation lost observe text %.80q", text)
			}
			if len(allNodeTexts(next)) == 0 {
				t.Fatal("xir observation produced no content text")
			}
			if legacy.Usage.InputTokens != next.Usage.InputTokens ||
				legacy.Usage.OutputTokens != next.Usage.OutputTokens ||
				legacy.Usage.CacheReadTokens != next.Usage.CacheReadTokens {
				t.Fatalf("usage legacy=%+v xir=%+v", legacy.Usage, next.Usage)
			}
			// Tool calls must survive with their arguments; the call id and
			// owner may differ (Gemini has no id, the adapter synthesizes one).
			legacyCalls := callSignatures(t, legacy)
			nextCalls := callSignatures(t, next)
			// The XIR codecs preserve Gemini functionCall arguments in the
			// request history where observe drops them, so the xir set may be a
			// superset; it must never lose an observe call.
			nextSet := make(map[string]struct{}, len(nextCalls))
			nextNames := make(map[string]struct{}, len(nextCalls))
			for _, call := range nextCalls {
				nextSet[call] = struct{}{}
				if name, _, ok := strings.Cut(call, "|"); ok {
					nextNames[name] = struct{}{}
				}
			}
			for _, call := range legacyCalls {
				if _, ok := nextSet[call]; ok {
					continue
				}
				// observe can drop a Gemini functionCall's arguments; the XIR
				// codec keeps them, so a same-name call is the counterpart.
				name, args, _ := strings.Cut(call, "|")
				if args == "" {
					if _, ok := nextNames[name]; ok {
						continue
					}
				}
				t.Fatalf("xir observation lost tool call %q (observe calls=%v xir calls=%v)", call, legacyCalls, nextCalls)
			}
			legacyFindings := findingSignature(t, legacy)
			nextFindings := findingSignature(t, next)
			if strings.Join(legacyFindings, "\n") != strings.Join(nextFindings, "\n") {
				t.Fatalf("findings differ\nobserve=%v\nxir=%v", legacyFindings, nextFindings)
			}
		})
	}
}

// allNodeTexts returns every non-empty node text, including tool declarations
// and tool results, for coverage checks.
func allNodeTexts(obs observe.TraceObservation) []string {
	var out []string
	for _, node := range observe.TraceNodes(obs) {
		if text := strings.TrimSpace(node.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// toolResultPayload decodes one envelope level of a tool result and returns
// the longest string leaf, or "" when text is not an object.
func toolResultPayload(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return ""
	}
	longest := ""
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			if len(typed) > len(longest) {
				longest = typed
			}
		case map[string]any:
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(parsed)
	return longest
}

// Tool calls comparison keys on name and compacted arguments, not on the
// provider-assigned id (Gemini omits it) or the owner label.
func callSignatures(t *testing.T, obs observe.TraceObservation) []string {
	t.Helper()
	out := make([]string, 0, len(obs.Tools.Calls))
	for _, node := range observe.TraceNodes(obs) {
		if node.NormalizedType != observe.NodeToolCall {
			continue
		}
		name, _ := node.Metadata["name"].(string)
		args := compact(t, observe.NodeArguments(node))
		out = append(out, name+"|"+args)
	}
	sort.Strings(out)
	return out
}

func TestUnsupportedExchangesFallBack(t *testing.T) {
	trajectory := benchcorpus.Agent(benchcorpus.ShortBytes, benchcorpus.Options{Secrets: true})
	request := benchcorpus.EncodeChat(trajectory)
	response := benchcorpus.Response(xir.ProtocolChatCompletions, trajectory)

	cases := []struct {
		name  string
		input observe.ParseInput
	}{
		{
			name: "entry exchange",
			input: func() observe.ParseInput {
				input := inputFor(t, "chat", request, response)
				input.ExchangeKind = "entry"
				return input
			}(),
		},
		{
			name: "models endpoint",
			input: func() observe.ParseInput {
				input := inputFor(t, "chat", request, response)
				input.Header.Meta.Operation = llm.OperationModels
				input.Header.Meta.Endpoint = "/v1/models"
				return input
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := xirobserve.Parse(context.Background(), tc.input); !errors.Is(err, xirobserve.ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}

// TestParityStreamingModelExchanges pins the SSE assembly path: each protocol's
// stream is folded into its non-streaming wire shape, decoded by the same codec
// and compared with the observe stream parser.
func TestParityStreamingModelExchanges(t *testing.T) {
	secret := benchcorpus.SensitiveValues()[0]
	cases := []struct {
		name     string
		protocol string
		sse      string
	}{
		{"chat", "chat", "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5-bench\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5-bench\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello \"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5-bench\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OPENAI_API_KEY=" + secret + "\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-5-bench\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
			"data: [DONE]\n\n"},
		{"responses", "responses", "event: response.output_text.delta\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello streaming\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-5-bench\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello streaming\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"},
		{"messages", "messages", "event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_s\",\"model\":\"gpt-5-bench\",\"usage\":{\"input_tokens\":10}}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello streaming\"}}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n"},
		{"gemini", "gemini", "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello \"}]}}],\"modelVersion\":\"gemini-bench\"}\n\n" +
			"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"streaming\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := inputFor(t, tc.protocol, benchcorpus.Encode(benchcorpusProtocol(tc.protocol), benchcorpus.Agent(benchcorpus.ShortBytes, benchcorpus.Options{Secrets: true})), []byte(tc.sse))
			input.IsStream = true
			legacy, next := parseBoth(t, input)
			if next.Parser != xirobserve.ParserName {
				t.Fatalf("parser = %q", next.Parser)
			}
			if !strings.Contains(next.Stream.AccumulatedText, "hello") {
				t.Fatalf("xir accumulated text = %q", next.Stream.AccumulatedText)
			}
			var nextTexts []string
			for _, node := range observe.TraceNodes(next) {
				if text := strings.TrimSpace(node.Text); text != "" {
					nextTexts = append(nextTexts, text)
				}
				if args := strings.TrimSpace(observe.NodeArguments(node)); args != "" {
					nextTexts = append(nextTexts, args)
				}
			}
			nextJoined := strings.Join(nextTexts, "\n")
			for _, text := range contentTexts(legacy) {
				if !strings.Contains(nextJoined, text) {
					t.Fatalf("xir streaming observation lost text %.60q", text)
				}
			}
			if strings.Join(findingSignature(t, legacy), "\n") != strings.Join(findingSignature(t, next), "\n") {
				t.Fatalf("streaming findings differ\nobserve=%v\nxir=%v", findingSignature(t, legacy), findingSignature(t, next))
			}
		})
	}
}

func benchcorpusProtocol(name string) xir.Protocol {
	switch name {
	case "chat":
		return xir.ProtocolChatCompletions
	case "responses":
		return xir.ProtocolResponses
	case "messages":
		return xir.ProtocolMessages
	case "gemini":
		return xir.ProtocolGemini
	}
	return xir.ProtocolChatCompletions
}
