// Package trajectory reconstructs client-visible Responses sessions as ATIF.
// It never calls a model or changes the source cassettes.
package trajectory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type callLocation struct {
	step      int
	signature string
}
type builder struct {
	trajectory      Trajectory
	history         []item
	instructions    string
	calls           map[string]callLocation
	results         map[string]string
	warnings        []Warning
	exchange        Exchange
	model           string
	metricsRequests int
	requests        int
	sessionSource   string
	// stream, when non-nil, receives the increments the NDJSON exporter writes
	// as each trace is consumed. It is nil for the buffered Build path, which
	// keeps that path byte-for-byte unchanged.
	stream *streamEmitter
}

// Build uses one agent step per recorded model response, not per SSE item.
// Historical assistant items without a recorded response are explicitly recovered
// context: they acquire neither inferred usage nor an invented model identity.
func Build(ctx context.Context, sessionID, sessionSource string, exchanges []Exchange) (Trajectory, error) {
	if len(exchanges) == 0 {
		return Trajectory{}, errors.New("session has no recorded requests")
	}
	exchanges = append([]Exchange(nil), exchanges...)
	sort.SliceStable(exchanges, func(i, j int) bool {
		if exchanges[i].Time.Equal(exchanges[j].Time) {
			return exchanges[i].TraceID < exchanges[j].TraceID
		}
		return exchanges[i].Time.Before(exchanges[j].Time)
	})
	b := newBuilder(sessionID, sessionSource)
	for _, ex := range exchanges {
		if err := ctx.Err(); err != nil {
			return Trajectory{}, err
		}
		b.consume(ex)
	}
	b.finish()
	return b.trajectory, nil
}

func newBuilder(sessionID, sessionSource string) *builder {
	b := &builder{trajectory: Trajectory{SchemaVersion: SchemaVersion, SessionID: sessionID, Agent: Agent{Name: "unknown", Version: "unknown"}}, calls: map[string]callLocation{}, results: map[string]string{}, warnings: []Warning{}, sessionSource: sessionSource}
	if strings.Contains(sessionSource, "codex") {
		b.trajectory.Agent.Name = "codex"
	}
	return b
}

// consume folds one recorded exchange into the builder state. Callers are
// responsible for the ordering guarantee: exchanges arrive oldest first.
func (b *builder) consume(ex Exchange) {
	b.requests++
	if b.stream != nil {
		b.stream.beginTrace(ex.TraceID, len(b.trajectory.Steps))
	}
	b.exchange, b.model = ex, ex.Model
	b.identifyAgent(ex)
	if ex.Error != "" {
		b.gap("cassette_unavailable", ex.Error)
		return
	}
	var req item
	if json.Unmarshal(ex.Request, &req) != nil || req == nil {
		b.gap("invalid_request", "Unable to decode request JSON")
		return
	}
	if !strings.HasSuffix(strings.TrimRight(strings.Split(ex.Endpoint, "?")[0], "/"), "/responses") {
		b.gap("unsupported_endpoint", "Unsupported exchange; original content is available in the source trace")
		return
	}
	if s := str(req["model"]); s != "" {
		b.model = s
	}
	instructions := render(req["instructions"])
	if instructions != b.instructions {
		if instructions != "" {
			b.add(Step{Source: "system", Message: instructions, Extra: b.reference("request", "$.instructions")})
		} else {
			b.warning("instructions_removed")
		}
		b.instructions = instructions
	}
	input := items(req["input"])
	n := overlap(b.history, input)
	incremental := str(req["previous_response_id"]) != ""
	if incremental {
		n = 0
	}
	if len(b.history) > 0 && len(input) > 0 && n < min(len(b.history), len(input)) && !incremental {
		b.warning("context_discontinuity")
	}
	b.appendInput(input, n)
	response := parseResponse(ex.Response, ex.Stream)
	for _, w := range response.Warnings {
		b.warning(w)
	}
	if response.Model != "" {
		b.model = response.Model
	}
	if ex.StatusCode < 200 || ex.StatusCode >= 300 {
		b.warning("http_error")
	}
	// Even an empty or failed response remains visible as a recorded attempt.
	step := b.agentStep(response.Output, "response", "$.output")
	step.ModelName = b.model
	if ex.ExchangeKind != "entry" && ex.StatusCode >= 200 && ex.StatusCode < 300 {
		one := 1
		step.LLMCallCount = &one
	}
	step.Timestamp = timestamp(ex.Time.Add(time.Duration(ex.DurationMs) * time.Millisecond))
	step.Extra["timestamp_basis"] = "request_start_plus_recorded_duration"
	if ex.Time.IsZero() {
		step.Timestamp = ""
		delete(step.Extra, "timestamp_basis")
	}
	if response.ID != "" {
		step.Extra["response_id"] = response.ID
	}
	if response.Status != "" {
		step.Extra["response_status"] = response.Status
	}
	if ex.StatusCode < 200 || ex.StatusCode >= 300 {
		step.Extra["http_status"] = ex.StatusCode
	}
	step.Metrics = normalizeMetrics(response.Usage)
	if step.Metrics != nil {
		b.metricsRequests++
	}
	if reasoning, ok := req["reasoning"].(map[string]any); ok {
		step.ReasoningEffort = str(reasoning["effort"])
	}
	if len(response.Output) == 0 {
		b.warning("empty_response_output")
	}
	b.addAgent(step, response.Output)
	if incremental {
		b.history = append(b.history, input...)
	} else {
		b.history = append([]item(nil), input...)
	}
	b.history = append(b.history, response.Output...)
}

// finish closes the trajectory: every tool call without a result becomes a
// missing-result warning, and the session-level metrics and extras are set.
func (b *builder) finish() {
	for i := range b.trajectory.Steps {
		step := &b.trajectory.Steps[i]
		var missing []string
		for _, call := range step.ToolCalls {
			found := false
			if step.Observation != nil {
				for _, r := range step.Observation.Results {
					if r.SourceCallID == call.ToolCallID {
						found = true
					}
				}
			}
			if !found {
				missing = append(missing, call.ToolCallID)
			}
		}
		if len(missing) > 0 {
			traceID := str(step.Extra["trace_id"])
			b.warnings = append(b.warnings, Warning{Code: "missing_tool_result", TraceID: traceID})
			if b.stream != nil {
				// The step record is already on the wire, so the result gap is
				// reported as its own NDJSON record instead of a late mutation.
				b.stream.missing = append(b.stream.missing, StreamMissing{Type: "missing_tool_result", StepID: step.StepID, TraceID: traceID, CallIDs: missing})
			} else {
				step.Extra["missing_result_call_ids"] = missing
			}
		}
	}
	if len(b.trajectory.Steps) == 0 {
		b.gap("empty_trajectory", "No reconstructable conversation items")
	}
	b.trajectory.FinalMetrics = aggregateMetrics(b.trajectory.Steps)
	b.trajectory.FinalMetrics.Extra = map[string]any{"recorded_requests": b.requests, "requests_with_usage": b.metricsRequests, "usage_scope": "recorded_client_visible_responses"}
	b.trajectory.Extra = map[string]any{"exporter": "trajecta", "exporter_version": "2", "session_source": b.sessionSource, "scope": "client_visible_responses", "request_count": b.requests, "warnings": b.warnings, "has_warnings": len(b.warnings) > 0, "completion": "unknown", "ordering": "recorded_at_then_trace_id"}
}
func (b *builder) reference(origin, path string) map[string]any {
	return map[string]any{"trace_id": b.exchange.TraceID, "origin": origin, "path": path}
}
func (b *builder) warning(code string) {
	w := Warning{Code: code, TraceID: b.exchange.TraceID}
	b.warnings = append(b.warnings, w)
	if b.stream != nil {
		b.stream.warningAdded(w)
	}
}
func (b *builder) gap(code, message string) {
	b.warning(code)
	b.add(Step{Source: "system", Message: message, Extra: map[string]any{"trace_id": b.exchange.TraceID, "kind": code}})
}
func (b *builder) add(s Step) int {
	s.StepID = len(b.trajectory.Steps) + 1
	b.trajectory.Steps = append(b.trajectory.Steps, s)
	index := len(b.trajectory.Steps) - 1
	if b.stream != nil {
		b.stream.stepAdded(index, s)
	}
	return index
}
func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (b *builder) appendInput(input []item, start int) {
	for i := start; i < len(input); {
		it := input[i]
		path := fmt.Sprintf("$.input[%d]", i)
		if isAgentItem(it) {
			end := i + 1
			for end < len(input) && isAgentItem(input[end]) {
				end++
			}
			b.addAgent(b.agentStep(input[i:end], "request", path), input[i:end])
			i = end
			continue
		}
		typ := str(it["type"])
		if isToolResult(typ) {
			b.appendResult(it, path)
			i++
			continue
		}
		extra := b.reference("request", path)
		switch typ {
		case "message", "":
			source := "system"
			role := str(it["role"])
			if role == "user" {
				source = "user"
			} else if role != "system" && role != "developer" {
				b.warning("unknown_message_role")
			}
			if role == "developer" {
				extra["original_role"] = role
			}
			b.add(Step{Source: source, Message: contentText(it["content"]), Extra: extra})
		case "additional_tools":
			// Dynamic tool declarations are configuration, not an agent action.
			extra["kind"] = "tool_configuration_update"
			b.add(Step{Source: "system", Message: "Tool configuration updated; definitions are available in the source trace", Extra: extra})
		default:
			b.warning("unknown_item_type")
			extra["provider_type"] = typ
			b.add(Step{Source: "system", Message: "Unmapped input item; see source trace", Extra: extra})
		}
		i++
	}
}
func isToolCall(typ string) bool {
	switch typ {
	case "function_call", "custom_tool_call", "local_shell_call", "mcp_call", "web_search_call", "file_search_call", "computer_call", "code_interpreter_call":
		return true
	}
	return false
}
func isToolResult(typ string) bool { return strings.HasSuffix(typ, "_call_output") }
func isAgentItem(it item) bool {
	return str(it["role"]) == "assistant" || str(it["type"]) == "reasoning" || isToolCall(str(it["type"]))
}

func (b *builder) agentStep(output []item, origin, path string) Step {
	step := Step{Source: "agent", Message: "", Extra: b.reference(origin, path)}
	var messages, summaries, reasoning []string
	var ids []string
	for _, it := range output {
		typ := str(it["type"])
		if id := str(it["id"]); id != "" {
			ids = append(ids, id)
		}
		switch {
		case typ == "message" || typ == "":
			if text := contentText(it["content"]); text != "" {
				messages = append(messages, text)
			}
		case typ == "reasoning":
			if text := contentText(it["summary"]); text != "" {
				summaries = append(summaries, text)
			}
			if text := contentText(it["content"]); text != "" {
				reasoning = append(reasoning, text)
			}
			if str(it["encrypted_content"]) != "" {
				step.Extra["encrypted_reasoning_omitted"] = true
			}
		case typ == "response_error":
			messages = append(messages, "Response error: "+render(it["error"])+" "+render(it["incomplete_details"]))
		case isToolCall(typ):
			id := str(it["call_id"])
			if id == "" {
				id = str(it["id"])
			}
			if id == "" {
				id = fmt.Sprintf("%s:call:%d:%d", b.exchange.TraceID, len(b.trajectory.Steps)+1, len(step.ToolCalls))
				b.warning("synthetic_call_id")
			}
			if loc, ok := b.calls[id]; ok && origin == "request" && loc.signature == signature(it) {
				continue
			}
			if _, ok := b.calls[id]; ok {
				b.warning("reused_call_id")
			}
			args := map[string]any{}
			switch {
			case typ == "custom_tool_call":
				args["input"] = it["input"]
			case it["arguments"] != nil:
				if raw, ok := it["arguments"].(string); ok {
					if json.Unmarshal([]byte(raw), &args) != nil || args == nil {
						args = map[string]any{"raw_arguments": raw}
						b.warning("non_object_tool_arguments")
					}
				} else if obj, ok := it["arguments"].(map[string]any); ok {
					args = obj
				} else {
					args["raw_arguments"] = it["arguments"]
					b.warning("non_object_tool_arguments")
				}
			case it["action"] != nil:
				args["action"] = it["action"]
			}
			name := str(it["name"])
			if name == "" {
				name = typ
			}
			step.ToolCalls = append(step.ToolCalls, ToolCall{ToolCallID: id, FunctionName: name, Arguments: args})
			// Provider-executed calls can carry their result inline.
			if value, ok := it["output"]; ok {
				attach(&step, Result{SourceCallID: id, Content: contentText(value)})
			}
		default:
			b.warning("unknown_item_type")
			messages = append(messages, "[Unmapped output item: "+typ+"; see source trace]")
		}
	}
	step.Message = strings.Join(messages, "\n\n")
	step.ReasoningContent = strings.Join(reasoning, "\n\n")
	if len(summaries) > 0 {
		step.Extra["reasoning_summary"] = strings.Join(summaries, "\n\n")
	}
	if len(ids) > 0 {
		step.Extra["item_ids"] = ids
	}
	if origin == "request" {
		step.Extra["history_recovered"] = true
	}
	return step
}
func (b *builder) addAgent(step Step, output []item) {
	index := b.add(step)
	for _, call := range step.ToolCalls {
		sig := ""
		for _, it := range output {
			if str(it["call_id"]) == call.ToolCallID || str(it["id"]) == call.ToolCallID {
				sig = signature(it)
				break
			}
		}
		b.calls[call.ToolCallID] = callLocation{step: index, signature: sig}
		delete(b.results, call.ToolCallID)
	}
}
func attach(step *Step, result Result) {
	if step.Observation == nil {
		step.Observation = &Observation{}
	}
	step.Observation.Results = append(step.Observation.Results, result)
}
func (b *builder) appendResult(it item, path string) {
	id := str(it["call_id"])
	result := Result{Content: contentText(it["output"]), Extra: b.reference("request", path)}
	if loc, ok := b.calls[id]; ok && id != "" {
		if previous, exists := b.results[id]; exists {
			if previous != signature(it) {
				b.warning("conflicting_tool_result")
				b.add(Step{Source: "system", Message: "Conflicting repeated tool result", Observation: &Observation{Results: []Result{result}}, Extra: b.reference("request", path)})
			}
			return
		}
		result.SourceCallID = id
		b.attachResult(loc.step, result)
		b.results[id] = signature(it)
	} else {
		b.warning("orphan_tool_result")
		b.add(Step{Source: "system", Message: "Tool result without a recorded call", Observation: &Observation{Results: []Result{result}}, Extra: b.reference("request", path)})
	}
}

// attachResult attaches a tool result to the step that made the call. In
// streaming mode the step's record is already emitted, so the result is handed
// to the stream as its own record and only the call id is retained locally for
// the final missing-result pass; this keeps a late result from holding its
// payload in memory a second time.
func (b *builder) attachResult(stepIndex int, result Result) {
	if stepIndex < 0 || stepIndex >= len(b.trajectory.Steps) {
		return
	}
	if b.stream != nil {
		b.stream.resultAdded(stepIndex, result)
		attach(&b.trajectory.Steps[stepIndex], Result{SourceCallID: result.SourceCallID})
		return
	}
	attach(&b.trajectory.Steps[stepIndex], result)
}

// compactFrom drops the payload of every step emitted so far while keeping the
// small bookkeeping the builder still needs: tool-call ids, attached result call
// ids, metrics and the trace id used by final warnings.
func (b *builder) compactFrom(start int) {
	if start < 0 {
		start = 0
	}
	for i := start; i < len(b.trajectory.Steps); i++ {
		step := &b.trajectory.Steps[i]
		compact := Step{StepID: step.StepID, Source: step.Source, ToolCalls: step.ToolCalls, Metrics: step.Metrics, LLMCallCount: step.LLMCallCount}
		if traceID, ok := step.Extra["trace_id"]; ok {
			compact.Extra = map[string]any{"trace_id": traceID}
		}
		if step.Observation != nil && len(step.Observation.Results) > 0 {
			results := make([]Result, 0, len(step.Observation.Results))
			for _, result := range step.Observation.Results {
				results = append(results, Result{SourceCallID: result.SourceCallID})
			}
			compact.Observation = &Observation{Results: results}
		}
		*step = compact
	}
}
