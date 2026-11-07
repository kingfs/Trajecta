package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/upstream"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

// protocolConversion is the per-request conversion context: the client
// protocol, the upstream protocol the selected target was chosen for, and
// the wire model name. It is non-nil only when conversion is enabled in
// config and Plan decided to convert for this selection.
type protocolConversion struct {
	clientProtocol   protocolconvert.Protocol
	upstreamProtocol protocolconvert.Protocol
	wireModel        string
	vertex           bool
	requestWarnings  []protocolconvert.Warning
	responseWarnings []protocolconvert.Warning
}

// conversionForSelection decides whether this selection needs conversion.
// It returns nil for the overwhelmingly common cases: conversion disabled,
// a non-convertible endpoint, or native passthrough. Correctness rules:
// streaming requests never convert (PlanForRequest rejects them), and a
// decision that is not convert leaves every byte untouched.
func (h *Handler) conversionForSelection(r *http.Request, selection *router.Selection, upstreamBodyBytes []byte) (*protocolConversion, error) {
	if h == nil || h.cfg == nil || !h.cfg.Router.Selection.ProtocolConversion {
		return nil, nil
	}
	if selection == nil || selection.Target == nil || r == nil || r.URL == nil {
		return nil, nil
	}
	// A compressed request body cannot be JSON-parsed for conversion.
	// Passthrough forwards it verbatim and works; conversion must fail
	// explicitly (and fast — see handler) instead of trying to parse
	// gzip bytes as JSON.
	if enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc != "" && enc != "identity" {
		return nil, fmt.Errorf("protocol conversion: compressed request body (Content-Encoding: %s) cannot be converted; use a native passthrough target", enc)
	}
	// A Gemini client can also ask for SSE via ?alt=sse on the
	// generateContent surface. The router only sees URL.Path, so it
	// cannot tell this from a non-streaming request. Refuse conversion
	// here (native passthrough still works) rather than buffer a stream.
	if q := r.URL.Query(); strings.EqualFold(q.Get("alt"), "sse") {
		return nil, nil
	}
	client, err := protocolconvert.DetectFromPath(r.URL.Path)
	if err != nil {
		// Endpoints without conversion vocabulary (models, embeddings,
		// tokenize) are never converted.
		return nil, nil
	}
	caps := selection.Target.Upstream.ProtocolCapabilitiesForModel(selection.Request.ModelName)
	decision, err := protocolconvert.PlanForRequest(protocolconvert.PlanRequest{
		Client: client,
		Caps:   caps,
		Mode:   protocolconvert.ModePassthroughPreferred,
		Stream: selection.Request.Stream,
	})
	if err != nil {
		return nil, err
	}
	if !decision.Convert() {
		return nil, nil
	}
	wireModel := llm.ModelFromBody(upstreamBodyBytes)
	if wireModel == "" {
		wireModel = selection.Request.ModelName
	}
	// P1: re-check Plan with the wire model actually used. The router
	// checked caps with the requested model name; alias rewriting may have
	// changed it, and OpenAI-compatible per-model supports_* overrides are
	// keyed by model. If the wire model is not served by any convertible
	// protocol, do not convert — fall back to native (which will fail
	// with the upstream's own error) rather than sending a body the
	// target cannot parse.
	if wireModel != selection.Request.ModelName {
		if caps2 := selection.Target.Upstream.ProtocolCapabilitiesForModel(wireModel); !caps2.Has(decision.UpstreamProtocol) {
			return nil, nil
		}
	}
	return &protocolConversion{
		clientProtocol:   decision.ClientProtocol,
		upstreamProtocol: decision.UpstreamProtocol,
		wireModel:        wireModel,
		vertex:           isVertexTarget(selection.Target),
	}, nil
}

func isVertexTarget(target *router.Target) bool {
	if target == nil {
		return false
	}
	switch target.Upstream.RoutingProfile {
	case upstream.RoutingProfileVertexExpress, upstream.RoutingProfileVertexProject:
		return true
	}
	return false
}

// protocolConversionEvent records the conversion in the cassette so a human
// reading a trace can see that the upstream bytes differ from the client
// bytes and why (including what the IR could not carry across).
func protocolConversionEvent(c *protocolConversion, start time.Time) recorder.RecordEvent {
	fields := make([]string, 0, len(c.requestWarnings)+len(c.responseWarnings))
	for _, warning := range c.requestWarnings {
		fields = append(fields, warning.Field)
	}
	for _, warning := range c.responseWarnings {
		fields = append(fields, "response:"+warning.Field)
	}
	return recorder.RecordEvent{
		Type: "protocol_conversion",
		Time: start,
		Attributes: map[string]interface{}{
			"client_protocol":   string(c.clientProtocol),
			"upstream_protocol": string(c.upstreamProtocol),
			"wire_model":        c.wireModel,
			"warning_fields":    fields,
		},
	}
}

// responseConversionEvent records the response-side warnings (the request
// event is created before the upstream call, when response warnings are
// still unknown).
func responseConversionEvent(c *protocolConversion, start time.Time) recorder.RecordEvent {
	fields := make([]string, 0, len(c.responseWarnings))
	for _, warning := range c.responseWarnings {
		fields = append(fields, warning.Field)
	}
	return recorder.RecordEvent{
		Type: "protocol_conversion_response",
		Time: start,
		Attributes: map[string]interface{}{
			"client_protocol":   string(c.clientProtocol),
			"upstream_protocol": string(c.upstreamProtocol),
			"warning_fields":    fields,
		},
	}
}

// convertRequestBody converts the wire request body. The caller passes the
// client-visible body; the result is what goes upstream. Conversion warnings
// are recorded on the conversion context for the cassette event.
func (c *protocolConversion) convertRequestBody(body []byte) ([]byte, error) {
	result, err := protocolconvert.ConvertRequest(body, c.clientProtocol, c.upstreamProtocol, c.wireModel, c.vertex)
	if err != nil {
		return nil, err
	}
	c.requestWarnings = result.Warnings
	return result.Body, nil
}

// upstreamPath is the path the converted request must be sent to. For
// Gemini the model travels in the path; the client query string is
// deliberately not carried over, because query options (beta flags,
// alt=sse) belong to the client protocol's surface.
func (c *protocolConversion) upstreamPath() string {
	return c.upstreamProtocol.RequestPath(c.wireModel, false, c.vertex)
}

// applyConvertedHeaders fixes protocol-scoped headers after a conversion.
// Authorization is handled separately by ApplyAuthHeaders (target profile),
// but version/beta/key headers belong to the client protocol's surface and
// must not leak, nor be missing, on the new surface.
func applyConvertedHeaders(outreq *http.Request, original *http.Request, conv *protocolConversion) {
	if outreq == nil || conv == nil {
		return
	}
	hdr := outreq.Header
	switch conv.upstreamProtocol {
	case protocolconvert.ProtocolMessages:
		// Messages requires anthropic-version; a Chat/Responses client
		// never sends it, so supply the default. (ApplyAuthHeaders only
		// fills it when missing, but that runs after this point.)
		if hdr.Get("anthropic-version") == "" {
			hdr.Set("anthropic-version", "2023-06-01")
		}
		// anthropic-beta is Messages-scoped: keep it only for Messages.
		if original.Header.Get("anthropic-beta") != "" {
			hdr.Set("anthropic-beta", original.Header.Get("anthropic-beta"))
		}
	default:
		// Anthropic-scoped headers must not leak to other protocols.
		hdr.Del("anthropic-version")
		hdr.Del("anthropic-beta")
	}
	if conv.upstreamProtocol == protocolconvert.ProtocolGemini {
		// Gemini clients authenticate via ?key= in the query, which the
		// conversion drops with the rest of the query string. Forward it
		// as the header form so a header-only gateway still authenticates.
		if key := original.URL.Query().Get("key"); key != "" && hdr.Get("x-goog-api-key") == "" {
			hdr.Set("x-goog-api-key", key)
		}
	}
	// The converted body is always JSON.
	hdr.Set("Content-Type", "application/json")
}

// convertResponseBody converts a successful upstream response back to the
// client protocol. Error responses are never converted: their bodies are
// provider-specific diagnostics, and routing them through the IR would
// replace a useful upstream error with an empty shell in the client
// protocol. They reach the client byte for byte, as before conversion
// existed. An empty successful body (e.g. 204) also passes through: there
// is nothing to translate, and failing the candidate over it would be a
// spurious 502. Conversion buffers the whole body, so it is capped at
// maxBytes (the same ceiling the request path uses) instead of growing
// without bound.
func (c *protocolConversion) convertResponseBody(resp *http.Response, maxBytes int64) (*http.Response, error) {
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("protocol conversion: read upstream response: %w", err)
	}
	_ = resp.Body.Close()
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("protocol conversion: upstream response exceeds %d bytes", maxBytes)
	}
	if len(body) == 0 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	result, err := protocolconvert.ConvertResponse(body, c.upstreamProtocol, c.clientProtocol, c.wireModel, c.vertex)
	if err != nil {
		return nil, fmt.Errorf("protocol conversion: convert %s response to %s: %w", c.upstreamProtocol, c.clientProtocol, err)
	}
	c.responseWarnings = result.Warnings
	resp.Body = io.NopCloser(bytes.NewReader(result.Body))
	resp.ContentLength = int64(len(result.Body))
	if resp.Header != nil {
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(result.Body)))
		resp.Header.Del("Content-Encoding")
	}
	return resp, nil
}
