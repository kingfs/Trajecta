// Package harness classifies the client-side agent harness that produced an
// LLM API request from the request itself.
//
// The proxy records traffic it did not author: an SDK call, a CLI agent, an IDE
// extension or a gateway all arrive as the same POST. Knowing which one sent a
// request is what lets a trace list separate "Claude Code on a Tuesday" from
// "the test suite", and it is the one fact a cassette cannot be asked for later
// without re-reading the request bytes. Detection therefore runs once on the
// write path and its result is stored as trace metadata.
//
// Detection is deliberately passive: it reads the request headers, URL path and
// body and never rewrites any of them. The evidence it returns contains marker
// identifiers only ("user-agent:claude-cli", "prompt:you-are-claude-code"), so
// no prompt text, header value or credential can leak into the trace index. The
// highest-confidence signal wins; when several products share an SDK (GitHub
// Copilot and a plain OpenAI call both carry `openai-python` headers, for
// example) the product-specific signature is tried first and the SDK is only the
// fallback class.
package harness

import (
	"bytes"
	"net/http"
	"sort"
	"strings"
)

// Product classes a Detection.Kind can carry. The kind is a stable API-ish
// string: the Monitor groups by it and a filter should keep working when a new
// harness is added to the catalogue.
const (
	KindAgentCLI = "agent-cli"
	KindIDE      = "ide-extension"
	KindAgentApp = "agent-app"
	KindGateway  = "gateway"
	KindSDK      = "sdk"
)

// Confidence levels. A product-identifying header or a system-prompt
// fingerprint unique to one harness is high; a combination of tool names and a
// generic prompt phrase is medium; an SDK fallback with no product signature is
// low.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Detection is the classification of one request. The zero value means "no
// recognizable client", which is a normal answer for a hand-written curl or an
// internal synthetic exchange.
type Detection struct {
	// ID is the canonical, stable identifier (`claude-code`, `codex-cli`, ...).
	// It is what the Monitor filters on, so it must not change when a display
	// name changes.
	ID string
	// Name is the product label shown to a reader (`Claude Code`).
	Name string
	// Version is the client version the traffic carried, when it carried one.
	// It is empty rather than guessed when the request does not say.
	Version string
	// Kind is one of the Kind* constants.
	Kind string
	// Confidence is one of the Confidence* constants.
	Confidence string
	// Tags are stable labels derived from the request: `harness:<id>`,
	// `kind:<kind>`, `sdk:<sdk>`, `capability:<capability>`. They are sorted and
	// deduplicated.
	Tags []string
	// Evidence names the signals that matched, never their values.
	Evidence []string
}

// Empty reports whether no classification was made.
func (d Detection) Empty() bool {
	return d.ID == ""
}

// Detect classifies req, optionally using body (which may be nil for a request
// without one, such as a model listing). It does not mutate req.
//
// Identification is header-first and body-last. Phase one runs the catalogue
// over the headers and URL only (a view with no body windows) and returns the
// first high-confidence match without ever touching the body bytes, so the
// common case - a harness that sends a product User-Agent or a private header
// - costs a handful of header lookups. Only when no header signal names a
// product does phase two run the full catalogue with the body windows, which
// is where the prompt/tool fallbacks for header-less clients (Kilo Code,
// Goose) and the Qwen Code impersonation check live. The one short-circuit
// exception is Claude Code: Qwen Code spoofs its `claude-cli` User-Agent, so
// a phase-one claude-code claim is always re-checked against the Qwen prompt
// in phase two before it is accepted.
func Detect(req *http.Request, body []byte) Detection {
	if req == nil {
		return Detection{}
	}
	view := newRequestView(req, body)

	if detection, ok := firstHeaderHigh(newRequestView(req, nil)); ok && detection.ID != "claude-code" {
		detection.Tags = buildTags(detection, view)
		detection.Evidence = dedupeStrings(detection.Evidence)
		return detection
	}

	var best Detection
	for _, rule := range catalog {
		match, ok := rule.match(view)
		if !ok {
			continue
		}
		detection := Detection{
			ID:         rule.id,
			Name:       rule.name,
			Kind:       rule.kind,
			Version:    match.version,
			Confidence: match.confidence,
			Evidence:   match.evidence,
		}
		if best.Empty() || confidenceRank(detection.Confidence) > confidenceRank(best.Confidence) {
			best = detection
		}
		// A high-confidence product match cannot be beaten by a later rule; the
		// catalogue is ordered specific-to-generic anyway, so stop at the first
		// high-confidence hit.
		if best.Confidence == ConfidenceHigh {
			break
		}
	}

	if best.Empty() {
		// No product signature: the SDK and capability tags are still useful
		// labels for a plain SDK call ("streaming, tool-use, openai-python") and
		// are kept even without an ID. The caller decides whether a tag-only
		// detection is worth a `harness.detected` event; it is not, because no
		// harness was identified.
		best.Tags = sdkOnlyTags(view)
		best.Evidence = dedupeStrings(best.Evidence)
		return best
	}
	best.Tags = buildTags(best, view)
	best.Evidence = dedupeStrings(best.Evidence)
	return best
}

// firstHeaderHigh runs the catalogue over a header-only view and returns the
// first high-confidence match in catalogue order, which is the specificity
// order. A view without body windows makes every body marker miss, so only
// header- and path-based rules can match here; medium/low header matches are
// deliberately not returned, because a body signal may still claim the trace
// with higher confidence in phase two.
func firstHeaderHigh(view requestView) (Detection, bool) {
	for _, rule := range catalog {
		match, ok := rule.match(view)
		if !ok || match.confidence != ConfidenceHigh {
			continue
		}
		return Detection{
			ID:         rule.id,
			Name:       rule.name,
			Kind:       rule.kind,
			Version:    match.version,
			Confidence: match.confidence,
			Evidence:   match.evidence,
		}, true
	}
	return Detection{}, false
}

func confidenceRank(confidence string) int {
	switch confidence {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	default:
		return 0
	}
}

// buildTags renders the stable labels for a detection: the harness itself, its
// kind, any SDK the request clearly used, and the capability facts inferred
// from the request shape. Capability tags are useful even when only the SDK was
// recognized, because "plain SDK that streams and uses tools" is a different
// test case from "plain SDK that does neither".
func buildTags(detection Detection, view requestView) []string {
	tags := make([]string, 0, 6)
	tags = append(tags, "harness:"+detection.ID, "kind:"+detection.Kind)
	tags = append(tags, view.labelTags()...)
	sort.Strings(tags)
	return dedupeStrings(tags)
}

// sdkOnlyTags is what a request that matched no product signature still
// carries: the SDK family and the capability labels. No `harness:` or `kind:`
// tag is invented, because the classification deliberately stayed empty.
func sdkOnlyTags(view requestView) []string {
	tags := view.labelTags()
	sort.Strings(tags)
	return dedupeStrings(tags)
}

// labelTags are the labels derived from the request shape rather than from a
// product rule.
func (view requestView) labelTags() []string {
	capabilities := view.capabilities()
	tags := make([]string, 0, 3+len(capabilities))
	if sdk := view.sdkTag(); sdk != "" {
		tags = append(tags, "sdk:"+sdk)
	}
	for _, capability := range capabilities {
		tags = append(tags, "capability:"+capability)
	}
	return tags
}

// matchResult is what a catalogue rule reports about a matching request.
type matchResult struct {
	confidence string
	version    string
	evidence   []string
}

// rule is one catalogue entry. The match function only reads the request view;
// it does not allocate on the no-match path beyond small string work.
type rule struct {
	id    string
	name  string
	kind  string
	match func(requestView) (matchResult, bool)
}

func detected(confidence string, version string, evidence ...string) (matchResult, bool) {
	return matchResult{
		confidence: confidence,
		version:    version,
		evidence:   evidence,
	}, true
}

// requestView is the read-only projection of a request the rules work on. Body
// markers are searched through bounded windows rather than the whole payload:
// the system prompt and tool declarations of every agent harness observed so
// far sit at the start of the body (chat system message, anthropic system
// field, responses instructions, gemini systemInstruction), while OpenAI-style
// tool arrays sit at the end. Two windows therefore cover the interesting text
// without making detection cost grow with a multi-megabyte conversation.
type requestView struct {
	method string
	path   string
	query  string
	header http.Header
	// bodyWindows are the byte slices the marker search may read. They are
	// sub-slices of body, so no copy is made.
	bodyWindows [][]byte
	// body is the full body, used only by the cheap capability checks that need
	// JSON field semantics.
	body []byte
}

const (
	// bodyPrefixScan is how much of a request body is searched for prompt and
	// tool markers.
	bodyPrefixScan = 512 << 10
	// bodySuffixScan covers the tail of a request, where OpenAI-style tool
	// arrays and response-API item lists usually sit.
	bodySuffixScan = 256 << 10
)

func newRequestView(req *http.Request, body []byte) requestView {
	view := requestView{
		method: strings.ToUpper(req.Method),
		path:   req.URL.Path,
		query:  req.URL.RawQuery,
		header: req.Header,
		body:   body,
	}
	if len(body) == 0 {
		return view
	}
	if len(body) <= bodyPrefixScan+bodySuffixScan {
		view.bodyWindows = [][]byte{body}
		return view
	}
	view.bodyWindows = [][]byte{
		body[:bodyPrefixScan],
		body[len(body)-bodySuffixScan:],
	}
	return view
}

// header returns the first value of a header. Go's Header.Get is canonical-key
// based and already case-insensitive.
func (view requestView) headerValue(name string) string {
	if view.header == nil {
		return ""
	}
	return strings.TrimSpace(view.header.Get(name))
}

// headerFirst returns the first non-empty value among names.
func (view requestView) headerFirst(names ...string) string {
	for _, name := range names {
		if value := view.headerValue(name); value != "" {
			return value
		}
	}
	return ""
}

// headerNameWithPrefix returns the lexicographically first header name that
// starts with prefix, lower-cased, or "" when none does. It is deterministic so
// a replayed cassette produces the same evidence list. Go canonicalizes header
// names, so the value is lower-cased before the comparison.
func (view requestView) headerNameWithPrefix(prefix string) string {
	prefix = strings.ToLower(prefix)
	found := ""
	for name := range view.header {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if found == "" || lower < found {
			found = lower
		}
	}
	return found
}

// headerContains reports whether any value of name contains sub, case
// insensitively. Header values are lower-cased once per check rather than per
// rule; the number of headers involved is tiny.
func (view requestView) headerContains(name string, sub string) bool {
	if view.header == nil {
		return false
	}
	sub = strings.ToLower(sub)
	for _, value := range view.header.Values(name) {
		if strings.Contains(strings.ToLower(value), sub) {
			return true
		}
	}
	return false
}

// bodyContains reports whether any body window contains marker. Markers are
// exact bytes that JSON leaves unescaped: plain ASCII phrases and tool names.
// The search works on the byte windows directly, so a marker check never copies
// the payload and detection stays allocation-flat on the write path.
func (view requestView) bodyContains(marker string) bool {
	if marker == "" {
		return false
	}
	needle := []byte(marker)
	for _, window := range view.bodyWindows {
		if bytes.Contains(window, needle) {
			return true
		}
	}
	return false
}

// bodyHeadContainsAny reports whether any marker occurs in the head body
// window only. System prompts and instruction fields sit at the start of the
// body on every protocol, while the tail window holds tool arrays and the
// most recent conversation - exactly where a user quoting `"You are Codex"`
// would live. Searching prompt phrases in the head only keeps a pasted prompt
// from claiming the trace, which a whole-body scan cannot avoid.
func (view requestView) bodyHeadContainsAny(markers ...string) bool {
	if len(view.bodyWindows) == 0 {
		return false
	}
	head := view.bodyWindows[0]
	for _, marker := range markers {
		if marker != "" && bytes.Contains(head, []byte(marker)) {
			return true
		}
	}
	return false
}

// bodyContainsAny reports whether any marker occurs in the body windows. It
// exists so a rule can name one strong marker set in one call instead of
// repeating the loop.
func (view requestView) bodyContainsAny(markers ...string) bool {
	for _, marker := range markers {
		if view.bodyContains(marker) {
			return true
		}
	}
	return false
}

// bodyContainsAll reports whether every marker occurs in the body windows.
func (view requestView) bodyContainsAll(markers ...string) bool {
	for _, marker := range markers {
		if !view.bodyContains(marker) {
			return false
		}
	}
	return true
}

// hasJSONShape reports whether the query string or path carries a marker (the
// Gemini protocol puts the streaming verb in the query).
func (view requestView) hasJSONShape(shape string) bool {
	return strings.Contains(view.query, shape) || strings.Contains(view.path, shape)
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
