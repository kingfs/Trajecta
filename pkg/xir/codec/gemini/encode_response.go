package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

type responseWire struct {
	Candidates     []candidateWire `json:"candidates,omitempty"`
	PromptFeedback json.RawMessage `json:"promptFeedback,omitempty"`
	UsageMetadata  *usageWire      `json:"usageMetadata,omitempty"`
	ModelVersion   string          `json:"modelVersion,omitempty"`
	ResponseID     string          `json:"responseId,omitempty"`
}

type candidateWire struct {
	Content      contentWire `json:"content"`
	FinishReason string      `json:"finishReason,omitempty"`
}

type usageWire struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
}

// EncodeResponse encodes a neutral response as a Gemini
// GenerateContentResponse body.
func (c Codec) EncodeResponse(ctx context.Context, resp *xir.Response, opts xir.EncodeOptions) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("gemini: nil response")
	}
	if resp.Error != nil {
		return encodeErrorEnvelope(resp.Error)
	}

	extension, err := extensionObject(resp.Extensions)
	if err != nil {
		return nil, err
	}
	parts, err := encodeResponseParts(resp.Parts)
	if err != nil {
		return nil, err
	}
	promptFeedback, err := encodePromptFeedback(resp, extension)
	if err != nil {
		return nil, err
	}

	var candidates []candidateWire
	includeCandidate := len(parts) > 0
	if !includeCandidate && resp.StopReason != "" && resp.StopReason != xir.FinishUnknown && resp.StopReason != xir.FinishRefusal {
		includeCandidate = true
	}
	if includeCandidate {
		candidates = []candidateWire{{
			Content:      contentWire{Role: "model", Parts: parts},
			FinishReason: encodeFinishReason(resp),
		}}
	}

	var usageMetadata *usageWire
	if resp.Usage != nil {
		total := resp.Usage.TotalTokens
		if total == 0 {
			total = resp.Usage.InputTokens + resp.Usage.OutputTokens + resp.Usage.ReasoningTokens
		}
		usageMetadata = &usageWire{
			PromptTokenCount:        resp.Usage.InputTokens,
			CandidatesTokenCount:    resp.Usage.OutputTokens,
			TotalTokenCount:         total,
			CachedContentTokenCount: resp.Usage.CacheReadTokens,
			ThoughtsTokenCount:      resp.Usage.ReasoningTokens,
		}
	}

	return marshalWithExtensions(responseWire{
		Candidates:     candidates,
		PromptFeedback: promptFeedback,
		UsageMetadata:  usageMetadata,
		ModelVersion:   resp.Model,
		ResponseID:     resp.ID,
	}, extensionRaw(resp.Extensions), responseOwnedKeys)
}

func encodeResponseParts(parts []xir.Part) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(parts))
	for i := range parts {
		part := &parts[i]
		if part.Kind == xir.PartRefusal {
			// A blocked prompt is represented through promptFeedback, not as
			// a fabricated candidate part.
			continue
		}
		raw, ok, err := encodePart(part, nil)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, raw)
		}
	}
	return out, nil
}

func encodePromptFeedback(resp *xir.Response, extension map[string]json.RawMessage) (json.RawMessage, error) {
	if raw := bytes.TrimSpace(extension["promptFeedback"]); len(raw) > 0 {
		if !json.Valid(raw) || raw[0] != '{' {
			return nil, fmt.Errorf("gemini: promptFeedback extension must be a JSON object")
		}
		return cloneRaw(raw), nil
	}
	if resp.StopReason != xir.FinishRefusal {
		return nil, nil
	}
	reason := blockReason(resp.StopDetail)
	if reason == "" {
		for i := range resp.Parts {
			if resp.Parts[i].Kind == xir.PartRefusal {
				if candidate := blockReason(resp.Parts[i].Text); candidate != "" {
					reason = candidate
					break
				}
			}
		}
	}
	if reason == "" {
		reason = "OTHER"
	}
	raw, err := json.Marshal(map[string]string{"blockReason": reason})
	if err != nil {
		return nil, fmt.Errorf("gemini: encode promptFeedback: %w", err)
	}
	return raw, nil
}

func blockReason(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "BLOCK_REASON_UNSPECIFIED", "SAFETY", "OTHER", "BLOCKLIST", "PROHIBITED_CONTENT", "IMAGE_SAFETY":
		return strings.ToUpper(strings.TrimSpace(value))
	default:
		return ""
	}
}

func encodeFinishReason(resp *xir.Response) string {
	if detail := strings.ToUpper(strings.TrimSpace(resp.StopDetail)); isGeminiFinishReason(detail) {
		return detail
	}
	switch resp.StopReason {
	case xir.FinishStop, xir.FinishToolUse:
		// Gemini reports a function-call turn as STOP.
		return "STOP"
	case xir.FinishLength:
		return "MAX_TOKENS"
	case xir.FinishContentFilter, xir.FinishRefusal:
		return "SAFETY"
	case xir.FinishError:
		return "OTHER"
	default:
		return ""
	}
}

func isGeminiFinishReason(reason string) bool {
	switch reason {
	case "STOP", "MAX_TOKENS", "SAFETY", "RECITATION", "LANGUAGE", "OTHER", "BLOCKLIST",
		"PROHIBITED_CONTENT", "SPII", "MALFORMED_FUNCTION_CALL", "IMAGE_SAFETY",
		"UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS", "MISSING_THOUGHT_SIGNATURE", "FINISH_REASON_UNSPECIFIED":
		return true
	default:
		return false
	}
}

func encodeErrorEnvelope(geminiErr *xir.Error) ([]byte, error) {
	if geminiErr == nil {
		return nil, fmt.Errorf("gemini: nil error")
	}
	if raw := bytes.TrimSpace(geminiErr.Raw); len(raw) > 0 && json.Valid(raw) && raw[0] == '{' {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err == nil {
			if _, ok := object["error"]; ok {
				return cloneRaw(raw), nil
			}
			envelope, err := json.Marshal(map[string]json.RawMessage{"error": cloneRaw(raw)})
			if err != nil {
				return nil, fmt.Errorf("gemini: encode error envelope: %w", err)
			}
			return envelope, nil
		}
	}

	status := strings.TrimSpace(geminiErr.Code)
	if status == "" {
		if geminiErr.Type != "" && geminiErr.Type != "error" {
			status = geminiErr.Type
		} else {
			status = "UNKNOWN"
		}
	}
	code := geminiErr.Status
	if code == 0 {
		code = 500
	}
	body := struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	}{
		Code:    code,
		Message: geminiErr.Message,
		Status:  status,
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gemini: encode error body: %w", err)
	}
	envelope, err := json.Marshal(map[string]json.RawMessage{"error": rawBody})
	if err != nil {
		return nil, fmt.Errorf("gemini: encode error envelope: %w", err)
	}
	return envelope, nil
}
