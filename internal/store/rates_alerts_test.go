package store

import (
	"context"
	"testing"
	"time"
)

// Disabled alerts never breach.
func TestRatesAlertsDisabledNeverBreaches(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	settings, err := st.LoadRatesAlertsSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadRatesAlertsSettings() error = %v", err)
	}
	if settings != (RatesAlertsSettings{}) {
		t.Fatalf("default settings = %+v, want disabled zero value", settings)
	}
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		seedRateLog(t, st, dir, alertLogName(i), now.Add(-10*time.Second), 100, "gpt-alert")
	}
	breaches, evaluated, err := st.EvaluateRatesAlerts(RatesAlertsSettings{Enabled: false, MaxRPM: 1})
	if err != nil {
		t.Fatalf("EvaluateRatesAlerts() error = %v", err)
	}
	if len(breaches) != 0 || len(evaluated) != 0 {
		t.Fatalf("disabled breaches=%+v evaluated=%v, want none", breaches, evaluated)
	}
}

// Validation and persistence round-trip.
func TestRatesAlertsSettingsValidationAndPersistence(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.SaveRatesAlertsSettings(context.Background(), RatesAlertsSettings{Enabled: true, MaxRPM: 0, MaxTPM: 0}); err == nil {
		t.Fatal("enabled with no thresholds error = nil, want validation failure")
	}
	if _, err := st.SaveRatesAlertsSettings(context.Background(), RatesAlertsSettings{Enabled: true, MaxRPM: 10, Severity: "bogus"}); err == nil {
		t.Fatal("bogus severity error = nil, want validation failure")
	}
	saved, err := st.SaveRatesAlertsSettings(context.Background(), RatesAlertsSettings{Enabled: true, MaxRPM: 10, MaxTPM: 1000, Severity: " ERROR "})
	if err != nil {
		t.Fatalf("SaveRatesAlertsSettings() error = %v", err)
	}
	if saved.Severity != "error" || saved.MaxRPM != 10 || saved.MaxTPM != 1000 || !saved.Enabled {
		t.Fatalf("saved = %+v, want normalized {true 10 1000 error}", saved)
	}
	loaded, err := st.LoadRatesAlertsSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadRatesAlertsSettings() error = %v", err)
	}
	if loaded != saved {
		t.Fatalf("loaded = %+v, want %+v", loaded, saved)
	}
}

// Global and per-model breaches, event building, and clear semantics are
// pinned through the store surface with the events API in monitor tests.
func TestRatesAlertsEvaluateGlobalAndModel(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		seedRateLog(t, st, dir, alertLogName(10+i), now.Add(-10*time.Second), 50, "gpt-alert")
	}
	settings := RatesAlertsSettings{Enabled: true, MaxRPM: 2, MaxTPM: 10000, Severity: "warning"}
	breaches, evaluated, err := st.EvaluateRatesAlerts(settings)
	if err != nil {
		t.Fatalf("EvaluateRatesAlerts() error = %v", err)
	}
	byScope := map[string]RatesBreach{}
	for _, breach := range breaches {
		byScope[string(breach.Scope)+":"+breach.Key] = breach
	}
	global, ok := byScope["global:"]
	if !ok || global.RPM != 3 {
		t.Fatalf("global breach = %+v (present=%v), want RPM 3 over max 2", global, ok)
	}
	model, ok := byScope["model:gpt-alert"]
	if !ok || model.RPM != 3 {
		t.Fatalf("model breach = %+v (present=%v), want RPM 3 over max 2", model, ok)
	}
	if len(evaluated) == 0 {
		t.Fatal("evaluated scopes is empty, want global at least")
	}
	if global.Fingerprint() != "rates:global" || model.Fingerprint() != "rates:model:gpt-alert" {
		t.Fatalf("fingerprints = %q/%q, want rates:global and rates:model:gpt-alert", global.Fingerprint(), model.Fingerprint())
	}
	event, err := RatesAlertSystemEvent(global, "warning")
	if err != nil {
		t.Fatalf("RatesAlertSystemEvent() error = %v", err)
	}
	if event.Source != "rates" || event.Category != "rate_breach" || event.Fingerprint != "rates:global" {
		t.Fatalf("event = %+v, want source rates / category rate_breach / rates:global", event)
	}
	if event.Title == "" || len(event.DetailsJSON) == 0 {
		t.Fatalf("event title/details missing: title=%q details=%s", event.Title, event.DetailsJSON)
	}
	modelEvent, err := RatesAlertSystemEvent(model, "error")
	if err != nil {
		t.Fatalf("RatesAlertSystemEvent(model) error = %v", err)
	}
	if modelEvent.Model != "gpt-alert" || modelEvent.Severity != "error" {
		t.Fatalf("model event = %+v, want Model gpt-alert / error", modelEvent)
	}

	// Raising the threshold past the current rate clears the breach: the
	// evaluator then returns no crossing but still lists the scope so the
	// caller can resolve the existing event.
	breaches, evaluated, err = st.EvaluateRatesAlerts(RatesAlertsSettings{Enabled: true, MaxRPM: 100, Severity: "warning"})
	if err != nil {
		t.Fatalf("EvaluateRatesAlerts(cleared) error = %v", err)
	}
	if len(breaches) != 0 {
		t.Fatalf("cleared breaches = %v, want none", breaches)
	}
	found := false
	for _, scope := range evaluated {
		if scope == "rates:global" {
			found = true
		}
	}
	if !found {
		t.Fatalf("evaluated = %v, want rates:global present", evaluated)
	}
}

func alertLogName(i int) string {
	return "rates-alert-" + string(rune('a'+i)) + ".http"
}
