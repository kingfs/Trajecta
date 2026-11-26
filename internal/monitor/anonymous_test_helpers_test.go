package monitor

import (
	"net/http"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/store"
)

// registerRoutesForTest keeps the production fail-closed default intact while
// letting isolated tests run without an auth store. A test that needs a real
// verifier passes one explicitly and is left untouched.
func registerRoutesForTest(mux *http.ServeMux, st *store.Store, opts ...RouteOptions) {
	var options RouteOptions
	if len(opts) > 0 {
		options = opts[0]
	}
	if options.MonitorAuthVerifier == nil {
		options.MonitorAuthVerifier = auth.AllowAnonymousForTest()
	}
	RegisterRoutes(mux, st, options)
}
