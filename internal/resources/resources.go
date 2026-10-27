// Package resources extracts the local filesystem paths and URLs a recorded
// exchange carried, so the monitor can answer "which directories did this
// agent read and which addresses did it call".
//
// The extraction is a pure function of the observation: it never rewrites the
// request or the response, stores no file contents, and needs no vault. The
// detector is the report-only Ragel machine in pkg/secretmask, so the shapes
// are the same ones the masking engine knows without sharing its rewrite path.
//
// The result is deduplicated by value within one trace; the caller (the parse
// worker) persists that as one row per distinct resource, not one per
// occurrence. A path reused across sessions is deduplicated globally by the
// resources table, which is what keeps the database bounded by the number of
// distinct values rather than by traffic.
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

	// maxValueBytes bounds a stored value. A path or URL longer than this is
	// not indexed rather than truncated: a truncated value would collide with
	// every other value sharing its prefix, and no real path or endpoint is
	// this long. It is a guard against a pathological match, not a display
	// limit.
	maxValueBytes = 8192
)

// Access is one distinct path or URL seen in a trace.
type Access struct {
	Kind  string
	Value string
	// Count is the number of semantic nodes that carried the value. A node is
	// scanned through overlapping text views (text, arguments, raw JSON), so
	// counting views would inflate it for every node.
	Count int
	// Position is the order of first appearance within the trace.
	Position int
}

// Extract returns the distinct local paths and URLs in obs, in first-appearance
// order, with the number of nodes that carried each one.
func Extract(obs observe.TraceObservation) []Access {
	byKey := make(map[string]*Access, 16)
	order := make([]string, 0, 16)
	position := 0
	for _, node := range observe.TraceNodes(obs) {
		// A node's four text views overlap; a value in more than one of them is
		// still one occurrence in this node.
		nodeSeen := make(map[string]struct{}, 4)
		// Scan decoded text only. The raw JSON envelopes are escaped text, and
		// treating them as paths makes "\n" in a PEM block's base64 look like a
		// UNC root. Decoding a string that is itself JSON (a tool call's
		// arguments, or an entry parser's whole-request node) recovers the paths
		// inside it at the same depth as the text.
		views := decodedTextViews(node.Text)
		views = append(views, decodedTextViews(observe.NodeArguments(node))...)
		views = append(views, decodedJSONTexts(node.Raw)...)
		views = append(views, decodedJSONTexts(node.JSON)...)
		for _, text := range views {
			if text == "" {
				continue
			}
			src := []byte(text)
			// A credential's material can contain slashes that look exactly like
			// an absolute path; a private-key body is base64, and any base64 run
			// with two slash-separated segments would otherwise be indexed. Drop
			// a candidate only when it is entirely inside a credential span: a
			// URL that carries a key in its query still starts before it and is
			// kept.
			credentials := secretmask.Scan(src)
			for _, match := range secretmask.Indicators(src) {
				if insideCredential(match, credentials) {
					continue
				}
				rule, ok := match.Rule()
				if !ok || !rule.ReportOnly {
					continue
				}
				kind, ok := kindFor(rule.Kind)
				if !ok {
					continue
				}
				value := normalizeValue(rule.Kind, string(match.Bytes(src)))
				if value == "" || len(value) > maxValueBytes {
					continue
				}
				key := kind + "\x00" + value
				if _, ok := nodeSeen[key]; ok {
					continue
				}
				nodeSeen[key] = struct{}{}
				if access, ok := byKey[key]; ok {
					access.Count++
					continue
				}
				byKey[key] = &Access{Kind: kind, Value: value, Count: 1, Position: position}
				position++
				order = append(order, key)
			}
		}
	}
	out := make([]Access, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out
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

// decodedJSONTexts returns every string value in one raw JSON payload. It is
// used to see through the extra escaping an entry parser's whole-request node
// carries; a malformed payload contributes nothing.
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
