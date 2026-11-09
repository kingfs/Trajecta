package gemini

import (
	"context"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// DecodeResponse decodes a Gemini GenerateContentResponse. A provider error
// envelope is represented as Response.Error rather than returned as a Go
// error, because the HTTP exchange itself was decoded successfully.
func (c Codec) DecodeResponse(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Response, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	root, err := documentObject(doc)
	if err != nil {
		return nil, err
	}

	resp := &xir.Response{Ref: root, StopReason: xir.FinishUnknown}

	if modelVersion, _, err := stringField(root, "modelVersion", "$"); err != nil {
		return nil, err
	} else if modelVersion != "" {
		resp.Model = modelVersion
	} else if model, _, err := stringField(root, "model", "$"); err != nil {
		return nil, err
	} else {
		resp.Model = model
	}
	if responseID, _, err := stringField(root, "responseId", "$"); err != nil {
		return nil, err
	} else {
		resp.ID = responseID
	}

	if errorNode, ok, err := objectField(root, "error", "$"); err != nil {
		return nil, err
	} else if ok {
		resp.Error, err = decodeErrorNode(errorNode, "$.error")
		if err != nil {
			return nil, err
		}
		resp.StopReason = xir.FinishError
		resp.Status = "error"
		return resp, nil
	}

	candidates, ok, err := arrayField(root, "candidates", "$")
	if err != nil {
		return nil, err
	}
	if ok && len(candidates) > 0 {
		candidate := candidates[0]
		if candidate.Kind != wire.Object {
			return nil, fmt.Errorf("gemini: $.candidates[0]: expected object, got %s", kindName(candidate.Kind))
		}
		if err := decodeCandidate(candidate, "$.candidates[0]", resp); err != nil {
			return nil, err
		}
	}

	if promptFeedback, ok, err := objectField(root, "promptFeedback", "$"); err != nil {
		return nil, err
	} else if ok {
		if blockReason, _, err := stringField(promptFeedback, "blockReason", "$.promptFeedback"); err != nil {
			return nil, err
		} else if blockReason != "" {
			refusalRef, _ := promptFeedback.Get("blockReason")
			resp.Parts = append(resp.Parts, xir.Part{
				Kind: xir.PartRefusal,
				Text: blockReason,
				Ref:  refusalRef,
			})
			if resp.StopReason == xir.FinishUnknown {
				resp.StopReason = xir.FinishRefusal
				if resp.StopDetail == "" {
					resp.StopDetail = blockReason
				}
			}
		}
	}

	if resp.StopReason == xir.FinishStop && hasToolCall(resp.Parts) {
		// Gemini reports a completed function-call turn as STOP. The neutral
		// finish reason says the turn is asking the client to run a tool.
		resp.StopReason = xir.FinishToolUse
	}

	if usageNode, ok, err := objectField(root, "usageMetadata", "$"); err != nil {
		return nil, err
	} else if ok {
		resp.Usage, err = decodeUsage(usageNode, "$.usageMetadata")
		if err != nil {
			return nil, err
		}
	}

	resp.Extensions = extensionMap(root, responseKnownKeys)
	return resp, nil
}

func decodeCandidate(candidate *wire.Node, path string, resp *xir.Response) error {
	content, ok, err := objectField(candidate, "content", path)
	if err != nil {
		return err
	}
	if ok {
		parts, err := decodeContentParts(content, path+".content")
		if err != nil {
			return err
		}
		resp.Parts = append(resp.Parts, parts...)
	}

	finishReason, hasFinishReason, err := stringField(candidate, "finishReason", path)
	if err != nil {
		return err
	}
	if hasFinishReason {
		resp.StopReason = mapFinishReason(finishReason)
		if resp.StopReason == xir.FinishUnknown && finishReason != "" {
			resp.StopDetail = finishReason
		}
	}

	finishMessage, _, err := stringField(candidate, "finishMessage", path)
	if err != nil {
		return err
	}
	if finishMessage != "" {
		resp.StopDetail = finishMessage
	}
	return nil
}

func mapFinishReason(reason string) xir.FinishReason {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "STOP":
		return xir.FinishStop
	case "MAX_TOKENS":
		return xir.FinishLength
	case "SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return xir.FinishContentFilter
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS", "MISSING_THOUGHT_SIGNATURE":
		return xir.FinishError
	default:
		return xir.FinishUnknown
	}
}

func hasToolCall(parts []xir.Part) bool {
	for i := range parts {
		if parts[i].Kind == xir.PartToolCall && parts[i].ToolCall != nil {
			return true
		}
	}
	return false
}

func decodeUsage(node *wire.Node, path string) (*usage.Usage, error) {
	u := &usage.Usage{Source: "gemini"}
	if v, ok, err := intField(node, "promptTokenCount", path); err != nil {
		return nil, err
	} else if ok {
		u.InputTokens = v
	}
	if v, ok, err := intField(node, "candidatesTokenCount", path); err != nil {
		return nil, err
	} else if ok {
		u.OutputTokens = v
	}
	if v, ok, err := intField(node, "totalTokenCount", path); err != nil {
		return nil, err
	} else if ok {
		u.TotalTokens = v
	}
	if v, ok, err := intField(node, "cachedContentTokenCount", path); err != nil {
		return nil, err
	} else if ok {
		u.CacheReadTokens = v
	}
	if v, ok, err := intField(node, "thoughtsTokenCount", path); err != nil {
		return nil, err
	} else if ok {
		u.ReasoningTokens = v
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens + u.ReasoningTokens
	}
	return u, nil
}

func decodeErrorNode(node *wire.Node, path string) (*xir.Error, error) {
	code, _, err := intField(node, "code", path)
	if err != nil {
		return nil, err
	}
	message, _, err := stringField(node, "message", path)
	if err != nil {
		return nil, err
	}
	status, _, err := stringField(node, "status", path)
	if err != nil {
		return nil, err
	}
	return &xir.Error{
		Status:  code,
		Type:    "error",
		Code:    status,
		Message: message,
		Raw:     cloneRaw(node.Raw()),
	}, nil
}
