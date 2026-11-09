// Package responses implements the OpenAI Responses API codec for the
// Exchange IR (XIR).
//
// Decoding projects the request and response envelopes onto xir.Request and
// xir.Response. The item-based input is flattened into one xir.Message per
// input item so the original item order survives: message items become
// ordinary messages, function_call and function_call_output items become
// assistant/tool messages carrying a tool-call/tool-result part, and reasoning
// items become assistant messages carrying a reasoning part.
//
// Encoding re-emits the item-based Responses shape: flat function tools,
// function_call and function_call_output items, reasoning items and the
// response envelope with usage.
//
// Fields the XIR does not model are kept in Request.Extensions and
// Response.Extensions under the "responses" key as the raw JSON object of
// those members, so the same protocol can re-emit them. The two nested request
// options without a typed home, text.verbosity and reasoning.summary, are kept
// in Params.Extra under the dotted keys "text.verbosity" and
// "reasoning.summary".
//
// Sealed blocks are protocol-owned: a sealed part is only re-emitted when its
// Protocol is xir.ProtocolResponses.
package responses

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// ExtensionKey is the Request.Extensions / Response.Extensions key holding the
// Responses fields the semantic IR does not model. Keeping them per protocol
// means a later Responses encode can restore them while another protocol's
// encoder ignores them.
const ExtensionKey = "responses"

// Param keys used in xir.Params.Extra for nested request options the neutral
// Params type does not carry.
const (
	ParamTextVerbosity    = "text.verbosity"
	ParamReasoningSummary = "reasoning.summary"
)

// Codec is the OpenAI Responses API codec. The zero value is ready to use and
// safe for concurrent use.
type Codec struct{}

// New returns a Responses API codec.
func New() *Codec { return &Codec{} }

var _ xir.Codec = Codec{}

// Protocol reports the protocol the codec speaks.
func (Codec) Protocol() xir.Protocol { return xir.ProtocolResponses }

// requestModeledFields are the top-level request members the semantic model
// carries. Every other member is preserved in Extensions.
var requestModeledFields = []string{
	"model",
	"instructions",
	"input",
	"tools",
	"tool_choice",
	"max_output_tokens",
	"temperature",
	"top_p",
	"top_k",
	"stream",
	"text",
	"reasoning",
	"previous_response_id",
	"conversation",
	"prompt_cache_key",
	"metadata",
	"store",
	"parallel_tool_calls",
	"user",
}

// responseModeledFields are the top-level response members the semantic model
// carries. Every other member is preserved in Extensions.
var responseModeledFields = []string{
	"id",
	"object",
	"status",
	"model",
	"output",
	"usage",
	"error",
	"incomplete_details",
}

// DecodeRequest decodes one Responses API request body into the semantic IR.
// Unknown and missing optional fields are tolerated; only a nil document or a
// non-object root is an error.
func (Codec) DecodeRequest(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := documentRoot(doc, "request")
	if err != nil {
		return nil, err
	}

	req := &xir.Request{Ref: root}
	req.Model, _ = root.StringField("model")
	req.System = decodeInstructions(root)
	req.Messages = decodeInput(root)
	req.Tools = decodeTools(root)

	toolChoiceNode, _ := root.Get("tool_choice")
	var toolChoiceOK bool
	req.ToolChoice, toolChoiceOK = decodeToolChoice(toolChoiceNode)

	req.Params = decodeRequestParams(root)
	req.State = decodeState(root)
	req.Stream, _ = root.BoolField("stream")

	extras := collectExtras(root, requestModeledFields)
	// A tool_choice shape the IR cannot represent (allowed_tools, built-in
	// tool selectors) stays in Extensions instead of being dropped.
	if toolChoiceNode != nil && !toolChoiceOK {
		if extras == nil {
			extras = map[string]json.RawMessage{}
		}
		extras["tool_choice"] = cloneRaw(toolChoiceNode.Raw())
	}
	if len(extras) > 0 {
		if raw := marshalValue(extras); len(raw) > 0 {
			req.Extensions = map[string]json.RawMessage{ExtensionKey: raw}
		}
	}

	return req, nil
}

// DecodeResponse decodes one Responses API response body into the semantic IR.
// It also accepts the provider error envelope {"error": {...}}. Unknown and
// missing optional fields are tolerated.
func (Codec) DecodeResponse(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := documentRoot(doc, "response")
	if err != nil {
		return nil, err
	}

	resp := &xir.Response{Ref: root}
	resp.ID, _ = root.StringField("id")
	resp.Model, _ = root.StringField("model")
	resp.Status, _ = root.StringField("status")
	resp.Parts = decodeOutput(root)
	resp.Usage = decodeUsageNode(root)
	resp.Error = decodeErrorEnvelope(root)

	var incomplete string
	if details, ok := root.Get("incomplete_details"); ok && details.Kind == wire.Object {
		incomplete, _ = details.StringField("reason")
	}
	resp.StopReason, resp.StopDetail = mapStopReason(resp.Status, incomplete, resp.Parts, resp.Error)

	if extras := collectExtras(root, responseModeledFields); len(extras) > 0 {
		if raw := marshalValue(extras); len(raw) > 0 {
			resp.Extensions = map[string]json.RawMessage{ExtensionKey: raw}
		}
	}

	return resp, nil
}

// documentRoot validates and returns the root object of doc.
func documentRoot(doc *xir.Document, kind string) (*wire.Node, error) {
	if doc == nil {
		return nil, fmt.Errorf("responses: nil %s document", kind)
	}
	if doc.Root == nil {
		return nil, fmt.Errorf("responses: %s document has no root", kind)
	}
	if doc.Root.Kind != wire.Object {
		return nil, fmt.Errorf("responses: %s root is not a JSON object", kind)
	}
	return doc.Root, nil
}

// collectExtras returns every top-level member except the modeled ones. It
// returns nil when there is nothing left.
func collectExtras(root *wire.Node, modeled []string) map[string]json.RawMessage {
	if root == nil || root.Kind != wire.Object {
		return nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(root.Raw(), &all); err != nil {
		return nil
	}
	for _, key := range modeled {
		delete(all, key)
	}
	if len(all) == 0 {
		return nil
	}
	return all
}

// cloneRaw copies raw so a decoded value never aliases a caller's buffer.
func cloneRaw(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
