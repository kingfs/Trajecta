package gateway

import (
	"net/http"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

// ApplyConvertedHeaders fixes protocol-scoped headers after a request body was
// converted. Authentication is handled separately by the target's auth rules;
// version/beta/key headers belong to the client protocol's surface and must not
// leak, nor be missing, on the new surface.
func ApplyConvertedHeaders(outreq, original *http.Request, upstreamProtocol protocolconvert.Protocol) {
	if outreq == nil {
		return
	}
	hdr := outreq.Header
	switch upstreamProtocol {
	case protocolconvert.ProtocolMessages:
		if hdr.Get("anthropic-version") == "" {
			hdr.Set("anthropic-version", "2023-06-01")
		}
		if original != nil && original.Header.Get("anthropic-beta") != "" {
			hdr.Set("anthropic-beta", original.Header.Get("anthropic-beta"))
		}
	default:
		hdr.Del("anthropic-version")
		hdr.Del("anthropic-beta")
	}
	if upstreamProtocol == protocolconvert.ProtocolGemini && original != nil && original.URL != nil {
		if key := original.URL.Query().Get("key"); key != "" && hdr.Get("x-goog-api-key") == "" {
			hdr.Set("x-goog-api-key", key)
		}
	}
	hdr.Set("Content-Type", "application/json")
}
