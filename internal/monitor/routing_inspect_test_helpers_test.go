package monitor

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// newInspectRouter builds the live router the inspector talks to from the same
// stored configuration the forwarding path is published from.
func newInspectRouter(t *testing.T, st *store.Store) *router.Router {
	t.Helper()
	targets, err := channel.NewService(st).RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	cfg := &config.Config{}
	cfg.Router.Selection.Policy = router.PolicyFirstAvailable
	rtr, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	if err := rtr.Reload(targets); err != nil {
		t.Fatalf("router.Reload() error = %v", err)
	}
	return rtr
}
