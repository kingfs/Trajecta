package xirobserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/xir"
	xirstream "github.com/kingfs/Trajecta/pkg/xir/stream"
)

// assembleStream folds an SSE body of one protocol into the non-streaming wire
// shape of that protocol (so the ordinary codec decodes it) plus the observe
// stream section (events, accumulated text/reasoning) the UI renders.
func assembleStream(protocol xir.Protocol, body []byte) ([]byte, observe.ObservationStream, error) {
	switch protocol {
	case xir.ProtocolChatCompletions:
		return assembleChatStream(body)
	case xir.ProtocolResponses:
		return assembleResponsesStream(body)
	case xir.ProtocolMessages:
		return assembleMessagesStream(body)
	case xir.ProtocolGemini:
		return assembleGeminiStream(body)
	default:
		return nil, observe.ObservationStream{}, ErrUnsupported
	}
}

func sseData(body []byte) ([][]byte, error) {
	decoder := xirstream.NewDecoder(bytes.NewReader(body))
	defer decoder.Close()
	var frames [][]byte
	for {
		frame, err := decoder.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		data := bytes.TrimSpace(frame.Data)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		frames = append(frames, append([]byte(nil), data...))
	}
	return frames, nil
}

func streamEvent(index int, eventType, providerType string, normalized observe.NormalizedType, delta string, raw []byte) observe.StreamEvent {
	return observe.StreamEvent{
		Index:          index,
		EventType:      eventType,
		ProviderType:   providerType,
		NormalizedType: normalized,
		Delta:          delta,
		JSON:           append(json.RawMessage(nil), raw...),
	}
}

func marshalWire(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("xirobserve: synthesize stream body: %w", err)
	}
	return raw, nil
}

// --- Chat Completions -------------------------------------------------------

type chatStreamTool struct {
	id   string
	name string
	args strings.Builder
}

func assembleChatStream(body []byte) ([]byte, observe.ObservationStream, error) {
	frames, err := sseData(body)
	if err != nil {
		return nil, observe.ObservationStream{}, err
	}
	var (
		id        string
		model     string
		finish    string
		text      strings.Builder
		reasoning strings.Builder
		tools     = map[int]*chatStreamTool{}
		toolOrder []int
		usage     map[string]any
		stream    observe.ObservationStream
	)
	for i, data := range frames {
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta.Content
			reason := choice.Delta.ReasoningContent
			if reason == "" {
				reason = choice.Delta.Reasoning
			}
			if delta != "" {
				text.WriteString(delta)
				stream.Events = append(stream.Events, streamEvent(i, "chat.completion.chunk", "content", observe.NodeText, delta, data))
			}
			if reason != "" {
				reasoning.WriteString(reason)
				stream.Events = append(stream.Events, streamEvent(i, "chat.completion.chunk", "reasoning_content", observe.NodeReasoning, reason, data))
			}
			for _, call := range choice.Delta.ToolCalls {
				tool, ok := tools[call.Index]
				if !ok {
					tool = &chatStreamTool{}
					tools[call.Index] = tool
					toolOrder = append(toolOrder, call.Index)
				}
				if call.ID != "" {
					tool.id = call.ID
				}
				if call.Function.Name != "" {
					tool.name = call.Function.Name
				}
				if call.Function.Arguments != "" {
					tool.args.WriteString(call.Function.Arguments)
				}
				stream.Events = append(stream.Events, streamEvent(i, "chat.completion.chunk", "tool_calls", observe.NodeToolCallDelta, call.Function.Arguments, data))
			}
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
		}
	}
	sort.Ints(toolOrder)
	var content any
	if text.Len() > 0 {
		content = text.String()
	}
	message := map[string]any{"role": "assistant", "content": content}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolOrder) > 0 {
		calls := make([]any, 0, len(toolOrder))
		for _, index := range toolOrder {
			tool := tools[index]
			calls = append(calls, map[string]any{
				"id": tool.id, "type": "function",
				"function": map[string]any{"name": tool.name, "arguments": tool.args.String()},
			})
		}
		message["tool_calls"] = calls
	}
	choice := map[string]any{"index": 0, "message": message}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	final := map[string]any{
		"id": id, "object": "chat.completion", "model": model,
		"choices": []any{choice},
	}
	if usage != nil {
		final["usage"] = usage
	}
	stream.AccumulatedText = text.String()
	stream.AccumulatedReasoning = reasoning.String()
	raw, err := marshalWire(final)
	return raw, stream, err
}

// --- Responses --------------------------------------------------------------

func assembleResponsesStream(body []byte) ([]byte, observe.ObservationStream, error) {
	frames, err := sseData(body)
	if err != nil {
		return nil, observe.ObservationStream{}, err
	}
	var (
		final    map[string]json.RawMessage
		finalRaw []byte
		text     strings.Builder
		reason   strings.Builder
		stream   observe.ObservationStream
	)
	for i, data := range frames {
		var frame struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Text     string          `json:"text"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "response.output_text.delta":
			text.WriteString(frame.Delta)
			stream.Events = append(stream.Events, streamEvent(i, frame.Type, "output_text", observe.NodeText, frame.Delta, data))
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reason.WriteString(frame.Delta)
			stream.Events = append(stream.Events, streamEvent(i, frame.Type, "reasoning", observe.NodeReasoning, frame.Delta, data))
		case "response.completed", "response.incomplete", "response.failed":
			if len(frame.Response) > 0 {
				if err := json.Unmarshal(frame.Response, &final); err != nil {
					return nil, stream, fmt.Errorf("xirobserve: decode responses completion: %w", err)
				}
				finalRaw = append([]byte(nil), frame.Response...)
			}
		}
	}
	if finalRaw == nil {
		// Providers that omit the terminal envelope: synthesize one message
		// item from the accumulated deltas.
		final = map[string]json.RawMessage{}
		var output []any
		if text.Len() > 0 {
			output = append(output, map[string]any{
				"type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}},
			})
		}
		raw, err := json.Marshal(output)
		if err != nil {
			return nil, stream, err
		}
		final["output"] = raw
		raw, err = marshalWire(map[string]any{"status": "completed", "output": output})
		if err != nil {
			return nil, stream, err
		}
		finalRaw = raw
	}
	stream.AccumulatedText = text.String()
	stream.AccumulatedReasoning = reason.String()
	return finalRaw, stream, nil
}

// --- Anthropic Messages -----------------------------------------------------

type messageStreamBlock struct {
	kind      string
	text      strings.Builder
	thinking  strings.Builder
	signature string
	id        string
	name      string
	args      strings.Builder
}

func assembleMessagesStream(body []byte) ([]byte, observe.ObservationStream, error) {
	frames, err := sseData(body)
	if err != nil {
		return nil, observe.ObservationStream{}, err
	}
	var (
		id         string
		model      string
		stopReason string
		blocks     = map[int]*messageStreamBlock{}
		order      []int
		inputTok   int
		outputTok  int
		text       strings.Builder
		reason     strings.Builder
		stream     observe.ObservationStream
	)
	for i, data := range frames {
		var frame struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type     string `json:"type"`
				ID       string `json:"id"`
				Name     string `json:"name"`
				Thinking string `json:"thinking"`
				Text     string `json:"text"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			MessageDelta struct {
				StopReason string `json:"stop_reason"`
			} `json:"message_delta"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "message_start":
			id = frame.Message.ID
			model = frame.Message.Model
			inputTok = frame.Message.Usage.InputTokens
		case "content_block_start":
			block := &messageStreamBlock{kind: frame.ContentBlock.Type}
			block.id = frame.ContentBlock.ID
			block.name = frame.ContentBlock.Name
			if frame.ContentBlock.Text != "" {
				block.text.WriteString(frame.ContentBlock.Text)
			}
			if frame.ContentBlock.Thinking != "" {
				block.thinking.WriteString(frame.ContentBlock.Thinking)
			}
			blocks[frame.Index] = block
			order = append(order, frame.Index)
		case "content_block_delta":
			block := blocks[frame.Index]
			if block == nil {
				block = &messageStreamBlock{kind: "text"}
				blocks[frame.Index] = block
				order = append(order, frame.Index)
			}
			switch frame.Delta.Type {
			case "text_delta":
				block.text.WriteString(frame.Delta.Text)
				text.WriteString(frame.Delta.Text)
				stream.Events = append(stream.Events, streamEvent(i, frame.Type, "text_delta", observe.NodeText, frame.Delta.Text, data))
			case "thinking_delta":
				block.thinking.WriteString(frame.Delta.Thinking)
				reason.WriteString(frame.Delta.Thinking)
				stream.Events = append(stream.Events, streamEvent(i, frame.Type, "thinking_delta", observe.NodeReasoning, frame.Delta.Thinking, data))
			case "signature_delta":
				block.signature = frame.Delta.Signature
			case "input_json_delta":
				block.args.WriteString(frame.Delta.PartialJSON)
				stream.Events = append(stream.Events, streamEvent(i, frame.Type, "input_json_delta", observe.NodeToolCallDelta, frame.Delta.PartialJSON, data))
			}
		case "message_delta":
			if frame.MessageDelta.StopReason != "" {
				stopReason = frame.MessageDelta.StopReason
			}
			if frame.Usage.OutputTokens > 0 {
				outputTok = frame.Usage.OutputTokens
			}
		}
	}
	sort.Ints(order)
	content := make([]any, 0, len(order))
	for _, index := range order {
		block := blocks[index]
		switch block.kind {
		case "thinking":
			blockJSON := map[string]any{"type": "thinking", "thinking": block.thinking.String()}
			if block.signature != "" {
				blockJSON["signature"] = block.signature
			}
			content = append(content, blockJSON)
		case "tool_use":
			var input any = map[string]any{}
			if raw := strings.TrimSpace(block.args.String()); raw != "" {
				_ = json.Unmarshal([]byte(raw), &input)
			}
			content = append(content, map[string]any{"type": "tool_use", "id": block.id, "name": block.name, "input": input})
		default:
			content = append(content, map[string]any{"type": "text", "text": block.text.String()})
		}
	}
	if len(content) == 0 && text.Len() > 0 {
		content = append(content, map[string]any{"type": "text", "text": text.String()})
	}
	final := map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content":     content,
		"stop_reason": stopReason,
		"usage":       map[string]any{"input_tokens": inputTok, "output_tokens": outputTok},
	}
	stream.AccumulatedText = text.String()
	stream.AccumulatedReasoning = reason.String()
	raw, err := marshalWire(final)
	return raw, stream, err
}

// --- Gemini generateContent -------------------------------------------------

func assembleGeminiStream(body []byte) ([]byte, observe.ObservationStream, error) {
	frames, err := sseData(body)
	if err != nil {
		return nil, observe.ObservationStream{}, err
	}
	var (
		modelVersion string
		finish       string
		text         strings.Builder
		parts        []any
		usage        map[string]any
		stream       observe.ObservationStream
	)
	for i, data := range frames {
		var chunk struct {
			Candidates []struct {
				Content struct {
					Role  string `json:"role"`
					Parts []struct {
						Text         string          `json:"text"`
						FunctionCall json.RawMessage `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata map[string]any `json:"usageMetadata"`
			ModelVersion  string         `json:"modelVersion"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}
		if chunk.ModelVersion != "" {
			modelVersion = chunk.ModelVersion
		}
		if chunk.UsageMetadata != nil {
			usage = chunk.UsageMetadata
		}
		for _, candidate := range chunk.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					text.WriteString(part.Text)
					stream.Events = append(stream.Events, streamEvent(i, "generateContent.chunk", "text", observe.NodeText, part.Text, data))
				}
				if len(part.FunctionCall) > 0 {
					var call map[string]any
					if err := json.Unmarshal(part.FunctionCall, &call); err == nil {
						parts = append(parts, map[string]any{"functionCall": call})
					}
				}
			}
			if candidate.FinishReason != "" {
				finish = candidate.FinishReason
			}
		}
	}
	if text.Len() > 0 {
		parts = append([]any{map[string]any{"text": text.String()}}, parts...)
	}
	candidate := map[string]any{
		"content": map[string]any{"role": "model", "parts": parts},
		"index":   0,
	}
	if finish != "" {
		candidate["finishReason"] = finish
	}
	final := map[string]any{"candidates": []any{candidate}}
	if usage != nil {
		final["usageMetadata"] = usage
	}
	if modelVersion != "" {
		final["modelVersion"] = modelVersion
	}
	stream.AccumulatedText = text.String()
	raw, err := marshalWire(final)
	return raw, stream, err
}
