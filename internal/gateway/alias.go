package gateway

import (
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// RewriteModelAlias replaces only the model string span in a request body.
// It is the single alias-rewrite primitive used by the proxy and the local
// Responses runtime; unchanged/empty values return the original slice.
func RewriteModelAlias(body []byte, requested, upstream string) ([]byte, bool) {
	requested = strings.TrimSpace(requested)
	upstream = strings.TrimSpace(upstream)
	if len(body) == 0 || requested == "" || upstream == "" || strings.EqualFold(requested, upstream) {
		return body, false
	}
	rewritten, ok, err := xir.RewriteModel(body, upstream)
	if err != nil || !ok {
		return body, false
	}
	return rewritten, true
}

// RewriteModelPath replaces the model segment of a protocol whose model is
// carried in the URL path (Gemini generateContent and Vertex resource paths).
// The model follows the last "/models/" marker and ends at ":" (action),
// "/" (further path), "?" or "#". Only an exact case-insensitive match of the
// requested name is rewritten, so a converter-generated or already-rewritten
// path is left alone. The query string is preserved.
func RewriteModelPath(path, requested, upstream string) (string, bool) {
	requested = strings.TrimSpace(requested)
	upstream = strings.TrimSpace(upstream)
	if path == "" || requested == "" || upstream == "" || strings.EqualFold(requested, upstream) {
		return path, false
	}
	const marker = "/models/"
	idx := strings.LastIndex(path, marker)
	if idx < 0 {
		return path, false
	}
	start := idx + len(marker)
	end := start
	for end < len(path) {
		switch path[end] {
		case ':', '/', '?', '#':
			goto found
		}
		end++
	}
found:
	if end == start {
		return path, false
	}
	if !strings.EqualFold(path[start:end], requested) {
		return path, false
	}
	return path[:start] + upstream + path[end:], true
}
