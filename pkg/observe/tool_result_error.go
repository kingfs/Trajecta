package observe

import (
	"strconv"
	"strings"
)

// ToolResultFailed reports whether the exchange itself marks a tool result as
// failed. Only protocol-level state is consulted: an explicit `is_error` flag, a
// failed status, an explicit error part type, or a failed Gemini code execution
// outcome. The result text is never inspected.
//
// The distinction is the point of this predicate. Tool output is arbitrary
// bytes: a successful `git diff`, `cat` or test run can contain the token
// "error" (source code, a log line, a JSON schema, a stack trace), and a text
// search cannot tell that apart from a failed execution. A tool is failed only
// when the protocol says it is.
func ToolResultFailed(node SemanticNode) bool {
	switch node.NormalizedType {
	case NodeToolResult, NodeServerToolResult, NodeCodeResult:
	default:
		return false
	}
	if metadataBool(node.Metadata, "is_error") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(metadataString(node.Metadata, "status"))) {
	case "failed", "error", "failure":
		return true
	}
	if providerTypeMarksError(node.ProviderType) {
		return true
	}
	return node.NormalizedType == NodeCodeResult && geminiCodeExecutionFailed(node)
}

// metadataBool reads a boolean metadata value that the parsers may keep as a
// bool or as the string form a JSON field was decoded into.
func metadataBool(metadata map[string]any, key string) bool {
	value, ok := metadata[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}

// providerTypeMarksError recognizes an explicit error block type. The current
// Anthropic parser normalizes these to NodeError before this predicate is
// called, so this is a guard for parsers that keep the provider type on a tool
// result node.
func providerTypeMarksError(providerType string) bool {
	lower := strings.ToLower(strings.TrimSpace(providerType))
	return lower == "error" || strings.HasSuffix(lower, "_error") || strings.HasSuffix(lower, ".error")
}

// geminiCodeExecutionFailed reads the structured outcome of a Gemini code
// execution result. OUTCOME_FAILED and OUTCOME_DEADLINE_EXCEEDED are the
// protocol's own failure states; OUTCOME_OK and an absent outcome are not.
func geminiCodeExecutionFailed(node SemanticNode) bool {
	raw := rawMessageFromMetadata(node.Metadata, "codeExecutionResult")
	if len(raw) == 0 {
		return false
	}
	obj, err := decodeJSONObject(raw)
	if err != nil {
		return false
	}
	switch strings.ToUpper(stringField(obj, "outcome")) {
	case "OUTCOME_FAILED", "OUTCOME_DEADLINE_EXCEEDED":
		return true
	default:
		return false
	}
}
