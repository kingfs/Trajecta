package detector

import (
	"context"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/xir"
	xiraudit "github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// RequestScope is the IR-derived scannable span scope of a request body: the raw
// byte ranges of its text-bearing projection nodes, grouped by XIR point. It is
// what lets the hot path run a detection plugin only on the fields the plugin
// declares, and exclude sealed (encrypted reasoning / media) content structurally
// rather than by a post-hoc byte filter.
type RequestScope struct {
	SpansByPoint map[hook.Point][]detect.Span
}

// SpansFor returns the union of the raw byte spans declared for points. The
// result is suitable for detect.Input.Spans; the engine rejects any match that
// falls outside it.
func (s RequestScope) SpansFor(points []hook.Point) []detect.Span {
	var out []detect.Span
	for _, p := range points {
		out = append(out, s.SpansByPoint[p]...)
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

// ParseRequestScope parses body as a request of protocol and projects it to
// per-point raw byte spans, reusing the same XIR codec + audit projection the
// audit lane uses. Sealed nodes contribute no span (NodePoint maps them to the
// empty point), so encrypted content is excluded by construction.
//
// ok=false means the body could not be scoped: an unknown protocol, a parse or
// decode failure, a text-bearing node without a usable wire span, or an empty
// projection. The caller MUST then fall back to the whole-body byte path so that
// scoping never costs a detection the byte path would have made.
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
	// NeedTools is false: detection scopes cover message/tool-call/tool-result
	// content, not tool declarations, so decoding tool schemas is wasted work.
	req, err := codec.DecodeRequest(context.Background(), doc, xir.DecodeOptions{NeedParts: true})
	if err != nil {
		return RequestScope{}, false
	}
	obs := xiraudit.Build(po.proto, po.op, req, nil)
	scope := RequestScope{SpansByPoint: make(map[hook.Point][]detect.Span)}
	for _, node := range obs.Nodes {
		if node.Text == "" {
			continue
		}
		point := xiraudit.NodePoint(node)
		if point == "" {
			continue
		}
		if node.SpanEnd <= node.SpanStart || node.SpanEnd > len(body) {
			// A text-bearing node without a usable wire span cannot be scoped
			// precisely; fall back so no detection is lost.
			return RequestScope{}, false
		}
		scope.SpansByPoint[point] = append(scope.SpansByPoint[point], detect.Span{
			Start: node.SpanStart,
			End:   node.SpanEnd,
			Path:  node.Path,
			Kind:  string(point),
		})
	}
	if len(scope.SpansByPoint) == 0 {
		return RequestScope{}, false
	}
	return scope, true
}
