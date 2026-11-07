package channel

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// TestModelAvailabilityClassifiesAgainstTheLatestSuccessfulSync pins the
// availability truth table. The anchor is the channel's latest SUCCESSFUL probe
// (channel.last_probe_at), so a failed probe - which only updates the displayed
// status - must move no row in either direction. Rows the operator declared
// (manual or YAML static) are never classified "removed" merely because a
// discovery does not list them.
func TestModelAvailabilityClassifiesAgainstTheLatestSuccessfulSync(t *testing.T) {
	syncedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	synced := store.ChannelConfigRecord{ID: "p", LastProbeAt: syncedAt, LastProbeStatus: "success"}
	// A failed probe records its status but leaves the last successful anchor
	// in place.
	failed := store.ChannelConfigRecord{ID: "p", LastProbeAt: syncedAt, LastProbeStatus: "failed"}
	cases := []struct {
		name     string
		channel  store.ChannelConfigRecord
		model    store.ChannelModelRecord
		want     ModelState
		routable bool
	}{
		{
			name:    "listed by the latest successful sync",
			channel: synced,
			model:   store.ChannelModelRecord{Model: "a", Enabled: true, Source: "discovered", LastProbeAt: syncedAt},
			want:    ModelStateEnabled, routable: true,
		},
		{
			name:    "listed by the latest success stays enabled after a failed probe",
			channel: failed,
			model:   store.ChannelModelRecord{Model: "a", Enabled: true, Source: "discovered", LastProbeAt: syncedAt},
			want:    ModelStateEnabled, routable: true,
		},
		{
			name:    "switched off",
			channel: synced,
			model:   store.ChannelModelRecord{Model: "b", Enabled: false, Source: "discovered", LastProbeAt: syncedAt},
			want:    ModelStateDisabled,
		},
		{
			name:    "absent from the latest successful sync",
			channel: synced,
			model:   store.ChannelModelRecord{Model: "c", Enabled: true, Source: "discovered", LastProbeAt: syncedAt.Add(-time.Hour)},
			want:    ModelStateRemoved,
		},
		{
			name:    "removed by the latest success stays removed after a failed probe",
			channel: failed,
			model:   store.ChannelModelRecord{Model: "c", Enabled: true, Source: "discovered", LastProbeAt: syncedAt.Add(-time.Hour)},
			want:    ModelStateRemoved,
		},
		{
			name:    "manual stays routable",
			channel: synced,
			model:   store.ChannelModelRecord{Model: "d", Enabled: true, Source: "manual"},
			want:    ModelStateUnverified, routable: true,
		},
		{
			name:    "static declared in YAML stays routable when discovery does not list it",
			channel: synced,
			model:   store.ChannelModelRecord{Model: "e", Enabled: true, Source: "static"},
			want:    ModelStateUnverified, routable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelAvailability(tc.channel, tc.model); got != tc.want {
				t.Fatalf("ModelAvailability() = %q, want %q", got, tc.want)
			}
			if got := ModelRoutable(tc.channel, tc.model); got != tc.routable {
				t.Fatalf("ModelRoutable() = %v, want %v", got, tc.routable)
			}
		})
	}

	neverSynced := store.ChannelConfigRecord{ID: "p"}
	if got := ModelAvailability(neverSynced, store.ChannelModelRecord{Model: "x", Enabled: true, Source: "discovered"}); got != ModelStateUnverified {
		t.Fatalf("a model on a never-synced channel = %q, want unverified", got)
	}
	failedOnly := store.ChannelConfigRecord{ID: "p", LastProbeStatus: "failed"}
	if got := ModelAvailability(failedOnly, store.ChannelModelRecord{Model: "x", Enabled: true, Source: "discovered"}); got != ModelStateUnverified {
		t.Fatalf("a model on a channel that only ever failed = %q, want unverified", got)
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
		{Model: "static-one", Source: "static", Enabled: true},
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
	if !got["current"] || !got["manual-one"] || !got["static-one"] {
		t.Fatalf("StaticModels = %v, want current, manual-one and static-one", targets[0].StaticModels)
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

// TestFailedProbeKeepsTheLatestSuccessfulSnapshot drives the real probe path:
// a failed discovery records a failed status and a probe run, but it must not
// move the last successful anchor or resurrect a model an earlier success
// removed.
func TestFailedProbeKeepsTheLatestSuccessfulSnapshot(t *testing.T) {
	models := []string{"gpt-5", "gpt-4.1"}
	failing := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing {
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
			return
		}
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
	t.Cleanup(upstreamServer.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "p", Name: "P", BaseURL: upstreamServer.URL + "/v1", ProviderPreset: "openai",
		Enabled: true, ModelDiscovery: "list_models",
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st)
	enable := true
	if _, err := svc.ProbeWithOptions("p", ProbeOptions{EnableDiscovered: &enable}); err != nil {
		t.Fatal(err)
	}

	// The next successful sync stops listing gpt-4.1: it is removed.
	models = []string{"gpt-5"}
	result, err := svc.ProbeWithOptions("p", ProbeOptions{EnableDiscovered: &enable})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedCount != 1 {
		t.Fatalf("RemovedCount = %d, want 1", result.RemovedCount)
	}
	synced, err := st.GetChannelConfig("p")
	if err != nil {
		t.Fatal(err)
	}
	anchor := synced.LastProbeAt
	if anchor.IsZero() {
		t.Fatal("the successful probe did not record the availability anchor")
	}

	failing = true
	result, err = svc.ProbeWithOptions("p", ProbeOptions{EnableDiscovered: &enable})
	if err == nil {
		t.Fatal("ProbeWithOptions() error = nil, want the discovery failure")
	}
	if result.Status != "failed" {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	afterFailure, err := st.GetChannelConfig("p")
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.LastProbeStatus != "failed" {
		t.Fatalf("LastProbeStatus = %q, want failed", afterFailure.LastProbeStatus)
	}
	if !afterFailure.LastProbeAt.Equal(anchor) {
		t.Fatalf("a failed probe moved the last successful anchor: %s -> %s", anchor, afterFailure.LastProbeAt)
	}

	targets, err := svc.RuntimeTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if !slices.Contains(targets[0].StaticModels, "gpt-5") {
		t.Fatalf("StaticModels = %v, want the model the last successful sync listed", targets[0].StaticModels)
	}
	if slices.Contains(targets[0].StaticModels, "gpt-4.1") {
		t.Fatalf("StaticModels = %v, a failed probe resurrected a removed model", targets[0].StaticModels)
	}
	if !slices.Contains(targets[0].DisabledModels, "gpt-4.1") {
		t.Fatalf("DisabledModels = %v, want the removed model", targets[0].DisabledModels)
	}
}

// TestBootstrapStaticModelsSurviveSuccessfulSync covers the YAML upgrade path:
// the first bootstrap stores operator-declared models with source "static" and
// no probe timestamp, and a later successful discovery that does not list one
// of them must not classify it removed.
// TestLegacyStaleProbeAnchorIsRepairedByTheNextSuccessfulProbe pins the upgrade
// behaviour for databases written before a failed probe stopped moving the
// availability anchor: those rows can hold a channel.last_probe_at that a
// failed probe overwrote, later than every channel_models.last_probe_at.
//
// Availability is derived from the latest successful probe, so such an anchor
// is not evidence about any row. The failure mode on upgrade is therefore
// fail-closed - the channel's discovered rows classify as removed until a
// successful sync writes a real anchor - and one successful sync repairs the
// channel. A failed probe neither repairs nor worsens it.
func TestLegacyStaleProbeAnchorIsRepairedByTheNextSuccessfulProbe(t *testing.T) {
	models := []string{"gpt-5"}
	failing := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing {
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
			return
		}
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
	t.Cleanup(upstreamServer.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lastSuccess := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	staleFailure := lastSuccess.Add(time.Hour)
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "p", Name: "P", BaseURL: upstreamServer.URL + "/v1", ProviderPreset: "openai",
		Enabled: true, ModelDiscovery: "list_models",
		LastProbeAt: staleFailure, LastProbeStatus: "failed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceChannelModels("p", []store.ChannelModelRecord{
		{Model: "gpt-5", Source: "discovered", Enabled: true, LastProbeAt: lastSuccess},
		{Model: "gone", Source: "discovered", Enabled: true, LastProbeAt: lastSuccess.Add(-time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(st)
	assertStaleWindow := func(t *testing.T) {
		t.Helper()
		targets, err := svc.RuntimeTargets()
		if err != nil {
			t.Fatal(err)
		}
		if len(targets) != 1 {
			t.Fatalf("targets = %d, want 1", len(targets))
		}
		if len(targets[0].StaticModels) != 0 {
			t.Fatalf("StaticModels = %v, a legacy stale anchor must not widen routability", targets[0].StaticModels)
		}
		if !slices.Contains(targets[0].DisabledModels, "gpt-5") || !slices.Contains(targets[0].DisabledModels, "gone") {
			t.Fatalf("DisabledModels = %v, want both rows excluded", targets[0].DisabledModels)
		}
	}
	assertStaleWindow(t)

	// A failed probe cannot repair a stale anchor, because it must not move it.
	failing = true
	if _, err := svc.ProbeWithOptions("p", ProbeOptions{}); err == nil {
		t.Fatal("ProbeWithOptions() error = nil, want the discovery failure")
	}
	stale, err := st.GetChannelConfig("p")
	if err != nil {
		t.Fatal(err)
	}
	if !stale.LastProbeAt.Equal(staleFailure) {
		t.Fatalf("a failed probe moved the legacy anchor: %s -> %s", staleFailure, stale.LastProbeAt)
	}
	assertStaleWindow(t)

	// One successful sync writes a real anchor and repairs the classification.
	failing = false
	if _, err := svc.ProbeWithOptions("p", ProbeOptions{}); err != nil {
		t.Fatal(err)
	}
	repaired, err := st.GetChannelConfig("p")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.LastProbeStatus != "success" || repaired.LastProbeAt.Equal(staleFailure) || repaired.LastProbeAt.Before(staleFailure) {
		t.Fatalf("a successful sync did not repair the anchor: status=%q at=%s", repaired.LastProbeStatus, repaired.LastProbeAt)
	}
	targets, err := svc.RuntimeTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if !slices.Contains(targets[0].StaticModels, "gpt-5") {
		t.Fatalf("StaticModels = %v, want the repaired model routable", targets[0].StaticModels)
	}
	if slices.Contains(targets[0].StaticModels, "gone") {
		t.Fatalf("StaticModels = %v, a repairing sync must keep the removed model removed", targets[0].StaticModels)
	}
}

func TestBootstrapStaticModelsSurviveSuccessfulSync(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"listed-model"}]}`))
	}))
	t.Cleanup(upstreamServer.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "pin",
		Name:           "pin",
		BaseURL:        upstreamServer.URL + "/v1",
		ProviderPreset: "openai",
		ModelDiscovery: "list_models",
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertChannelModel("pin", store.ChannelModelRecord{Model: "pinned-model", Source: "static", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st)

	enable := true
	result, err := svc.ProbeWithOptions("pin", ProbeOptions{EnableDiscovered: &enable})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedCount != 0 {
		t.Fatalf("RemovedCount = %d, want 0: a discovery that does not list a YAML static model removed it", result.RemovedCount)
	}

	channelRecord, err := st.GetChannelConfig("pin")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListChannelModels("pin", false)
	if err != nil {
		t.Fatal(err)
	}
	pinned, ok := findChannelModel(rows, "pinned-model")
	if !ok {
		t.Fatal("the YAML static model row disappeared after a successful sync")
	}
	if got := ModelAvailability(channelRecord, pinned); got != ModelStateUnverified {
		t.Fatalf("static model availability = %q, want unverified", got)
	}
	if !ModelRoutable(channelRecord, pinned) {
		t.Fatal("the YAML static model is not routable after a successful sync")
	}

	targets, err := svc.RuntimeTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if !slices.Contains(targets[0].StaticModels, "pinned-model") {
		t.Fatalf("StaticModels = %v, want the YAML static model", targets[0].StaticModels)
	}
	if !slices.Contains(targets[0].StaticModels, "listed-model") {
		t.Fatalf("StaticModels = %v, want the discovered model", targets[0].StaticModels)
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
