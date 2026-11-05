package channel

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestModelAvailabilityClassifiesAgainstTheLatestSync(t *testing.T) {
	syncedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	channel := store.ChannelConfigRecord{ID: "p", LastProbeAt: syncedAt, LastProbeStatus: "success"}
	cases := []struct {
		name     string
		model    store.ChannelModelRecord
		want     ModelState
		routable bool
	}{
		{name: "synced and enabled", model: store.ChannelModelRecord{Model: "a", Enabled: true, Source: "discovered", LastProbeAt: syncedAt}, want: ModelStateEnabled, routable: true},
		{name: "switched off", model: store.ChannelModelRecord{Model: "b", Enabled: false, Source: "discovered", LastProbeAt: syncedAt}, want: ModelStateDisabled},
		{name: "absent from latest sync", model: store.ChannelModelRecord{Model: "c", Enabled: true, Source: "discovered", LastProbeAt: syncedAt.Add(-time.Hour)}, want: ModelStateRemoved},
		{name: "manual stays routable", model: store.ChannelModelRecord{Model: "d", Enabled: true, Source: "manual"}, want: ModelStateUnverified, routable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelAvailability(channel, tc.model); got != tc.want {
				t.Fatalf("ModelAvailability() = %q, want %q", got, tc.want)
			}
			if got := ModelRoutable(channel, tc.model); got != tc.routable {
				t.Fatalf("ModelRoutable() = %v, want %v", got, tc.routable)
			}
		})
	}

	neverSynced := store.ChannelConfigRecord{ID: "p"}
	if got := ModelAvailability(neverSynced, store.ChannelModelRecord{Model: "x", Enabled: true, Source: "static"}); got != ModelStateUnverified {
		t.Fatalf("a model on a never-synced channel = %q, want unverified", got)
	}
	failed := store.ChannelConfigRecord{ID: "p", LastProbeAt: syncedAt, LastProbeStatus: "failed"}
	if got := ModelAvailability(failed, store.ChannelModelRecord{Model: "x", Enabled: true, Source: "discovered", LastProbeAt: syncedAt.Add(-time.Hour)}); got != ModelStateUnverified {
		t.Fatalf("a failed sync must not remove models, got %q", got)
	}
}

func TestRuntimeTargetsExcludeRemovedModelsButKeepTheirSwitch(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	syncedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "p", Name: "Provider", BaseURL: "https://example.invalid/v1", ProviderPreset: "openai",
		Enabled: true, ModelDiscovery: "list_models", LastProbeAt: syncedAt, LastProbeStatus: "success",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceChannelModels("p", []store.ChannelModelRecord{
		{Model: "current", Source: "discovered", Enabled: true, LastProbeAt: syncedAt},
		{Model: "gone", DisplayName: "gone", Source: "discovered", Enabled: true, LastProbeAt: syncedAt.Add(-time.Hour)},
		{Model: "manual-one", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	targets, err := NewService(st).RuntimeTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	got := map[string]bool{}
	for _, model := range targets[0].StaticModels {
		got[model] = true
	}
	if !got["current"] || !got["manual-one"] {
		t.Fatalf("StaticModels = %v, want current and manual-one", targets[0].StaticModels)
	}
	if got["gone"] {
		t.Fatalf("StaticModels = %v, a removed model must not be routable", targets[0].StaticModels)
	}
	found := false
	for _, model := range targets[0].DisabledModels {
		if model == "gone" {
			found = true
		}
	}
	if !found {
		t.Fatalf("DisabledModels = %v, want the removed model", targets[0].DisabledModels)
	}
	// The operator's switch survives, so a later sync restores the model.
	rows, err := st.ListChannelModels("p", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Model == "gone" && !row.Enabled {
			t.Fatal("a removed model lost its enabled switch")
		}
	}
}

func TestProbeReportsAddedAndRemovedModels(t *testing.T) {
	var models = []string{"gpt-5", "gpt-4.1"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"data":[`
		for i, model := range models {
			if i > 0 {
				body += ","
			}
			body += `{"id":"` + model + `"}`
		}
		_, _ = w.Write([]byte(body + `]}`))
	}))
	t.Cleanup(upstream.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "p", Name: "P", BaseURL: upstream.URL + "/v1", ProviderPreset: "openai",
		Enabled: true, ModelDiscovery: "list_models",
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st)
	if _, err := svc.ProbeWithOptions("p", ProbeOptions{}); err != nil {
		t.Fatal(err)
	}

	models = []string{"gpt-5", "gpt-5-mini"}
	result, err := svc.ProbeWithOptions("p", ProbeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.AddedCount != 1 || result.RemovedCount != 1 {
		t.Fatalf("added/removed = %d/%d, want 1/1", result.AddedCount, result.RemovedCount)
	}
}
