package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/store"
)

func TestChannelModelsAPICarriesSyncState(t *testing.T) {
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
		{Model: "off", Source: "discovered", Enabled: false, LastProbeAt: syncedAt},
		{Model: "gone", Source: "discovered", Enabled: true, LastProbeAt: syncedAt.Add(-time.Hour)},
		{Model: "manual-one", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	channelDetailAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/channels/p/models", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var payload channelModelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	got := map[string]channelModelItem{}
	for _, item := range payload.Items {
		got[item.Model] = item
	}
	for _, tc := range []struct {
		model    string
		state    channel.ModelState
		routable bool
	}{
		{"current", channel.ModelStateEnabled, true},
		{"off", channel.ModelStateDisabled, false},
		{"gone", channel.ModelStateRemoved, false},
		{"manual-one", channel.ModelStateUnverified, true},
	} {
		item, ok := got[tc.model]
		if !ok {
			t.Fatalf("model %q missing from the response: %+v", tc.model, payload.Items)
		}
		if item.State != string(tc.state) {
			t.Fatalf("%s state = %q, want %q", tc.model, item.State, tc.state)
		}
		if item.Routable != tc.routable {
			t.Fatalf("%s routable = %v, want %v", tc.model, item.Routable, tc.routable)
		}
	}
}

func TestProviderDetailModelUsageCarriesSyncState(t *testing.T) {
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
		{Model: "current", DisplayName: "current", Source: "discovered", Enabled: true, LastProbeAt: syncedAt},
		{Model: "gone", DisplayName: "gone", Source: "discovered", Enabled: true, LastProbeAt: syncedAt.Add(-time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	channelDetailAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/channels/p", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var item channelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, model := range item.ModelsUsage {
		states[model.Model] = model.State
	}
	if states["current"] != string(channel.ModelStateEnabled) {
		t.Fatalf("current state = %q, all = %+v", states["current"], item.ModelsUsage)
	}
	if states["gone"] != string(channel.ModelStateRemoved) {
		t.Fatalf("gone state = %q, all = %+v", states["gone"], item.ModelsUsage)
	}
}
