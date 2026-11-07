package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
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
	fields := make([]string, 0, len(c.requestWarnings))
	for _, warning := range c.requestWarnings {
		fields = append(fields, warning.Field)
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

// convertResponseBody converts a successful upstream response back to the
// client protocol. Error responses are never converted: their bodies are
// provider-specific diagnostics, and routing them through the IR would
// replace a useful upstream error with an empty shell in the client
// protocol. They reach the client byte for byte, as before conversion
// existed.
func (c *protocolConversion) convertResponseBody(resp *http.Response) (*http.Response, error) {
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("protocol conversion: read upstream response: %w", err)
	}
	_ = resp.Body.Close()
	result, err := protocolconvert.ConvertResponse(body, c.upstreamProtocol, c.clientProtocol, c.wireModel, c.vertex)
	if err != nil {
		return nil, fmt.Errorf("protocol conversion: convert %s response to %s: %w", c.upstreamProtocol, c.clientProtocol, err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(result.Body))
	resp.ContentLength = int64(len(result.Body))
	if resp.Header != nil {
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(result.Body)))
		resp.Header.Del("Content-Encoding")
	}
	return resp, nil
}
