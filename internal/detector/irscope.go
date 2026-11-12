package detector

import (
	"context"
	"sort"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/xir"
	xiraudit "github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
)

// RequestScope is the IR-derived scan scope of a request body for the hot path.
// It records the sealed (encrypted reasoning / media / protocol-native opaque)
// byte spans so the hot path can scan the whole body EXCEPT those spans. This is
// coverage-preserving: every byte the whole-body byte path scans is still scanned
// here, minus structurally-sealed content the byte path also excludes via the
// Vault's opaque guard. Field-level point scoping is deliberately NOT used to
// restrict the hot scan universe — a credential in a non-projected position (a
// JSON key, a `user`/`metadata` field, a JSON-only tool result) must still be
// masked, so narrowing to projected text fields would leak it. Point scoping is
// applied in the advisory audit lane instead, where it has no security impact.
type RequestScope struct {
	sealed  []detect.Span
	bodyLen int
}

// ScannableSpans returns the whole body minus the sealed spans, sorted and
// non-overlapping, suitable for detect.Input.Spans. With no sealed content it is
// the single span [0, bodyLen), i.e. exactly the byte path's scan universe.
func (s RequestScope) ScannableSpans() []detect.Span {
	return complementSpans(s.sealed, s.bodyLen)
}

// complementSpans returns the ranges of [0, n) not covered by excluded. The
// excluded spans are sorted and merged first; out-of-range or empty spans are
// ignored. The result is sorted, disjoint and covers the rest of [0, n).
func complementSpans(excluded []detect.Span, n int) []detect.Span {
	if n <= 0 {
		return nil
	}
	valid := make([]detect.Span, 0, len(excluded))
	for _, s := range excluded {
		if s.Start < 0 || s.End <= s.Start || s.End > n {
			continue
		}
		valid = append(valid, s)
	}
	if len(valid) == 0 {
		return []detect.Span{{Start: 0, End: n}}
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Start != valid[j].Start {
			return valid[i].Start < valid[j].Start
		}
		return valid[i].End < valid[j].End
	})
	var out []detect.Span
	cursor := 0
	for _, s := range valid {
		if s.Start > cursor {
			out = append(out, detect.Span{Start: cursor, End: s.Start})
		}
		if s.End > cursor {
			cursor = s.End
		}
	}
	if cursor < n {
		out = append(out, detect.Span{Start: cursor, End: n})
	}
	return out
}

// protocolOperations maps the proxy's wire-protocol name to the XIR protocol and
// request operation the codec registry expects.
var protocolOperations = map[string]struct {
	proto xir.Protocol
	op    xir.Operation
}{
	"chat_completions": {xir.ProtocolChatCompletions, xir.OperationChat},
	"responses":        {xir.ProtocolResponses, xir.OperationResponses},
	"messages":         {xir.ProtocolMessages, xir.OperationMessages},
	"gemini":           {xir.ProtocolGemini, xir.OperationGenerateContent},
}

// ParseRequestScope parses body as a request of protocol and collects its sealed
// byte spans, reusing the same XIR codec + audit projection the audit lane uses.
// The hot path scans the complement (see RequestScope.ScannableSpans), so sealed
// content is excluded structurally while everything else keeps full coverage.
//
// ok=false means the body could not be parsed (unknown protocol, parse or decode
// failure, empty body); the caller then falls back to the whole-body byte path.
// Sealed spans are best-effort: the Vault's opaque guard remains active on both
// paths, so even a sealed region this projection does not capture is still
// refused by the Vault — scoping never costs a detection the byte path makes.
func ParseRequestScope(protocol string, body []byte) (RequestScope, bool) {
	po, ok := protocolOperations[protocol]
	if !ok || len(body) == 0 {
		return RequestScope{}, false
	}
	doc, err := xir.ParseDocument(po.proto, po.op, body)
	if err != nil {
		return RequestScope{}, false
	}
	codec, ok := registry.Default().Lookup(po.proto)
	if !ok {
		return RequestScope{}, false
	}
	req, err := codec.DecodeRequest(context.Background(), doc, xir.DecodeOptions{NeedParts: true})
	if err != nil {
		return RequestScope{}, false
	}
	obs := xiraudit.Build(po.proto, po.op, req, nil)
	scope := RequestScope{bodyLen: len(body)}
	for _, node := range obs.Nodes {
		if node.Kind != string(xir.PartSealed) {
			continue
		}
		if node.SpanStart < 0 || node.SpanEnd <= node.SpanStart || node.SpanEnd > len(body) {
			// A sealed block without a usable wire span is left to the Vault's
			// opaque guard rather than guessed at.
			continue
		}
		scope.sealed = append(scope.sealed, detect.Span{Start: node.SpanStart, End: node.SpanEnd})
	}
	return scope, true
}
