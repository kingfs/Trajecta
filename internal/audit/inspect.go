// Package audit turns a recorded cassette back into the Exchange IR and the
// audit projection. It is the first internal consumer of pkg/xir/audit: the
// cassette remains the source of truth, the projection is derived on demand,
// and nothing here writes to the database.
package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kingfs/Trajecta/internal/detector"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
)

var (
	// ErrUnsupportedProtocol means the cassette's endpoint/provider does not
	// map to one of the four XIR protocols.
	ErrUnsupportedProtocol = errors.New("audit: unsupported cassette protocol")
)

// Result is one inspected cassette.
type Result struct {
	Header      recordfile.RecordHeader
	Protocol    xir.Protocol
	Operation   xir.Operation
	Streaming   bool
	Observation *audit.Observation
	// Detections are advisory sensitive-value spans from pkg/detect. They never
	// carry the matched value; the cassette remains the evidence source.
	Detections []Detection
	Warnings   []string
}

// Inspector decodes and projects cassettes. The zero value uses the default
// codec registry.
type Inspector struct {
	Registry *registry.Registry
	// Detectors, when set, supplies the audit detection engine from the
	// published detection-plugin snapshot. A nil value falls back to the builtin
	// secretmask scanner.
	Detectors *detector.Runtime
}

// New returns an Inspector with all four built-in codecs.
func New() *Inspector { return &Inspector{Registry: registry.Default()} }

// detectEngine resolves the point-scoped engine for the audit detection lane.
// A nil resolver makes detectObservation fall back to the builtin secretmask
// scanner on every point.
func (i *Inspector) detectEngine() engineResolver {
	if i != nil && i.Detectors != nil {
		return i.Detectors.Snapshot().AuditEngineFor
	}
	return nil
}

// InspectFile reads one cassette and returns its audit projection.
func (i *Inspector) InspectFile(ctx context.Context, path string) (*Result, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return i.InspectBytes(ctx, content)
}

// InspectBytes parses one cassette. A streaming response is reported with
// Streaming=true and only the request side is decoded; streamed response
// assembly belongs to the XIR stream layer, not to this projection yet.
func (i *Inspector) InspectBytes(ctx context.Context, content []byte) (*Result, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		return nil, fmt.Errorf("audit: parse cassette prelude: %w", err)
	}
	protocol, operation, ok := protocolFor(parsed.Header)
	if !ok {
		return nil, fmt.Errorf("%w: endpoint=%q provider=%q", ErrUnsupportedProtocol, parsed.Header.Meta.Endpoint, parsed.Header.Meta.Provider)
	}
	_, reqBody, _, resBody := recordfile.ExtractSections(content, parsed)
	reg := i.Registry
	if reg == nil {
		reg = registry.Default()
	}
	codec, ok := reg.Lookup(protocol)
	if !ok {
		return nil, fmt.Errorf("audit: no codec for %s", protocol)
	}
	reqDoc, err := xir.ParseDocument(protocol, operation, reqBody)
	if err != nil {
		return nil, fmt.Errorf("audit: parse %s request: %w", protocol, err)
	}
	request, err := codec.DecodeRequest(ctx, reqDoc, xir.DecodeOptions{NeedParts: true, NeedTools: true, NeedUsage: true, NeedState: true})
	if err != nil {
		return nil, fmt.Errorf("audit: decode %s request: %w", protocol, err)
	}
	streaming := parsed.Header.Layout.IsStream || looksLikeSSE(resBody)
	var response *xir.Response
	var warnings []string
	if streaming && protocol == xir.ProtocolChatCompletions && len(resBody) > 0 {
		response, err = decodeChatStream(ctx, resBody)
		if err != nil {
			warnings = append(warnings, "stream decode: "+err.Error())
			response = nil
		}
	} else if streaming {
		warnings = append(warnings, "streaming response is not decoded for "+string(protocol))
	} else if len(resBody) > 0 {
		resDoc, err := xir.ParseDocument(protocol, operation, resBody)
		if err != nil {
			return nil, fmt.Errorf("audit: parse %s response: %w", protocol, err)
		}
		response, err = codec.DecodeResponse(ctx, resDoc, xir.DecodeOptions{NeedParts: true, NeedUsage: true})
		if err != nil {
			return nil, fmt.Errorf("audit: decode %s response: %w", protocol, err)
		}
	}
	result := &Result{
		Header:      parsed.Header,
		Protocol:    protocol,
		Operation:   operation,
		Streaming:   streaming,
		Observation: audit.Build(protocol, operation, request, response),
		Warnings:    warnings,
	}
	detections, detectErr := detectObservation(ctx, result.Observation, i.detectEngine())
	if detectErr != nil {
		result.Warnings = append(result.Warnings, "detect: "+detectErr.Error())
	} else {
		result.Detections = detections
	}
	return result, nil
}

func protocolFor(header recordfile.RecordHeader) (xir.Protocol, xir.Operation, bool) {
	path := strings.ToLower(strings.TrimSpace(header.Meta.Endpoint))
	if path == "" {
		path = strings.ToLower(strings.TrimSpace(header.Meta.URL))
	}
	switch {
	case strings.Contains(path, "/chat/completions"):
		return xir.ProtocolChatCompletions, xir.OperationChat, true
	case strings.Contains(path, "/responses"):
		return xir.ProtocolResponses, xir.OperationResponses, true
	case strings.Contains(path, "/messages"):
		return xir.ProtocolMessages, xir.OperationMessages, true
	case strings.Contains(path, "generatecontent"):
		return xir.ProtocolGemini, xir.OperationGenerateContent, true
	}
	switch strings.ToLower(strings.TrimSpace(header.Meta.Provider)) {
	case "openai", "openai_compatible", "azure_openai", "vllm":
		return xir.ProtocolChatCompletions, xir.OperationChat, true
	case "anthropic":
		return xir.ProtocolMessages, xir.OperationMessages, true
	case "google_genai", "vertex_native":
		return xir.ProtocolGemini, xir.OperationGenerateContent, true
	}
	return "", "", false
}

func looksLikeSSE(body []byte) bool {
	body = []byte(strings.TrimSpace(string(body)))
	return strings.HasPrefix(string(body), "data:") || strings.HasPrefix(string(body), "event:")
}
