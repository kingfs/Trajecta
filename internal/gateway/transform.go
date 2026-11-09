package gateway

import (
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir"
)

// Converter is the request-side conversion a target may need. The proxy's
// protocolConversion implements it; the gateway only sees the stable methods.
type Converter interface {
	ConvertRequest([]byte) ([]byte, []protocolconvert.Warning, error)
	UpstreamPath() (string, error)
}

// Masker is the request-side stand-in renderer. *secretmask.Vault implements
// it. The interface exists so the transform can be tested without a Vault.
type Masker interface {
	Mask([]byte) ([]byte, []secretmask.MaskEvent)
}

// ConverterFactory builds a converter from the post-alias record body. It is
// called only after the alias rewrite, because conversion protocol selection
// depends on the wire model. Returning nil means native passthrough.
type ConverterFactory func(recordBody []byte) (Converter, error)

// RequestTransform is the fixed request-side order:
//
//	alias rewrite -> record body -> conversion -> masking -> wire body
//
// The record body is what the cassette records (post-alias, pre-conversion and
// pre-mask). The wire body is what the upstream receives.
type RequestTransform struct {
	RequestedModel string
	UpstreamModel  string
	ConverterFor   ConverterFactory
	Masker         Masker
}

// RequestResult is the outcome of one request transform.
type RequestResult struct {
	// Body keeps the client and final wire bytes as one explicit pair.
	Body *Body
	// RecordBody is the alias-rewritten body the recorder must keep.
	RecordBody []byte
	// WireBody is the body to send upstream (converted and/or masked).
	WireBody []byte
	// Converter is nil for a native passthrough or when no factory was set.
	Converter Converter
	// PathOverride is the upstream path a converter requires (Gemini model in
	// the path). Empty means keep the client path.
	PathOverride string
	// Warnings are the conversion's loss ledger entries.
	Warnings []protocolconvert.Warning
	// MaskEvents are the stand-ins rendered on the wire body.
	MaskEvents []secretmask.MaskEvent

	AliasApplied      bool
	ConversionApplied bool
	Masked            bool
}

// Apply runs the request transform. Alias-rewrite failures are non-fatal and
// fall back to the original bytes (the routing layer already decided the
// model); converter lookup and conversion failures are returned to the caller
// for the proxy's 4xx/failover classification. Masking never fails.
func (t RequestTransform) Apply(client []byte) (RequestResult, error) {
	body := NewBody(client)
	result := RequestResult{Body: body, RecordBody: client, WireBody: client}
	record := client
	if upstream := strings.TrimSpace(t.UpstreamModel); upstream != "" &&
		!strings.EqualFold(upstream, strings.TrimSpace(t.RequestedModel)) {
		if rewritten, ok, err := xir.RewriteModel(client, upstream); err == nil && ok {
			record = rewritten
			result.AliasApplied = true
		}
	}
	body.SetWire(record)
	result.RecordBody = body.WireBytes()
	result.WireBody = body.WireBytes()

	if t.ConverterFor != nil {
		converter, err := t.ConverterFor(record)
		if err != nil {
			return result, err
		}
		if converter != nil {
			converted, warnings, err := converter.ConvertRequest(record)
			if err != nil {
				return result, err
			}
			result.Converter = converter
			result.Warnings = warnings
			body.SetWire(converted)
			result.WireBody = body.WireBytes()
			result.ConversionApplied = true
			path, err := converter.UpstreamPath()
			if err != nil {
				return result, err
			}
			result.PathOverride = path
		}
	}

	if t.Masker != nil {
		masked, events := t.Masker.Mask(result.WireBody)
		if len(events) > 0 {
			if masked == nil {
				return result, fmt.Errorf("gateway: masker returned nil body with %d events", len(events))
			}
			body.SetWire(masked)
			result.WireBody = body.WireBytes()
			result.MaskEvents = events
			result.Masked = true
		}
	}
	return result, nil
}
