package monitor

import (
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// newTestStore opens the local dev/test harness database through the production
// constructor with the sqlite driver named explicitly, replacing the retired
// directory-only store.New entry point.
func newTestStore(outputDir string) (*store.Store, error) {
	return store.NewWithDatabase(outputDir, "sqlite", filepath.Join(outputDir, "trace_index.sqlite3"), 4, 4)
}

// newUpstreamListTestRouter builds a router publishing one static upstream, the
// way the forwarding path publishes routing state. The upstream list endpoint
// reads router snapshots only; the retired upstream_targets/upstream_models
// projection fallback is no longer available.
func newUpstreamListTestRouter(t *testing.T, upstreamID string, models ...string) *router.Router {
	t.Helper()
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             upstreamID,
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   models,
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
			},
		},
	}
	rtr, err := router.New(cfg)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("router.Initialize() error = %v", err)
	}
	return rtr
}
