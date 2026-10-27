// Package resources extracts the local filesystem paths and URLs a model asked
// a tool to operate on, so the monitor can answer "which files did this agent
// read, and which addresses did it call".
//
// The scope is deliberately narrow: only the model's own tool calls are read
// (see toolcalls.go). Tool results, message text, reasoning and code bodies are
// not scanned, because a path-shaped string in those is usually not an access -
// a regular expression literal, an import specifier, a documentation link or a
// URL quoted in prose all look like one. Within a call, the argument key (a
// `file_path` is a location, a `pattern` is a regex) and the shell operand
// position decide which tokens can be accesses; the shape detector in
// pkg/secretmask validates the survivors.
//
// The extraction is a pure function of the observation: it never rewrites the
// request or the response, stores no file contents, and needs no vault. The
// result is deduplicated by value within one trace; the caller (the parse
// worker) persists that as one row per distinct resource, not one per
// occurrence. A path reused across sessions is deduplicated globally by the
// resources table.
package resources

import (
	"encoding/json"
	"strings"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

const (
	// KindURL is an http(s)/ws(s) URL.
	KindURL = "url"
	// KindLocalPath is a local filesystem path in any of the forms
	// secretmask.RuleLocalPath recognizes.
	KindLocalPath = "local_path"

	// modeStrictPath is the internal mode of a candidate taken from a field
	// whose key does not name a location. An unclassified argument may be a
	// path, but only a strong shape is evidence: an absolute or home path, a
	// drive, a UNC root, or a relative path with a file extension. A bare
	// "../../lib/monitor" is an import specifier, not a file read.
	modeStrictPath = "strict_local_path"

	// maxValueBytes bounds a stored value. A path or URL longer than this is
	// not indexed rather than truncated: a truncated value would collide with
	// every other value sharing its prefix, and no real path or endpoint is
	// this long. It is a guard against a pathological match, not a display
	// limit.
	maxValueBytes = 8192
)

// Access is one distinct path or URL a model tool call named.
type Access struct {
	Kind  string
	Value string
	// Count is the number of tool calls that named the value. A call can carry
	// the same value in more than one argument, which is still one call.
	Count int
	// Position is the order of first appearance within the trace.
	Position int
}

// Occurrence is one place a distinct value was found: the tool call's node and
// the argument view inside it. Locate returns these so the Monitor can jump from
// a resource row to the conversation step the value came from; the offsets are
// byte offsets into the decoded argument text, not into the cassette.
type Occurrence struct {
	NodeID   string
	NodePath string
	View     string
	Start    int
	End      int
}

// The labels of the text views a node is scanned through by LocateValue. They
// are part of the locate response (not user-facing names): "text" is the
// parser's normalized text, "arguments" a tool call's arguments, and "raw"/"json"
// the payloads after one layer of decoding.
const (
	viewText      = "text"
	viewArguments = "arguments"
	viewRaw       = "raw"
	viewJSON      = "json"
)

// candidate is one argument token that may name an access, before the shape
// check. mode restricts the kind the field can mean: a URL argument cannot
// become a filesystem path.
type candidate struct {
	mode  string
	token string
	start int
	end   int
}

// Extract returns the distinct local paths and URLs the model's tool calls named
// in obs, in first-appearance order, with the number of calls that named each.
func Extract(obs observe.TraceObservation) []Access {
	byKey := make(map[string]*Access, 16)
	order := make([]string, 0, 16)
	position := 0
	for _, call := range modelToolCalls(obs) {
		// One call can name the same value in two arguments; that is one
		// occurrence of the value in this call, not two.
		callSeen := make(map[string]struct{}, 4)
		forEachCallCandidate(call, func(mode, token, view string, start, end int) {
			kind, value, ok := resolveCandidate(candidate{mode: mode, token: token, start: start, end: end})
			if !ok {
				return
			}
			key := kind + "\x00" + value
			if _, duplicated := callSeen[key]; duplicated {
				return
			}
			callSeen[key] = struct{}{}
			if access, exists := byKey[key]; exists {
				access.Count++
				return
			}
			byKey[key] = &Access{Kind: kind, Value: value, Count: 1, Position: position}
			position++
			order = append(order, key)
		})
	}
	out := make([]Access, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out
}

// maxLocateOccurrences bounds a literal search response. A value that occurs
// more often than this is ambient text (a format string, a common word), not a
// position an operator is looking for.
const maxLocateOccurrences = 200

// LocateValue returns every place obs carries value literally in one of the
// decoded node text views. It backs the audit-side locate for a masked value:
// the credential audit row knows the original bytes, and the conversation has to
// highlight all of them, not only the first.
//
// Unlike Locate it does not run the access extractor; the caller already knows
// the exact value and wants the positions, wherever the payload carries them.
// Empty values and values absent from the parse yield nothing.
func LocateValue(obs observe.TraceObservation, value string) []Occurrence {
	if value == "" {
		return nil
	}
	var out []Occurrence
	for _, node := range observe.TraceNodes(obs) {
		for _, view := range nodeTextViews(node) {
			start := 0
			for start <= len(view.text) {
				index := strings.Index(view.text[start:], value)
				if index < 0 {
					break
				}
				index += start
				out = append(out, Occurrence{
					NodeID:   node.ID,
					NodePath: node.Path,
					View:     view.label,
					Start:    index,
					End:      index + len(value),
				})
				if len(out) >= maxLocateOccurrences {
					return out
				}
				start = index + len(value)
			}
		}
	}
	return out
}

// Locate returns the places obs's model tool calls name kind/value. One
// occurrence per call node is enough to jump to the conversation, and calls are
// visited in the same order Extract walks them, so the first result is the
// earliest place the value appeared. A value the current extractor would not
// produce (an old index row, or a value the caller invented) has no occurrence.
func Locate(obs observe.TraceObservation, kind, value string) []Occurrence {
	if value == "" || (kind != KindURL && kind != KindLocalPath) {
		return nil
	}
	var out []Occurrence
	seenNodes := make(map[string]struct{}, 8)
	for _, call := range modelToolCalls(obs) {
		if call.nodeID != "" {
			if _, seen := seenNodes[call.nodeID]; seen {
				continue
			}
		}
		found := false
		forEachCallCandidate(call, func(mode, token, view string, start, end int) {
			if found {
				return
			}
			foundKind, foundValue, ok := resolveCandidate(candidate{mode: mode, token: token, start: start, end: end})
			if !ok || foundKind != kind || foundValue != value {
				return
			}
			found = true
			spanStart, spanEnd := candidateSpan(candidate{token: token, start: start, end: end})
			out = append(out, Occurrence{
				NodeID:   call.nodeID,
				NodePath: call.nodePath,
				View:     view,
				Start:    spanStart,
				End:      spanEnd,
			})
		})
		if found && call.nodeID != "" {
			seenNodes[call.nodeID] = struct{}{}
		}
	}
	return out
}

// resolveCandidate turns one argument token into a resource kind and value, or
// reports that it is not an access. The token must be exactly one path or URL:
// the shape detector has to match the whole token, which is what keeps
// "import ... from '../../lib/monitor'" and "print('/x')" from becoming file
// reads even when they reach an argument.
func resolveCandidate(c candidate) (kind, value string, ok bool) {
	token := strings.TrimSpace(c.token)
	if token == "" || len(token) > maxValueBytes {
		return "", "", false
	}
	src := []byte(token)
	matches := secretmask.Indicators(src)
	if len(matches) != 1 {
		return "", "", false
	}
	match := matches[0]
	if match.Start != 0 || match.End != len(token) {
		return "", "", false
	}
	rule, ok := match.Rule()
	if !ok || !rule.ReportOnly {
		return "", "", false
	}
	kind, ok = kindFor(rule.Kind)
	if !ok {
		return "", "", false
	}
	switch {
	case c.mode == modeStrictPath:
		// A URL is unambiguous; a local path from an unclassified field needs
		// a strong shape.
		if kind == KindLocalPath && !isStrongPathToken(token) {
			return "", "", false
		}
	case c.mode != "" && c.mode != kind:
		return "", "", false
	}
	if insideCredential(match, secretmask.Scan(src)) {
		return "", "", false
	}
	value = normalizeValue(rule.Kind, token)
	if value == "" || len(value) > maxValueBytes {
		return "", "", false
	}
	return kind, value, true
}

// isStrongPathToken reports whether a relative path candidate has evidence of
// naming a file rather than being a module specifier or a route fragment: an
// absolute, home, drive or UNC root, or a last segment with a file extension.
func isStrongPathToken(token string) bool {
	if token == "" {
		return false
	}
	switch {
	case strings.HasPrefix(token, "/"), strings.HasPrefix(token, "~/"),
		strings.HasPrefix(token, `\\`):
		return true
	case len(token) >= 3 && isASCIIAlpha(token[0]) && token[1] == ':' &&
		(token[2] == '/' || token[2] == '\\'):
		return true
	}
	slash := strings.LastIndexAny(token, `\/`)
	base := token[slash+1:]
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return false
	}
	for _, r := range base[dot+1:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			continue
		default:
			return false
		}
	}
	return true
}

func isASCIIAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// candidateSpan returns the byte range the candidate occupies inside the
// argument view, with the surrounding whitespace the resolver trimmed.
func candidateSpan(c candidate) (start, end int) {
	left := strings.TrimLeft(c.token, " \t\r\n")
	lead := len(c.token) - len(left)
	token := strings.TrimRight(left, " \t\r\n")
	return c.start + lead, c.start + lead + len(token)
}

// textView is one decoded string of one node, labelled with the view it came
// from so a locate result can say where the value was seen.
type textView struct {
	label string
	text  string
}

// nodeTextViews returns the views of node in the order LocateValue scans them.
//
// Scan decoded text only. The raw JSON envelopes are escaped text, and treating
// them as paths makes "\n" in a PEM block's base64 look like a UNC root.
// Decoding a string that is itself JSON (a tool call's arguments, or an entry
// parser's whole-request node) recovers the strings inside it at the same depth
// as the text.
func nodeTextViews(node observe.SemanticNode) []textView {
	var views []textView
	for _, text := range decodedTextViews(node.Text) {
		views = append(views, textView{label: viewText, text: text})
	}
	for _, text := range decodedTextViews(observe.NodeArguments(node)) {
		views = append(views, textView{label: viewArguments, text: text})
	}
	for _, text := range decodedJSONTexts(node.Raw) {
		views = append(views, textView{label: viewRaw, text: text})
	}
	for _, text := range decodedJSONTexts(node.JSON) {
		views = append(views, textView{label: viewJSON, text: text})
	}
	return views
}

// insideCredential reports whether an indicator span is wholly contained in a
// credential span. A URL that merely contains a key is not contained, because it
// starts before the key.
func insideCredential(match secretmask.Finding, credentials []secretmask.Finding) bool {
	for _, credential := range credentials {
		if credential.Start <= match.Start && match.End <= credential.End {
			return true
		}
	}
	return false
}

func kindFor(kind secretmask.Kind) (string, bool) {
	switch kind {
	case secretmask.KindURL:
		return KindURL, true
	case secretmask.KindLocalPath:
		return KindLocalPath, true
	default:
		return "", false
	}
}

// normalizeValue turns the raw matched bytes into the value stored for an
// operator. A Windows path inside a tool call's arguments is JSON text, so
// every real backslash is doubled (C:\\Users\\alice); the index records the
// path with one separator instead. A decoded UNC path also starts with a
// doubled backslash, but its network root is the only doubled run there, which
// is how the two forms are told apart. Invalid UTF-8 is coerced because both
// databases store the value as text.
func normalizeValue(kind secretmask.Kind, value string) string {
	if kind == secretmask.KindLocalPath && strings.Contains(value, `\\`) {
		// Keep the doubled prefix of a decoded UNC path; every other doubled
		// run is JSON escaping.
		if !strings.HasPrefix(value, `\\`) || strings.Contains(value[2:], `\\`) {
			value = strings.ReplaceAll(value, `\\`, `\`)
		}
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

// decodedTextViews decodes one text that is JSON as a whole (a tool call's
// arguments object, or a quoted message body) and falls back to the text
// itself when it is not JSON. The decoded strings are the values that actually
// reach a model, without the envelope's escapes.
func decodedTextViews(text string) []string {
	if text == "" {
		return nil
	}
	if decoded := decodedJSONTexts([]byte(text)); len(decoded) > 0 {
		return decoded
	}
	return []string{text}
}

// decodedJSONTexts returns every string value in one raw JSON payload.
func decodedJSONTexts(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var out []string
	collectJSONStrings(value, &out)
	return out
}

func collectJSONStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case []any:
		for _, item := range typed {
			collectJSONStrings(item, out)
		}
	case map[string]any:
		for _, item := range typed {
			collectJSONStrings(item, out)
		}
	}
}
