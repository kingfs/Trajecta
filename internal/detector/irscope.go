package detector

import (
	"bytes"
	"context"
	"sort"
	"strings"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// defaultMaxScopeBytes bounds the body size the hot path will project through
// the XIR codec. The projection allocates a node per JSON value plus the decoded
// request, which is a few times the body size; without a bound a 64 MiB request
// (the largest accepted body) could allocate hundreds of MiB per attempt.
// Larger bodies keep the byte fast path, which is still fully covered.
const defaultMaxScopeBytes = 256 << 10

// ScopeOptions tunes the request projection for one hot-path decision.
type ScopeOptions struct {
	// MaxBytes bounds the body size that may be projected. Zero uses
	// defaultMaxScopeBytes; a negative value disables the bound.
	MaxBytes int
	// RequireFields asks for the per-point field projection even when the body
	// carries no sealed marker. The builtin body scanner does not need it; a
	// field-scoped plugin does.
	RequireFields bool
}

// RequestScope is the IR-derived scan scope of one request body.
//
// It serves two independent consumers:
//
//   - the sealed set: protocol content that must round-trip byte for byte
//     (signed reasoning, encrypted reasoning, redacted thinking). A content
//     detector must never rewrite it, and the Vault drops any candidate that
//     overlaps it. Only blocks a codec marks MustRoundTrip are sealed; an
//     unmodeled protocol-native part (Gemini executableCode) is ordinary
//     content and stays scannable.
//   - the field map: the raw byte spans of each XIR hook point. A plugin that
//     declares Points scans only its own fields instead of the whole body, so a
//     narrow algorithm (tool arguments, tool results) stays cheap and does not
//     see unrelated text.
//
// The hot path always scans the whole body once and lets the Vault drop the
// candidates that overlap a sealed span. It deliberately does not split the
// body into complement spans: slicing the buffer changes the detector's
// context window at every boundary, which makes the same input score
// differently from the byte path.
type RequestScope struct {
	bodyLen int
	sealed  []detect.Span
	fields  map[hook.Point][]detect.Span
}

// SealedSpans returns the must-round-trip wire spans, sorted and merged.
func (s RequestScope) SealedSpans() []detect.Span {
	return s.sealed
}

// Fields returns the wire spans of one point, sorted and merged. It returns nil
// for a point the projection does not represent.
func (s RequestScope) Fields(point hook.Point) []detect.Span {
	if len(s.fields) == 0 {
		return nil
	}
	return s.fields[point]
}

// FieldsFor returns the merged union of the given points' spans.
func (s RequestScope) FieldsFor(points []hook.Point) []detect.Span {
	var out []detect.Span
	for _, point := range points {
		out = append(out, s.fields[point]...)
	}
	return mergeSpans(out, 0)
}

// HasPoint reports whether the projection produced any span for point.
func (s RequestScope) HasPoint(point hook.Point) bool {
	for _, span := range s.fields[point] {
		if span.End > span.Start {
			return true
		}
	}
	return false
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

// sealedMarkers is the cheap prefilter for "this body can contain a block that
// must round-trip". It is per protocol and deliberately conservative: a marker
// may be a substring of ordinary text (an extra parse is harmless), but it must
// never miss a sealed construct. The chat-completions codec produces no sealed
// part at all, so its entry is nil and the protocol skips the projection
// entirely.
var sealedMarkers = map[xir.Protocol][][]byte{
	xir.ProtocolChatCompletions: nil,
	xir.ProtocolMessages:        {[]byte(`"redacted_thinking"`)},
	xir.ProtocolResponses:       {[]byte(`"encrypted_content"`)},
	xir.ProtocolGemini:          {[]byte(`"thought`), []byte(`"thought_signature"`)},
}

// mayHaveSealed reports whether body contains a marker of sealed content for
// proto. An entry with no markers (chat) always answers false.
func mayHaveSealed(proto xir.Protocol, body []byte) bool {
	markers := sealedMarkers[proto]
	if len(markers) == 0 {
		return false
	}
	for _, marker := range markers {
		if bytes.Contains(body, marker) {
			return true
		}
	}
	return false
}

// ParseRequestScope parses body as a request of protocol and projects it into a
// RequestScope. ok=false means the body was not projected (unknown protocol,
// empty body, over the size bound, no sealed marker when fields were not
// required, or a parse/decode failure); the caller keeps the byte fast path.
func ParseRequestScope(protocol string, body []byte, opts ScopeOptions) (RequestScope, bool) {
	po, ok := protocolOperations[protocol]
	if !ok || len(body) == 0 {
		return RequestScope{}, false
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxScopeBytes
	}
	if maxBytes > 0 && len(body) > maxBytes {
		return RequestScope{}, false
	}
	if !opts.RequireFields && !mayHaveSealed(po.proto, body) {
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
	req, err := codec.DecodeRequest(context.Background(), doc, xir.DecodeOptions{NeedParts: true, NeedTools: true})
	if err != nil {
		return RequestScope{}, false
	}
	scope := RequestScope{bodyLen: len(body)}
	if opts.RequireFields {
		scope.fields = make(map[hook.Point][]detect.Span, 8)
	}
	collectRequestScope(req, &scope)
	scope.sealed = mergeSpans(scope.sealed, 0)
	for point, spans := range scope.fields {
		scope.fields[point] = mergeSpans(spans, 0)
	}
	return scope, true
}

// collectRequestScope walks the decoded request once and records the sealed
// spans plus the per-point field spans. It is the hot-path replacement for
// building the whole audit projection (which allocates a node, a stable sha1
// ID and cloned raws for every part) just to read the same spans.
func collectRequestScope(req *xir.Request, scope *RequestScope) {
	if req == nil {
		return
	}
	var walk func(role xir.Role, parts []xir.Part)
	walk = func(role xir.Role, parts []xir.Part) {
		for _, part := range parts {
			if part.Ref == nil {
				continue
			}
			span := detect.Span{Start: part.Ref.Span.Start, End: part.Ref.Span.End}
			// Any part whose sealed block is signature-bound must round-trip as a
			// whole: Gemini attaches a thoughtSignature to functionCall/media
			// parts too, and rewriting one byte of such a part (its arguments or
			// inline bytes included) invalidates the signature.
			if sealedRoundTrip(part.Sealed) {
				scope.sealed = append(scope.sealed, span)
				continue
			}
			switch part.Kind {
			case xir.PartSealed:
				if sealedRoundTrip(part.Sealed) {
					scope.sealed = append(scope.sealed, span)
					continue
				}
				// An unmodeled native part that is not signature-bound is
				// ordinary content: keep it scannable.
				scope.addField(requestPartPoint(role, part), span)
			case xir.PartReasoning:
				r := part.Reasoning
				if r == nil {
					break
				}
				if (r.Sealed != nil && r.Sealed.MustRoundTrip) || r.EncryptedContent != "" {
					scope.sealed = append(scope.sealed, span)
					continue
				}
				if reasoningVisibleText(r) != "" {
					scope.addField(requestPartPoint(role, part), span)
				}
			case xir.PartToolCall, xir.PartToolResult:
				scope.addField(requestPartPoint(role, part), span)
				if part.Kind == xir.PartToolResult && part.ToolResult != nil {
					walk(xir.RoleTool, part.ToolResult.Content)
				}
			case xir.PartText, xir.PartRefusal, xir.PartUnknown, xir.PartCitation, xir.PartSearch:
				scope.addField(requestPartPoint(role, part), span)
			default:
				// Media parts carry no text; the Vault's opaque guard owns
				// their inline bytes.
			}
		}
	}
	walk(xir.RoleSystem, req.System)
	for _, message := range req.Messages {
		walk(message.Role, message.Parts)
	}
	if len(req.Tools) > 0 {
		for _, tool := range req.Tools {
			if tool.Ref == nil {
				continue
			}
			scope.addField(hook.PointRequestToolDecl,
				detect.Span{Start: tool.Ref.Span.Start, End: tool.Ref.Span.End})
		}
	}
}

// addField records span under point when it is usable.
func (s *RequestScope) addField(point hook.Point, span detect.Span) {
	if s.fields == nil || point == "" || span.Start < 0 || span.End <= span.Start {
		return
	}
	s.fields[point] = append(s.fields[point], span)
}

// sealedRoundTrip reports whether a sealed value must be preserved byte for
// byte. A nil value or one without the flag is unmodeled content, not a
// signature-bound block.
func sealedRoundTrip(sealed *xir.Sealed) bool {
	return sealed != nil && sealed.MustRoundTrip
}

// reasoningVisibleText is the visible text of a reasoning part.
func reasoningVisibleText(r *xir.Reasoning) string {
	if r == nil {
		return ""
	}
	if r.Text != "" {
		return r.Text
	}
	if len(r.Summary) == 0 {
		return ""
	}
	return strings.Join(r.Summary, "\n")
}

// requestPartPoint maps one request part to the hook point it belongs to. It
// mirrors pkg/xir/audit.NodePoint for the request side, with one intentional
// difference: an unmodeled native part is content on the message point, so a
// plugin that declares that point still sees it.
func requestPartPoint(role xir.Role, part xir.Part) hook.Point {
	switch part.Kind {
	case xir.PartToolCall:
		return hook.PointRequestToolCall
	case xir.PartToolResult:
		return hook.PointRequestToolResult
	}
	if role == xir.RoleTool {
		// Content nested inside a tool result belongs to that result, not to a
		// message; mapping it to request.message would feed tool output to a
		// plugin that declared the message point.
		return hook.PointRequestToolResult
	}
	if role == xir.RoleSystem || role == xir.RoleDeveloper {
		return hook.PointRequestSystem
	}
	return hook.PointRequestMessage
}

// mergeSpans sorts spans, drops invalid/empty ones above n (n<=0 means no
// bound) and merges overlaps so a consumer can scan each byte at most once.
func mergeSpans(spans []detect.Span, n int) []detect.Span {
	if len(spans) == 0 {
		return nil
	}
	valid := make([]detect.Span, 0, len(spans))
	for _, span := range spans {
		if span.Start < 0 || span.End <= span.Start {
			continue
		}
		if n > 0 && span.End > n {
			continue
		}
		valid = append(valid, span)
	}
	if len(valid) == 0 {
		return nil
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Start != valid[j].Start {
			return valid[i].Start < valid[j].Start
		}
		return valid[i].End < valid[j].End
	})
	out := valid[:1]
	for _, span := range valid[1:] {
		last := &out[len(out)-1]
		if span.Start <= last.End {
			if span.End > last.End {
				last.End = span.End
			}
			continue
		}
		out = append(out, span)
	}
	return out
}
