package llm

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// derefString returns the pointed-to string, or an empty string for a nil pointer. SSE deltas carry
// optional string fields, and the same field is spelled differently by different OpenAI-compatible
// providers (`reasoning_content` vs `reasoning`), so callers fold the alternatives with firstNonEmpty.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
