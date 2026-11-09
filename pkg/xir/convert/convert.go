// Package convert implements the XIR protocol-conversion hub: a request or
// response in one wire protocol is decoded by its source codec, carried
// through the semantic Exchange IR, and encoded by the target codec.
//
// Same-protocol conversion is a byte-for-byte pass-through and never touches
// the semantic path. Streaming conversion is rejected with
// ErrStreamingConversion. Every known semantic loss is reported as an
// xir.Loss and the Policy decides whether a fatal loss aborts the conversion.
package convert

import (
	"bytes"
	"context"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
)

// Policy controls how conversion losses affect the result.
type Policy string

const (
	// Strict rejects a conversion that has any fatal loss. Non-fatal losses
	// are still reported in Result.Loss.
	Strict Policy = "strict"
	// Warn records every loss, including fatal ones, and returns the converted
	// body. It is the only policy that returns a body with a fatal loss.
	Warn Policy = "warn"
	// Drop ignores non-fatal losses (they are not reported) but still rejects
	// a fatal loss.
	Drop Policy = "drop"
)

// Options controls one conversion.
type Options struct {
	// Model overrides the model on the decoded request/response and on the
	// encoded target body. Empty preserves the source model.
	Model string
	// FromOperation overrides the source operation. Empty derives it from the
	// source protocol.
	FromOperation xir.Operation
	// ToOperation is the logical operation of the target exchange. Empty
	// derives it from the target protocol. It is carried for callers that
	// dispatch on the target operation; the four codecs encode the same body.
	ToOperation xir.Operation
	// Stream marks the exchange as streaming. A cross-protocol streaming
	// conversion is rejected; same-protocol raw bytes are passed through.
	Stream bool
	// Vertex selects the Vertex wire shape for a Gemini target.
	Vertex bool
	// Policy is the loss policy. The zero value is Strict.
	Policy Policy
	// Registry selects the codecs. Nil uses registry.Default().
	Registry *registry.Registry
}

// Result is one converted body plus its loss ledger.
type Result struct {
	// Bytes is the converted body. For a same-protocol conversion it is the
	// exact input slice, with no decode and no re-encode.
	Bytes []byte
	// Loss lists the semantic losses detected during conversion. Under Drop
	// non-fatal losses are omitted; under Warn fatal losses are included.
	Loss []xir.Loss
	// From and To identify the source and target protocols.
	From xir.Protocol
	To   xir.Protocol
	// Steps is the protocol path. A pass-through is [from]; a hub conversion
	// is [from, to].
	Steps []xir.Protocol
}

// ConvertRequest converts a non-streaming request body from one protocol to
// another. A same-protocol call returns raw unchanged (the same slice) and
// performs no JSON parse or semantic decode.
func ConvertRequest(ctx context.Context, raw []byte, from, to xir.Protocol, opts Options) (Result, error) {
	result := newResult(from, to)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if from == to {
		return passThrough(result, raw), nil
	}
	policy, err := normalPolicy(opts.Policy)
	if err != nil {
		return result, err
	}
	if opts.Stream {
		return result, &StreamingConversionError{From: from, To: to}
	}
	source, target, err := lookupCodecs(from, to, opts.Registry)
	if err != nil {
		return result, err
	}
	doc, err := xir.ParseDocument(from, operationFor(from, opts.FromOperation), raw)
	if err != nil {
		return result, fmt.Errorf("xir/convert: parse %s request: %w", from, err)
	}
	request, err := source.DecodeRequest(ctx, doc, decodeOptions())
	if err != nil {
		return result, fmt.Errorf("xir/convert: decode %s request: %w", from, err)
	}
	// Chat and Responses carry an in-body stream flag; Gemini carries it only
	// out of band, which Options.Stream covers.
	if opts.Stream || request.Stream {
		return result, &StreamingConversionError{From: from, To: to}
	}

	losses := requestLosses(from, to, request)
	result.Loss = losses
	if err := applyPolicy(policy, losses, &result); err != nil {
		return result, err
	}

	prepareRequest(request, to)
	if opts.Model != "" {
		request.Model = opts.Model
	}
	body, err := target.EncodeRequest(ctx, request, encodeOptions(opts))
	if err != nil {
		return result, fmt.Errorf("xir/convert: encode %s request: %w", to, err)
	}
	result.Bytes = body
	result.Steps = []xir.Protocol{from, to}
	return result, nil
}

// ConvertResponse converts a non-streaming response body from one protocol to
// another. Same-protocol bodies are passed through byte for byte. Usage is
// decoded by the source codec and re-emitted by the target codec.
func ConvertResponse(ctx context.Context, raw []byte, from, to xir.Protocol, opts Options) (Result, error) {
	result := newResult(from, to)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if from == to {
		return passThrough(result, raw), nil
	}
	policy, err := normalPolicy(opts.Policy)
	if err != nil {
		return result, err
	}
	if opts.Stream || looksStreaming(raw) {
		return result, &StreamingConversionError{From: from, To: to}
	}
	source, target, err := lookupCodecs(from, to, opts.Registry)
	if err != nil {
		return result, err
	}
	doc, err := xir.ParseDocument(from, operationFor(from, opts.FromOperation), raw)
	if err != nil {
		return result, fmt.Errorf("xir/convert: parse %s response: %w", from, err)
	}
	response, err := source.DecodeResponse(ctx, doc, decodeOptions())
	if err != nil {
		return result, fmt.Errorf("xir/convert: decode %s response: %w", from, err)
	}

	losses := responseLosses(from, to, response)
	result.Loss = losses
	if err := applyPolicy(policy, losses, &result); err != nil {
		return result, err
	}

	prepareResponse(response, to)
	if opts.Model != "" {
		response.Model = opts.Model
	}
	body, err := target.EncodeResponse(ctx, response, encodeOptions(opts))
	if err != nil {
		return result, fmt.Errorf("xir/convert: encode %s response: %w", to, err)
	}
	result.Bytes = body
	result.Steps = []xir.Protocol{from, to}
	return result, nil
}

func newResult(from, to xir.Protocol) Result {
	return Result{From: from, To: to, Steps: []xir.Protocol{from}}
}

func passThrough(result Result, raw []byte) Result {
	result.Bytes = raw
	result.Steps = []xir.Protocol{result.From}
	return result
}

func normalPolicy(policy Policy) (Policy, error) {
	switch policy {
	case "":
		return Strict, nil
	case Strict, Warn, Drop:
		return policy, nil
	default:
		return "", fmt.Errorf("xir/convert: unknown loss policy %q", policy)
	}
}

func lookupCodecs(from, to xir.Protocol, selected *registry.Registry) (xir.Codec, xir.Codec, error) {
	if selected == nil {
		selected = registry.Default()
	}
	source, ok := selected.Lookup(from)
	if !ok {
		return nil, nil, &CodecMissingError{Protocol: from, Direction: "source"}
	}
	target, ok := selected.Lookup(to)
	if !ok {
		return nil, nil, &CodecMissingError{Protocol: to, Direction: "target"}
	}
	return source, target, nil
}

func decodeOptions() xir.DecodeOptions {
	// Conversion needs every semantic view: parts and tools to detect what the
	// target cannot carry, usage to preserve it, state to reject hard affinity.
	return xir.DecodeOptions{NeedParts: true, NeedTools: true, NeedUsage: true, NeedState: true}
}

func encodeOptions(opts Options) xir.EncodeOptions {
	return xir.EncodeOptions{
		Model:  opts.Model,
		Vertex: opts.Vertex,
	}
}

func operationFor(protocol xir.Protocol, explicit xir.Operation) xir.Operation {
	if explicit != "" {
		return explicit
	}
	switch protocol {
	case xir.ProtocolChatCompletions:
		return xir.OperationChat
	case xir.ProtocolResponses:
		return xir.OperationResponses
	case xir.ProtocolMessages:
		return xir.OperationMessages
	case xir.ProtocolGemini:
		return xir.OperationGenerateContent
	default:
		return xir.OperationUnknown
	}
}

func looksStreaming(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	for _, prefix := range [][]byte{[]byte("data:"), []byte("event:"), []byte("id:")} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// applyPolicy filters the loss ledger and rejects fatal losses according to
// the selected policy. It mutates result.Loss but never result.Bytes.
func applyPolicy(policy Policy, losses []xir.Loss, result *Result) error {
	fatal := fatalLosses(losses)
	switch policy {
	case Warn:
		result.Loss = losses
		return nil
	case Drop:
		if len(fatal) > 0 {
			result.Loss = fatal
			return &FatalLossError{Losses: fatal}
		}
		result.Loss = nil
		return nil
	default: // Strict
		result.Loss = losses
		if len(fatal) > 0 {
			return &FatalLossError{Losses: fatal}
		}
		return nil
	}
}

func fatalLosses(losses []xir.Loss) []xir.Loss {
	var fatal []xir.Loss
	for _, loss := range losses {
		if loss.Fatal {
			fatal = append(fatal, loss)
		}
	}
	return fatal
}
