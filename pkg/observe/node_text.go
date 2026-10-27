package observe

import "encoding/json"

// TraceNodes returns every semantic node of the exchange in payload order:
// the request tree, the response tree and the accumulated stream tool calls,
// each walked depth-first.
//
// It is the one traversal detectors and derived indexes should use; a caller
// that walks only Request.Nodes misses the model output, and one that walks only
// Response.Nodes misses the history the client sent.
func TraceNodes(obs TraceObservation) []SemanticNode {
	var roots []SemanticNode
	roots = append(roots, obs.Request.Nodes...)
	roots = append(roots, obs.Response.Nodes...)
	roots = append(roots, obs.Stream.AccumulatedToolCalls...)
	var out []SemanticNode
	var walk func([]SemanticNode)
	walk = func(nodes []SemanticNode) {
		for _, node := range nodes {
			out = append(out, node)
			walk(node.Children)
		}
	}
	walk(roots)
	return out
}

// NodeTexts returns the text representations of one node: the normalized text,
// a tool call's arguments, and the two raw payloads. The views overlap on
// purpose - the raw JSON carries the same bytes the normalized text does - so a
// caller that counts occurrences must deduplicate per node rather than assume
// every returned view is a distinct occurrence.
//
// The raw payloads are JSON text, with escape sequences still in place. A
// caller that scans for paths or URLs should use NodeArguments and decode the
// raw payloads instead: "\\n" in a raw envelope is an escaped newline, not a
// UNC path.
func NodeTexts(node SemanticNode) []string {
	return []string{
		node.Text,
		NodeArguments(node),
		string(node.Raw),
		string(node.JSON),
	}
}

// NodeArguments returns the arguments text of a tool call, or an empty string.
// The value is the provider's JSON string, so a path inside it may still carry
// one level of escapes; the caller decides whether to decode it.
func NodeArguments(node SemanticNode) string {
	return metadataText(node.Metadata, "arguments")
}

// metadataText renders one metadata value as the text a detector should scan.
// A tool call's arguments are stored as the JSON string the provider sent, so a
// decoded string is returned as-is and any other shape is serialized.
func metadataText(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.RawMessage:
		return string(typed)
	case []byte:
		return string(typed)
	default:
		buf, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(buf)
	}
}
