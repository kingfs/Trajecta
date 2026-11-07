package protocolconvert

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// Warning records one piece of information a conversion could not carry
// across. Conversion still succeeds; a caller that cannot tolerate the loss
// should check Warnings and reject the request instead.
type Warning struct {
	Field  string
	Reason string
}

func (w Warning) Error() string { return fmt.Sprintf("protocolconvert: %s: %s", w.Field, w.Reason) }

// Result is the outcome of one conversion. Body is the encoded request or
// response in the target protocol.
type Result struct {
	Body     []byte
	Warnings []Warning
	// From and To are the protocols the conversion bridged (equal for a
	// passthrough-shaped conversion, which callers should avoid: use Plan
	// and send the original bytes instead).
	From Protocol
	To   Protocol
}

// ConvertRequest converts a client request body from one protocol to another
// via the pkg/llm intermediate representation. model, when non-empty,
// overrides the model in the converted body (the upstream wire name); for
// Gemini the model also belongs in the request path (see Protocol.RequestPath).
func ConvertRequest(body []byte, from Protocol, to Protocol, model string, vertex bool) (Result, error) {
	from, to = canonical(from), canonical(to)
	if !from.Valid() || !to.Valid() {
		return Result{}, fmt.Errorf("protocolconvert: invalid protocol pair %q -> %q", from, to)
	}
	// Same-protocol conversion is byte passthrough. Routing a request through
	// the IR would silently drop every field the IR does not carry, which is
	// exactly the corruption passthrough exists to avoid.
	if from == to {
		return Result{Body: append([]byte(nil), body...), From: from, To: to}, nil
	}
	// Fail loudly instead of silently corrupting what the IR cannot carry.
	// Multimodal media, encrypted/thinking blocks and opaque signatures
	// have no IR representation; the pkg/llm adapters would turn them
	// into prose text or drop them. A caller that needs those shapes
	// must use a native passthrough target.
	if field, ok := unsupportedContentField(body); ok {
		return Result{}, fmt.Errorf("protocolconvert: %s request carries %q which the conversion IR cannot represent; use a native passthrough target", from, field)
	}
	parsed, err := llm.ParseRequest(ProviderFor(from, vertex), from.Endpoint(), body)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: parse %s request: %w", from, err)
	}
	if model != "" {
		parsed.Model = model
	}
	// Anthropic Messages requires max_tokens; other protocols make it
	// optional, so a converted request must supply the default itself.
	if to == ProtocolMessages && parsed.MaxTokens == nil {
		defaultMaxTokens := 4096
		parsed.MaxTokens = &defaultMaxTokens
	}
	adapter, err := llm.AdapterFor(ProviderFor(to, vertex), to.Endpoint())
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: encode %s request: %w", to, err)
	}
	out, err := adapter.MarshalRequest(parsed)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: encode %s request: %w", to, err)
	}
	// The IR has no Stream field, but stream is a client-visible behaviour,
	// not metadata: dropping it would turn a streaming call into a
	// non-streaming one. Re-inject it from the source body.
	if stream := bodyStreamFlag(body); stream {
		out, err = setBodyStreamFlag(out, true)
		if err != nil {
			return Result{}, fmt.Errorf("protocolconvert: preserve stream flag: %w", err)
		}
	}
	return Result{Body: out, Warnings: requestWarnings(parsed, from, to, body), From: from, To: to}, nil
}

func bodyStreamFlag(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Stream
}

func setBodyStreamFlag(body []byte, stream bool) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(stream)
	if err != nil {
		return nil, err
	}
	payload["stream"] = raw
	return json.Marshal(payload)
}

// ConvertResponse converts an upstream response body from one protocol to
// the client's protocol.
func ConvertResponse(body []byte, from Protocol, to Protocol, model string, vertex bool) (Result, error) {
	from, to = canonical(from), canonical(to)
	if !from.Valid() || !to.Valid() {
		return Result{}, fmt.Errorf("protocolconvert: invalid protocol pair %q -> %q", from, to)
	}
	if from == to {
		return Result{Body: append([]byte(nil), body...), From: from, To: to}, nil
	}
	parsed, err := llm.ParseResponse(ProviderFor(from, vertex), from.Endpoint(), body)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: parse %s response: %w", from, err)
	}
	if model != "" {
		parsed.Model = model
	}
	// The IR keeps tool calls in Candidate.ToolCalls, but the Anthropic
	// encoder only reads tool_use content blocks. Fold them in so a
	// converted tool call is not silently dropped.
	if to == ProtocolMessages {
		foldToolCallsIntoContent(&parsed)
	}
	adapter, err := llm.AdapterFor(ProviderFor(to, vertex), to.Endpoint())
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: encode %s response: %w", to, err)
	}
	out, err := adapter.MarshalResponse(parsed)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: encode %s response: %w", to, err)
	}
	return Result{Body: out, Warnings: responseWarnings(parsed, from, to), From: from, To: to}, nil
}

// requestWarnings reports the request fields conversion is known to drop.
// The IR carries model/system/messages/tools/tool_choice/stop/temperature/
// top_p/top_k/max_tokens; everything else in the source body is lost. We
// detect the common extras by scanning the raw JSON keys the IR ignores.
func requestWarnings(req llm.LLMRequest, from Protocol, to Protocol, body []byte) []Warning {
	var warnings []Warning
	if from == to {
		return nil
	}
	// Top-k has no Chat Completions/Responses counterpart in the IR encoders
	// beyond what they emit; flag it whenever the source set it and the
	// target is not Messages/Gemini.
	if req.TopK != nil && (to == ProtocolChatCompletions || to == ProtocolResponses) {
		warnings = append(warnings, Warning{Field: "top_k", Reason: fmt.Sprintf("top_k is not carried by %s encoding", to)})
	}
	// Generic: any top-level key the IR does not carry is lost. Report it so
	// a caller can reject or log instead of silently degrading (seed,
	// response_format, reasoning_effort, cache_control, metadata, ...).
	// Stream is re-injected and model is overridden, so skip both.
	for _, field := range UnsupportedFields(body, from) {
		lower := strings.ToLower(field)
		if lower == "stream" || lower == "model" {
			continue
		}
		warnings = append(warnings, Warning{Field: field, Reason: fmt.Sprintf("%s field is not carried by the conversion IR when converting to %s", field, to)})
	}
	return warnings
}

// unsupportedContentField scans a raw request body for content whose IR
// representation is lossy or absent. It returns the offending type/key when
// found. Thinking and opaque signatures are included deliberately: they are
// byte-exact protocol values (see pkg/secretmask/opaque.go) and re-encoding
// them as text would corrupt them.
func unsupportedContentField(body []byte) (string, bool) {
	// Cheap, robust prefilter before a full walk: these bytes cannot appear
	// in a text-only request without indicating a shape the IR drops.
	// All needles are quoted/protocol terms so ordinary prose mentioning
	// "image", "signature" or "thinking" does not trip the filter (in JSON
	// string content those words are escaped and never appear quoted).
	needles := []string{"image_url", "input_audio", "file_data", "inlineData", "inline_data", "\"image\"", "\"audio\"", "\"file\"", "\"document\"", "thoughtSignature", "encrypted_content", "redacted_thinking", "\"thinking\"", "\"reasoning\"", "reasoning_content", "\"signature\""}
	haystack := string(body)
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return needle, true
		}
	}
	// Gemini function parts are also dropped by the IR adapters.
	if strings.Contains(haystack, "functionCall") || strings.Contains(haystack, "functionResponse") {
		return "functionCall/functionResponse", true
	}
	return "", false
}

func responseWarnings(resp llm.LLMResponse, from Protocol, to Protocol) []Warning {
	if from == to {
		return nil
	}
	var warnings []Warning
	for _, candidate := range resp.Candidates {
		if candidate.Refusal != nil && to != ProtocolResponses && to != ProtocolChatCompletions {
			warnings = append(warnings, Warning{Field: "refusal", Reason: fmt.Sprintf("refusal text is not representable in %s responses", to)})
		}
		if len(candidate.ToolCalls) > 0 && to == ProtocolGemini {
			// The Gemini response encoder emits text parts only; tool calls
			// do not survive a conversion to Gemini.
			warnings = append(warnings, Warning{Field: "tool_calls", Reason: "tool calls are not representable in gemini responses"})
		}
	}
	return warnings
}

// foldToolCallsIntoContent appends each candidate's tool calls as tool_use
// content blocks, unless an equivalent block already exists (some parsers
// duplicate the call in both places).
func foldToolCallsIntoContent(resp *llm.LLMResponse) {
	for i := range resp.Candidates {
		candidate := &resp.Candidates[i]
		for _, call := range candidate.ToolCalls {
			exists := false
			for _, content := range candidate.Content {
				if content.Type == "tool_use" && content.ToolName == call.Name && content.ToolCallID == call.ID {
					exists = true
					break
				}
			}
			if exists {
				continue
			}
			args := call.Args
			if args == nil && call.ArgsText != "" {
				var decoded map[string]any
				if err := json.Unmarshal([]byte(call.ArgsText), &decoded); err == nil {
					args = decoded
				}
			}
			candidate.Content = append(candidate.Content, llm.LLMContent{
				Type:       "tool_use",
				ID:         call.ID,
				ToolCallID: call.ID,
				ToolName:   call.Name,
				ToolArgs:   args,
			})
		}
	}
}

// UnsupportedFields lists the top-level JSON keys present in body that the
// IR does not carry, so callers can surface what conversion would drop
// without parsing the body themselves. It is best-effort: a malformed body
// yields no fields.
func UnsupportedFields(body []byte, from Protocol) []string {
	carried := carriedKeys(canonical(from))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	var out []string
	for key := range raw {
		if !carried[strings.ToLower(key)] {
			out = append(out, key)
		}
	}
	return out
}

func carriedKeys(p Protocol) map[string]bool {
	base := map[string]bool{
		"model": true, "messages": true, "input": true, "instructions": true,
		"system": true, "tools": true, "tool_choice": true, "stop_sequences": true,
		"stop": true, "temperature": true, "top_p": true, "top_k": true,
		"max_tokens": true, "max_output_tokens": true, "stream": true,
		"contents": true, "generation_config": true, "safety_settings": true,
	}
	_ = p
	return base
}
