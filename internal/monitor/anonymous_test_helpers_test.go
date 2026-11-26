package monitor

import (
	"net/http"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/store"
)

// registerAnonymousRoutesForTest makes the few route-level tests that are not
// about authentication opt into the explicit test-only anonymous verifier.
// Production RouteOptions never use this helper and nil verifiers fail closed.
func registerAnonymousRoutesForTest(mux *http.ServeMux, st *store.Store, opts ...RouteOptions) {
	var opt RouteOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.AuthVerifier == nil {
		opt.AuthVerifier = auth.AllowAnonymousForTest()
	}
	if opt.MonitorAuthVerifier == nil {
		opt.MonitorAuthVerifier = auth.AllowAnonymousForTest()
	}
	RegisterRoutes(mux, st, opt)
}
