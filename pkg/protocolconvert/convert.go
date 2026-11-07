package protocolconvert

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// Warning records one piece of information a conversion could not carry
// across. Conversion still succeeds; Fatal marks the combinations the hot path
// must reject instead of silently degrading the request.
type Warning struct {
	Field  string
	Reason string
	Fatal  bool
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

// FatalWarnings returns the warnings a caller must treat as a failed
// conversion rather than a documented loss.
func (r Result) FatalWarnings() []Warning {
	var out []Warning
	for _, warning := range r.Warnings {
		if warning.Fatal {
			out = append(out, warning)
		}
	}
	return out
}

// ConvertRequest converts a client request body from one protocol to another
// via the pkg/llm intermediate representation. model, when non-empty,
// overrides the model in the converted body (the upstream wire name); for
// Gemini the model also belongs in the request path (see Protocol.RequestPath).
//
// Streaming conversion is deliberately not implemented: incrementally
// delivering a stream requires a frame-by-frame translator, so a streaming
// request must use a native passthrough target and this function rejects it.
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
	if bodyStreamFlag(body) {
		return Result{}, fmt.Errorf("protocolconvert: streaming requests cannot be converted; use a native passthrough target")
	}
	// Reject content the IR cannot represent instead of letting the adapters
	// turn it into prose or empty parts.
	if field, ok := unsupportedContentField(body, from); ok {
		return Result{}, fmt.Errorf("protocolconvert: %s request carries %q which the conversion IR cannot represent; use a native passthrough target", from, field)
	}
	parsed, err := llm.ParseRequest(ProviderFor(from, vertex), from.Endpoint(), body)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: parse %s request: %w", from, err)
	}
	if model != "" {
		parsed.Model = model
	}
	if parsed.ToolChoice == nil && hasUnparsedToolChoice(body) {
		return Result{}, fmt.Errorf("protocolconvert: %s request carries a tool_choice shape the conversion IR does not understand", from)
	}
	// Chat's newer max_completion_tokens spelling is not carried by the IR, but
	// it means exactly max_tokens; map it instead of dropping it.
	if parsed.MaxTokens == nil {
		if maxTokens := bodyMaxCompletionTokens(body); maxTokens != nil {
			parsed.MaxTokens = maxTokens
		}
	}
	// Anthropic Messages requires max_tokens; other protocols make it
	// optional, so a converted request must supply the default itself.
	if to == ProtocolMessages && parsed.MaxTokens == nil {
		defaultMaxTokens := 4096
		parsed.MaxTokens = &defaultMaxTokens
	}
	if err := validateToolFlow(parsed, to); err != nil {
		return Result{}, err
	}
	adapter, err := llm.AdapterFor(ProviderFor(to, vertex), to.Endpoint())
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: encode %s request: %w", to, err)
	}
	out, err := adapter.MarshalRequest(parsed)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: encode %s request: %w", to, err)
	}
	return Result{Body: out, Warnings: requestWarnings(parsed, from, to, body), From: from, To: to}, nil
}

func bodyStreamFlag(body []byte) bool {
	var payload struct {
		Stream any `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	switch value := payload.Stream.(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes":
			return true
		}
	case float64:
		return value != 0
	}
	return false
}

// hasUnparsedToolChoice reports a tool_choice value the adapters could not map.
// A missing value, null, the empty string and an empty object all mean "no
// directive"; anything else must become a ToolChoice or be rejected.
func hasUnparsedToolChoice(body []byte) bool {
	var payload struct {
		ToolChoice json.RawMessage `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(payload.ToolChoice))
	return trimmed != "" && trimmed != "null" && trimmed != `""` && trimmed != "{}"
}

func bodyMaxCompletionTokens(body []byte) *int {
	var payload struct {
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	if payload.MaxCompletionTokens == nil || *payload.MaxCompletionTokens <= 0 {
		return nil
	}
	return payload.MaxCompletionTokens
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
	applyFinishReason(&parsed, from, to)
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
// Fatal is set for fields whose loss changes the model's answer; the proxy
// turns those into a 4xx instead of silently degrading the request.
func requestWarnings(req llm.LLMRequest, from Protocol, to Protocol, body []byte) []Warning {
	var warnings []Warning
	if from == to {
		return nil
	}
	if req.TopK != nil && (to == ProtocolChatCompletions || to == ProtocolResponses) {
		warnings = append(warnings, Warning{Field: "top_k", Reason: fmt.Sprintf("top_k is not carried by %s encoding", to)})
	}
	if len(req.SafetySettings) > 0 && to != ProtocolGemini {
		warnings = append(warnings, Warning{Field: "safety_settings", Fatal: true, Reason: fmt.Sprintf("safety_settings cannot be enforced by %s; refusing to answer without them", to)})
	}
	if (to == ProtocolMessages || to == ProtocolGemini) && hasStrictTool(req.Tools) {
		warnings = append(warnings, Warning{Field: "tools.strict", Fatal: true, Reason: fmt.Sprintf("%s cannot enforce a strict tool schema; refusing to relax it silently", to)})
	}
	fatalFields := fatalRequestFields(body)
	for _, field := range fatalFields {
		warnings = append(warnings, Warning{Field: field, Fatal: true, Reason: fmt.Sprintf("%s is not carried by the conversion IR; refusing to silently drop it when converting to %s", field, to)})
	}
	// Report every remaining top-level key the IR does not carry. Stream and
	// model are handled explicitly, and the fatal fields are already named.
	fatalSet := make(map[string]struct{}, len(fatalFields))
	for _, field := range fatalFields {
		fatalSet[strings.ToLower(field)] = struct{}{}
	}
	for _, field := range UnsupportedFields(body, from) {
		lower := strings.ToLower(field)
		if lower == "stream" || lower == "model" {
			continue
		}
		if _, fatal := fatalSet[lower]; fatal {
			continue
		}
		warnings = append(warnings, Warning{Field: field, Reason: fmt.Sprintf("%s field is not carried by the conversion IR when converting to %s", field, to)})
	}
	return warnings
}

// fatalRequestFields reads the semantically significant fields a conversion
// would drop. A field set to its default value is not a loss, so booleans and
// numeric penalties are checked against their defaults rather than presence.
func fatalRequestFields(body []byte) []string {
	var probe struct {
		ResponseFormat    json.RawMessage `json:"response_format"`
		ReasoningEffort   json.RawMessage `json:"reasoning_effort"`
		Reasoning         json.RawMessage `json:"reasoning"`
		Thinking          json.RawMessage `json:"thinking"`
		Seed              *json.Number    `json:"seed"`
		PresencePenalty   *float64        `json:"presence_penalty"`
		FrequencyPenalty  *float64        `json:"frequency_penalty"`
		Logprobs          *bool           `json:"logprobs"`
		TopLogprobs       *int            `json:"top_logprobs"`
		ParallelToolCalls *bool           `json:"parallel_tool_calls"`
		Functions         json.RawMessage `json:"functions"`
		FunctionCall      json.RawMessage `json:"function_call"`
		Modalities        []string        `json:"modalities"`
		Audio             json.RawMessage `json:"audio"`
		WebSearchOptions  json.RawMessage `json:"web_search_options"`
		Prediction        json.RawMessage `json:"prediction"`
		LogitBias         json.RawMessage `json:"logit_bias"`
		N                 *int            `json:"n"`
		BestOf            *int            `json:"best_of"`
		Text              json.RawMessage `json:"text"`
		Background        *bool           `json:"background"`
		Truncation        string          `json:"truncation"`
		Conversation      json.RawMessage `json:"conversation"`
		Prompt            json.RawMessage `json:"prompt"`
		MaxToolCalls      *int            `json:"max_tool_calls"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	var fields []string
	nonEmpty := func(field string, raw json.RawMessage) {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" && trimmed != "null" && trimmed != "[]" && trimmed != "{}" {
			fields = append(fields, field)
		}
	}
	nonEmpty("response_format", probe.ResponseFormat)
	nonEmpty("reasoning_effort", probe.ReasoningEffort)
	nonEmpty("reasoning", probe.Reasoning)
	nonEmpty("thinking", probe.Thinking)
	if probe.Seed != nil {
		fields = append(fields, "seed")
	}
	if probe.PresencePenalty != nil && *probe.PresencePenalty != 0 {
		fields = append(fields, "presence_penalty")
	}
	if probe.FrequencyPenalty != nil && *probe.FrequencyPenalty != 0 {
		fields = append(fields, "frequency_penalty")
	}
	if probe.Logprobs != nil && *probe.Logprobs {
		fields = append(fields, "logprobs")
	}
	if probe.TopLogprobs != nil && *probe.TopLogprobs > 0 {
		fields = append(fields, "top_logprobs")
	}
	if probe.ParallelToolCalls != nil && !*probe.ParallelToolCalls {
		fields = append(fields, "parallel_tool_calls")
	}
	nonEmpty("functions", probe.Functions)
	nonEmpty("function_call", probe.FunctionCall)
	for _, modality := range probe.Modalities {
		if !strings.EqualFold(strings.TrimSpace(modality), "text") {
			fields = append(fields, "modalities")
			break
		}
	}
	nonEmpty("audio", probe.Audio)
	nonEmpty("web_search_options", probe.WebSearchOptions)
	nonEmpty("prediction", probe.Prediction)
	nonEmpty("logit_bias", probe.LogitBias)
	nonEmpty("conversation", probe.Conversation)
	nonEmpty("prompt", probe.Prompt)
	if probe.N != nil && *probe.N > 1 {
		fields = append(fields, "n")
	}
	if probe.BestOf != nil && *probe.BestOf > 1 {
		fields = append(fields, "best_of")
	}
	if probe.Background != nil && *probe.Background {
		fields = append(fields, "background")
	}
	if probe.MaxToolCalls != nil && *probe.MaxToolCalls > 0 {
		fields = append(fields, "max_tool_calls")
	}
	if truncation := strings.TrimSpace(probe.Truncation); truncation != "" && !strings.EqualFold(truncation, "disabled") {
		fields = append(fields, "truncation")
	}
	// Responses carries its output format under text.format; the default
	// {"format":{"type":"text"}} is not a loss. Any other knob (a structured
	// format, verbosity, ...) changes the output and is fatal.
	if raw := strings.TrimSpace(string(probe.Text)); raw != "" && raw != "null" && raw != "{}" {
		var text map[string]json.RawMessage
		if err := json.Unmarshal(probe.Text, &text); err != nil || len(text) != 1 {
			fields = append(fields, "text")
		} else if format, ok := text["format"]; !ok {
			fields = append(fields, "text")
		} else {
			var parsed struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(format, &parsed); err != nil {
				fields = append(fields, "text.format")
			} else if kind := strings.TrimSpace(parsed.Type); kind != "" && !strings.EqualFold(kind, "text") {
				fields = append(fields, "text.format")
			}
		}
	}
	return fields
}

func hasStrictTool(tools []llm.LLMTool) bool {
	for i := range tools {
		if tools[i].Strict != nil && *tools[i].Strict {
			return true
		}
	}
	return false
}

func responseWarnings(resp llm.LLMResponse, from Protocol, to Protocol) []Warning {
	if from == to {
		return nil
	}
	var warnings []Warning
	for _, candidate := range resp.Candidates {
		for _, content := range candidate.Content {
			if content.Type == "thinking" && content.Text != "" {
				warnings = append(warnings, Warning{Field: "reasoning", Reason: fmt.Sprintf("reasoning content is not carried to %s", to)})
				break
			}
		}
	}
	return warnings
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
		"max_tokens": true, "max_output_tokens": true, "max_completion_tokens": true,
		"stream": true, "contents": true, "generationconfig": true,
		"systeminstruction": true, "safetysettings": true,
		"toolconfig": true, "tool_config": true, "text": true,
	}
	_ = p
	return base
}

// validateToolFlow rejects a conversion whose tool calls and tool results the
// target encoder could not name, instead of dropping them.
func validateToolFlow(req llm.LLMRequest, to Protocol) error {
	if to != ProtocolGemini {
		return nil
	}
	names := make(map[string]string)
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			c := &req.Messages[i].Content[j]
			if c.Type != "tool_use" {
				continue
			}
			id := strings.TrimSpace(firstNonEmptyString(c.ToolCallID, c.ID))
			if id != "" && c.ToolName != "" {
				names[id] = c.ToolName
			}
		}
	}
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			c := &req.Messages[i].Content[j]
			if c.Type != "tool_result" {
				continue
			}
			id := strings.TrimSpace(firstNonEmptyString(c.ToolCallID, c.ID))
			name := strings.TrimSpace(c.ToolName)
			if name == "" {
				name = names[id]
			}
			if name == "" {
				return fmt.Errorf("protocolconvert: tool result %q has no tool name; Gemini functionResponse requires one, so the request cannot be converted", id)
			}
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
