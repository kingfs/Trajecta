// Package gemini implements the Google Gemini generateContent codec for the
// Exchange IR.
//
// The codec covers the non-streaming generateContent request and response
// bodies. Gemini carries the streaming choice in the HTTP method
// (streamGenerateContent) rather than in the JSON body, so a caller that knows
// it is handling an SSE exchange sets Request.Stream through RequestMeta.
//
// Decoding keeps the protocol-native pieces that the neutral model cannot
// express: a thought part and its thoughtSignature are kept as a reasoning part
// with a xir.Sealed block, inline and file media keep their bytes/URI, and the
// safety settings survive in Request.Extensions. Encoding only emits fields
// that Gemini actually defines; it never invents request members.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// ExtensionKey is the Request.Extensions / Response.Extensions key holding
// Gemini fields the semantic IR does not model. Keeping them per protocol
// means a later Gemini encode can restore them while a different protocol's
// encoder ignores them. safetySettings and promptFeedback live inside this
// object under their wire names.
const ExtensionKey = "gemini"

// Param keys used in xir.Params.Extra for generationConfig fields that the
// neutral Params type does not carry. Unknown generationConfig fields are
// stored under their original key; unknown thinkingConfig fields use the
// "thinkingConfig." prefix.
const (
	ParamResponseMimeType = "responseMimeType"
	ParamResponseSchema   = "responseSchema"
)

// RequestMeta carries request facts that are not part of the JSON body.
//
// Gemini puts the model in the URL path and the streaming choice in the RPC
// method, so a caller that has the HTTP request line can pass them here. The
// model in the body wins over Model and Path when present.
type RequestMeta struct {
	// Path is the request path, for example
	// /v1beta/models/gemini-2.5-pro:generateContent.
	Path string
	// Model is an explicit caller-supplied model name.
	Model string
	// Stream marks the exchange as an SSE streamGenerateContent call.
	Stream bool
}

// Codec is the Gemini generateContent codec. It is stateless and safe for
// concurrent use.
type Codec struct{}

var _ xir.Codec = Codec{}

// New returns a Gemini codec.
func New() *Codec { return &Codec{} }

// Protocol reports the protocol served by this codec.
func (Codec) Protocol() xir.Protocol { return xir.ProtocolGemini }

// requestKnownKeys are the request fields the codec models. Everything else
// is kept in Extensions[ExtensionKey] so a Gemini re-encode can restore it.
var requestKnownKeys = map[string]struct{}{
	"model":             {},
	"systemInstruction": {},
	"contents":          {},
	"tools":             {},
	"toolConfig":        {},
	"generationConfig":  {},
}

// responseKnownKeys are the response fields the codec models. promptFeedback
// and unknown future fields are kept in Extensions[ExtensionKey].
var responseKnownKeys = map[string]struct{}{
	"candidates":    {},
	"usageMetadata": {},
	"modelVersion":  {},
	"responseId":    {},
	"error":         {},
}

// requestOwnedKeys are rendered from the neutral request. An extension object
// may never overwrite them.
var requestOwnedKeys = map[string]struct{}{
	"contents":          {},
	"systemInstruction": {},
	"tools":             {},
	"toolConfig":        {},
	"generationConfig":  {},
}

// responseOwnedKeys are rendered from the neutral response.
var responseOwnedKeys = map[string]struct{}{
	"candidates":     {},
	"promptFeedback": {},
	"usageMetadata":  {},
	"modelVersion":   {},
	"responseId":     {},
}

// DecodeRequest decodes a Gemini request body. The model is taken from the
// body when present; use DecodeRequestWithMeta to supply it from the URL path
// and to mark the exchange as streaming.
func (c Codec) DecodeRequest(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Request, error) {
	return c.DecodeRequestWithMeta(ctx, doc, opts, RequestMeta{})
}

// DecodeRequestWithPath is DecodeRequest with a model hint from the request
// path.
func (c Codec) DecodeRequestWithPath(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions, path string) (*xir.Request, error) {
	return c.DecodeRequestWithMeta(ctx, doc, opts, RequestMeta{Path: path})
}

// DecodeRequestWithMeta is DecodeRequest with out-of-band request facts.
func (c Codec) DecodeRequestWithMeta(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions, meta RequestMeta) (*xir.Request, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	root, err := documentObject(doc)
	if err != nil {
		return nil, err
	}

	model, _, err := stringField(root, "model", "$")
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = strings.TrimSpace(meta.Model)
	}
	if model == "" {
		model = ModelFromPath(meta.Path)
	}

	req := &xir.Request{
		Model:  model,
		Stream: meta.Stream,
		Ref:    root,
	}

	systemInstruction, ok, err := objectField(root, "systemInstruction", "$")
	if err != nil {
		return nil, err
	}
	if ok {
		req.System, err = decodeContentParts(systemInstruction, "$.systemInstruction")
		if err != nil {
			return nil, err
		}
	}

	contents, ok, err := arrayField(root, "contents", "$")
	if err != nil {
		return nil, err
	}
	if ok {
		for i, item := range contents {
			path := fmt.Sprintf("$.contents[%d]", i)
			if item.Kind != wire.Object {
				return nil, fmt.Errorf("gemini: %s: expected content object, got %s", path, kindName(item.Kind))
			}
			roleText, _, err := stringField(item, "role", path)
			if err != nil {
				return nil, err
			}
			parts, err := decodeContentParts(item, path)
			if err != nil {
				return nil, err
			}
			req.Messages = append(req.Messages, xir.Message{
				Role:  decodeRole(roleText),
				Parts: parts,
				Ref:   item,
			})
		}
	}

	if req.Tools, err = decodeTools(root); err != nil {
		return nil, err
	}
	if req.ToolChoice, err = decodeToolChoice(root); err != nil {
		return nil, err
	}
	if err := decodeGenerationConfig(root, &req.Params); err != nil {
		return nil, err
	}
	if err := validateSafetySettings(root); err != nil {
		return nil, err
	}
	req.Extensions = extensionMap(root, requestKnownKeys)
	return req, nil
}

// ModelFromPath extracts a Gemini model name from a generateContent path.
//
// It accepts the v1beta / v1 shapes, Vertex publisher paths, and paths whose
// colon method has been URL-escaped. It returns "" when the path does not
// contain a model segment.
func ModelFromPath(path string) string {
	if path == "" {
		return ""
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}

	var rest string
	for _, marker := range []string{"/models/", "/tunedModels/"} {
		if index := strings.LastIndex(path, marker); index >= 0 {
			rest = path[index+len(marker):]
			break
		}
	}
	if rest == "" {
		for _, prefix := range []string{"models/", "tunedModels/"} {
			if strings.HasPrefix(path, prefix) {
				rest = path[len(prefix):]
				break
			}
		}
	}
	if rest == "" {
		// A bare model name is useful to callers that already stripped the
		// path, but a path without a model segment is not a model path.
		if strings.ContainsAny(path, "/:") {
			return ""
		}
		return path
	}
	if i := strings.IndexAny(rest, ":/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// decodeParts decodes a "parts" array. A missing array is not an error: a
// blocked candidate can have empty content.
func decodeContentParts(content *wire.Node, contentPath string) ([]xir.Part, error) {
	items, ok, err := arrayField(content, "parts", contentPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	parts := make([]xir.Part, 0, len(items))
	for i, item := range items {
		part, err := decodePart(item, fmt.Sprintf("%s.parts[%d]", contentPath, i))
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func documentObject(doc *xir.Document) (*wire.Node, error) {
	if doc == nil {
		return nil, fmt.Errorf("gemini: nil document")
	}
	if doc.Root == nil {
		return nil, fmt.Errorf("gemini: document has no root")
	}
	if doc.Root.Kind != wire.Object {
		return nil, fmt.Errorf("gemini: document root: expected object, got %s", kindName(doc.Root.Kind))
	}
	return doc.Root, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func decodeRole(role string) xir.Role {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return xir.RoleUser
	case "model":
		return xir.RoleModel
	case "assistant":
		return xir.RoleAssistant
	case "system":
		return xir.RoleSystem
	case "tool", "function":
		return xir.RoleTool
	case "":
		return xir.RoleUnknown
	default:
		return xir.RoleUnknown
	}
}

func encodeRole(role xir.Role) string {
	switch role {
	case xir.RoleUser:
		return "user"
	case xir.RoleAssistant, xir.RoleModel:
		return "model"
	case xir.RoleTool:
		return "user"
	default:
		return "user"
	}
}

func kindName(kind wire.Kind) string {
	switch kind {
	case wire.Object:
		return "object"
	case wire.Array:
		return "array"
	case wire.String:
		return "string"
	case wire.Number:
		return "number"
	case wire.Bool:
		return "bool"
	case wire.Null:
		return "null"
	default:
		return "unknown"
	}
}

func cloneRaw(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
