package router

import (
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
)

func staticRefreshTarget(id string, models ...string) config.UpstreamTargetConfig {
	return config.UpstreamTargetConfig{
		ID:             id,
		Enabled:        boolPtr(true),
		Priority:       100,
		ModelDiscovery: ModelDiscoveryStaticOnly,
		StaticModels:   models,
		Upstream: config.UpstreamConfig{
			BaseURL:        "https://api.openai.com/v1",
			ProviderPreset: "openai",
		},
	}
}

// Committing configuration can take a database round trip. Routing must keep
// working while it settles: the router lock has to be released before commit.
func TestReloadWithCommitReleasesRouterLockWhileCommitting(t *testing.T) {
	rtr, err := New(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rtr.Reload([]config.UpstreamTargetConfig{staticRefreshTarget("primary", "gpt-5")}); err != nil {
		t.Fatal(err)
	}

	committing := make(chan struct{})
	allowCommit := make(chan struct{})
	reloaded := make(chan error, 1)
	go func() {
		reloaded <- rtr.ReloadWithCommit([]config.UpstreamTargetConfig{staticRefreshTarget("next", "gpt-5")}, func() error {
			close(committing)
			<-allowCommit
			return nil
		})
	}()
	<-committing

	targets := make(chan int, 1)
	go func() { targets <- len(rtr.Targets()) }()
	select {
	case count := <-targets:
		if count != 1 {
			t.Fatalf("targets visible during commit = %d, want the previous snapshot with 1", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("router lock is held while the configuration transaction commits")
	}

	close(allowCommit)
	if err := <-reloaded; err != nil {
		t.Fatalf("ReloadWithCommit() error = %v", err)
	}
	got := rtr.Targets()
	if len(got) != 1 || got[0].ID != "next" {
		t.Fatalf("published targets = %v, want next", got)
	}
}

// The router is a projection of channel configuration, not its owner: a reload
// and a background refresh must leave the legacy snapshot tables untouched.
func TestRouterNeverWritesTheApplicationDatabase(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rtr, err := New(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rtr.ReloadWithCommit([]config.UpstreamTargetConfig{staticRefreshTarget("primary", "gpt-5")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rtr.refreshTargets(rtr.Targets()); err != nil {
		t.Fatalf("refreshTargets() error = %v", err)
	}
	if rows, err := st.ListUpstreamTargets(); err != nil {
		t.Fatal(err)
	} else if len(rows) != 0 {
		t.Fatalf("router wrote %d upstream_targets rows", len(rows))
	}
	if rows, err := st.ListUpstreamModels(); err != nil {
		t.Fatal(err)
	} else if len(rows) != 0 {
		t.Fatalf("router wrote %d upstream_models rows", len(rows))
	}
}
