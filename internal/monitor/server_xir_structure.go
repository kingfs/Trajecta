// The XIR structure endpoint: the static logical shape of the Exchange IR
// (pkg/xir/schema) behind the Monitor's 质量 → XIR 结构 tab. It serves metadata
// only — no traffic, no store reads — so any signed-in role can render the
// map. The detection-plugin overlay (which nodes an enabled plugin observes)
// comes from the existing admin-only /api/detector-plugins catalog; a non-admin
// reader still gets the structure and the hook-point vocabulary.

package monitor

import (
	"net/http"

	"github.com/kingfs/Trajecta/pkg/xir/schema"
)

// xirStructureAPIHandler serves GET /api/xir/structure.
func xirStructureAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, schema.Build())
	}
}
