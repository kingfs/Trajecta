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
