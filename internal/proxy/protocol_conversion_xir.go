package proxy

import (
	"context"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
	"github.com/kingfs/Trajecta/pkg/xir"
	xirconvert "github.com/kingfs/Trajecta/pkg/xir/convert"
)

// convertRequestBodyXIR converts a request through the Exchange IR hub. It is
// the opt-in `protocol_conversion_engine: xir` path; the legacy pkg/llm path
// remains the default while the two engines are compared.
func (c *protocolConversion) convertRequestBodyXIR(body []byte) ([]byte, []protocolconvert.Warning, error) {
	from, to, err := xirProtocols(c.clientProtocol, c.upstreamProtocol)
	if err != nil {
		return nil, nil, &protocolConversionRequestError{err: err}
	}
	result, err := xirconvert.ConvertRequest(context.Background(), body, from, to, xirconvert.Options{
		Model:  c.wireModel,
		Vertex: c.vertex,
		Policy: xirconvert.Strict,
	})
	if err != nil {
		return nil, nil, &protocolConversionRequestError{err: err}
	}
	return result.Bytes, xirWarnings(result.Loss), nil
}

// convertResponseBodyXIR converts a successful upstream response back to the
// client protocol through the Exchange IR hub.
func (c *protocolConversion) convertResponseBodyXIR(body []byte) ([]byte, []protocolconvert.Warning, error) {
	from, to, err := xirProtocols(c.upstreamProtocol, c.clientProtocol)
	if err != nil {
		return nil, nil, fmt.Errorf("protocol conversion: %w", err)
	}
	result, err := xirconvert.ConvertResponse(context.Background(), body, from, to, xirconvert.Options{
		Model:  c.wireModel,
		Vertex: c.vertex,
		Policy: xirconvert.Strict,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("protocol conversion: convert %s response to %s: %w", from, to, err)
	}
	return result.Bytes, xirWarnings(result.Loss), nil
}

func xirProtocols(from, to protocolconvert.Protocol) (xir.Protocol, xir.Protocol, error) {
	convertOne := func(p protocolconvert.Protocol) (xir.Protocol, error) {
		switch p {
		case protocolconvert.ProtocolChatCompletions:
			return xir.ProtocolChatCompletions, nil
		case protocolconvert.ProtocolResponses:
			return xir.ProtocolResponses, nil
		case protocolconvert.ProtocolMessages:
			return xir.ProtocolMessages, nil
		case protocolconvert.ProtocolGemini, protocolconvert.ProtocolGenerateContent:
			return xir.ProtocolGemini, nil
		default:
			return "", fmt.Errorf("protocol conversion: unsupported protocol %q in XIR engine", p)
		}
	}
	fromXIR, err := convertOne(from)
	if err != nil {
		return "", "", err
	}
	toXIR, err := convertOne(to)
	if err != nil {
		return "", "", err
	}
	return fromXIR, toXIR, nil
}

func xirWarnings(losses []xir.Loss) []protocolconvert.Warning {
	if len(losses) == 0 {
		return nil
	}
	warnings := make([]protocolconvert.Warning, 0, len(losses))
	for _, loss := range losses {
		warnings = append(warnings, protocolconvert.Warning{
			Field:  loss.Field,
			Reason: loss.Reason,
			Fatal:  loss.Fatal || loss.Kind == xir.LossRejected,
		})
	}
	return warnings
}
