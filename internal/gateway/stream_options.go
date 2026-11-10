package gateway

import (
	"net/http"
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/xir"
)

// InjectStreamOptions adds stream_options.include_usage to an eligible Chat
// Completions request. It is a byte patch: every other byte of the client body
// is preserved. Non-POST, non-JSON and non-chat requests are returned as-is.
func InjectStreamOptions(req *http.Request, body []byte) []byte {
	if req == nil || req.Method != http.MethodPost || !strings.Contains(req.Header.Get("Content-Type"), "application/json") {
		return body
	}
	if llm.NormalizeEndpoint(req.URL.Path) != "/v1/chat/completions" {
		return body
	}
	rewritten, changed, err := xir.EnsureStreamIncludeUsage(body)
	if err != nil || !changed {
		return body
	}
	return rewritten
}
