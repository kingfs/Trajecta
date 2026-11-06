package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestRatesAlertsSettingsAPI(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	manager := testJWTManager(t, time.Hour)
	adminToken := systemTestToken(t, manager, 1, "admin", "admin")
	viewerToken := systemTestToken(t, manager, 2, "viewer", "viewer")

	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	if rr := systemTestRequest(mux, "/api/settings/rates-alerts", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET without credentials = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	if rr := systemTestRequest(mux, "/api/settings/rates-alerts", viewerToken); rr.Code != http.StatusForbidden {
		t.Fatalf("GET as viewer = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	rr := systemTestRequest(mux, "/api/settings/rates-alerts", adminToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET as admin = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var initial ratesAlertsSettingsView
	if err := json.Unmarshal(rr.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode initial: %v", err)
	}
	if initial.Enabled {
		t.Fatalf("initial = %+v, want disabled", initial)
	}

	patch := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/settings/rates-alerts", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	if rr := patch(viewerToken, `{"enabled":true,"max_rpm":10}`); rr.Code != http.StatusForbidden {
		t.Fatalf("PATCH as viewer = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if rr := patch(adminToken, `{"enabled":true,"max_rpm":0,"max_tpm":0}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH enabled without thresholds = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if rr := patch(adminToken, `{"enabled":true,"max_rpm":10,`); rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH malformed = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	rr = patch(adminToken, `{"enabled":true,"max_rpm":42,"max_tpm":9000,"severity":"error"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH as admin = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var saved ratesAlertsSettingsView
	if err := json.Unmarshal(rr.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	if !saved.Enabled || saved.MaxRPM != 42 || saved.MaxTPM != 9000 || saved.Severity != "error" {
		t.Fatalf("saved = %+v, want {true 42 9000 error}", saved)
	}
	rr = systemTestRequest(mux, "/api/settings/rates-alerts", adminToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET after PATCH = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var reloaded ratesAlertsSettingsView
	if err := json.Unmarshal(rr.Body.Bytes(), &reloaded); err != nil {
		t.Fatalf("decode reloaded: %v", err)
	}
	if reloaded != saved {
		t.Fatalf("reloaded = %+v, want %+v", reloaded, saved)
	}
}

func TestMaybeEvaluateRatesAlertsWritesAndResolves(t *testing.T) {
	// This test swaps the package-global evaluator; keeping it serial avoids racing
	// the other evaluator test over the same variable.

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.SaveRatesAlertsSettings(t.Context(), store.RatesAlertsSettings{Enabled: true, MaxRPM: 1, MaxTPM: 1000000, Severity: "warning"}); err != nil {
		t.Fatalf("SaveRatesAlertsSettings() error = %v", err)
	}
	now := time.Now().UTC()
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		name := "alert-" + string(rune('a'+i)) + ".http"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("# alert\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = name
		header.Meta.Time = now.Add(-10 * time.Second)
		header.Meta.URL = "https://example.invalid/v1/chat/completions"
		header.Meta.Method = http.MethodPost
		header.Meta.StatusCode = http.StatusOK
		header.Meta.Model = "gpt-alert"
		header.Meta.ExchangeKind = "entry"
		header.Meta.DurationMs = 10
		header.Usage.TotalTokens = 25
		if err := st.UpsertLogWithGrouping(path, header, store.GroupingInfo{}); err != nil {
			t.Fatalf("UpsertLogWithGrouping() error = %v", err)
		}
	}

	evaluator := &ratesAlertsEvaluator{interval: time.Minute}
	previous := globalRatesAlertsEvaluator
	globalRatesAlertsEvaluator = evaluator
	t.Cleanup(func() { globalRatesAlertsEvaluator = previous })
	req := httptest.NewRequest(http.MethodGet, "/api/overview?sections=traffic", nil)
	maybeEvaluateRatesAlerts(st, req)

	event, err := st.GetSystemEventByFingerprint("rates:global")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(rates:global) error = %v", err)
	}
	if event.Category != "rate_breach" || event.Source != "rates" || event.Status != store.SystemEventStatusUnread {
		t.Fatalf("event = %+v, want rates/rate_breach unread", event)
	}
	modelEvent, err := st.GetSystemEventByFingerprint("rates:model:gpt-alert")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(model) error = %v", err)
	}
	if modelEvent.Model != "gpt-alert" {
		t.Fatalf("model event = %+v, want Model gpt-alert", modelEvent)
	}

	// Second pass within the throttle interval is a no-op (no occurrence bump).
	maybeEvaluateRatesAlerts(st, req)
	event2, err := st.GetSystemEventByFingerprint("rates:global")
	if err != nil {
		t.Fatalf("second fingerprint read error = %v", err)
	}
	if event2.OccurrenceCount != event.OccurrenceCount {
		t.Fatalf("occurrence = %d, want %d after throttled pass", event2.OccurrenceCount, event.OccurrenceCount)
	}

	// Raising the threshold past the current rate clears the breach on the
	// next evaluation. Advance past the throttle interval so this pass runs.
	evaluator.lastEval = time.Now().Add(-2 * time.Minute)
	if _, err := st.SaveRatesAlertsSettings(t.Context(), store.RatesAlertsSettings{Enabled: true, MaxRPM: 100, MaxTPM: 1000000, Severity: "warning"}); err != nil {
		t.Fatalf("SaveRatesAlertsSettings(cleared) error = %v", err)
	}
	maybeEvaluateRatesAlerts(st, req)
	cleared, err := st.GetSystemEventByFingerprint("rates:global")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(cleared) error = %v", err)
	}
	if cleared.Status != store.SystemEventStatusResolved {
		t.Fatalf("cleared status = %q, want resolved", cleared.Status)
	}
	clearedModel, err := st.GetSystemEventByFingerprint("rates:model:gpt-alert")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(cleared model) error = %v", err)
	}
	if clearedModel.Status != store.SystemEventStatusResolved {
		t.Fatalf("cleared model status = %q, want resolved", clearedModel.Status)
	}
}

// A scope that went silent (no traffic in the last minute) is resolved even
// though it was not evaluated this round; read/ignored states survive.
func TestMaybeEvaluateRatesAlertsSilentScope(t *testing.T) {
	// This test swaps the package-global evaluator; keeping it serial avoids racing
	// the other evaluator test over the same variable.

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.SaveRatesAlertsSettings(t.Context(), store.RatesAlertsSettings{Enabled: true, MaxRPM: 1, MaxTPM: 1000000, Severity: "warning"}); err != nil {
		t.Fatalf("SaveRatesAlertsSettings() error = %v", err)
	}
	now := time.Now().UTC()
	dir := t.TempDir()
	// Traffic only on an unrelated model, so gpt-silent has an existing
	// breach event but no traffic this round.
	for i := 0; i < 2; i++ {
		name := "silent-" + string(rune('a'+i)) + ".http"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("# silent\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = name
		header.Meta.Time = now.Add(-10 * time.Second)
		header.Meta.URL = "https://example.invalid/v1/chat/completions"
		header.Meta.Method = http.MethodPost
		header.Meta.StatusCode = http.StatusOK
		header.Meta.Model = "gpt-live"
		header.Meta.ExchangeKind = "entry"
		header.Meta.DurationMs = 10
		header.Usage.TotalTokens = 25
		if err := st.UpsertLogWithGrouping(path, header, store.GroupingInfo{}); err != nil {
			t.Fatalf("UpsertLogWithGrouping() error = %v", err)
		}
	}
	stale, err := store.RatesAlertSystemEvent(store.RatesBreach{Scope: store.RatesAlertScopeModel, Key: "gpt-silent", RPM: 9, TPM: 9, MaxRPM: 1, MaxTPM: 1000000, Sampled: now}, "warning")
	if err != nil {
		t.Fatalf("RatesAlertSystemEvent() error = %v", err)
	}
	if _, err := st.UpsertSystemEvent(stale); err != nil {
		t.Fatalf("UpsertSystemEvent() error = %v", err)
	}
	evaluator := &ratesAlertsEvaluator{interval: time.Minute}
	previous := globalRatesAlertsEvaluator
	globalRatesAlertsEvaluator = evaluator
	t.Cleanup(func() { globalRatesAlertsEvaluator = previous })
	req := httptest.NewRequest(http.MethodGet, "/api/overview?sections=traffic", nil)
	maybeEvaluateRatesAlerts(st, req)
	resolved, err := st.GetSystemEventByFingerprint("rates:model:gpt-silent")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(silent) error = %v", err)
	}
	if resolved.Status != store.SystemEventStatusResolved {
		t.Fatalf("silent status = %q, want resolved", resolved.Status)
	}
}

// Disabling rate alerts must not leave a breach event unread forever: with the
// evaluator off, nothing can ever raise or clear it again. The disabled path
// resolves unread rate events (the settings that produced them no longer exist)
// while leaving a deliberate read alone.
func TestMaybeEvaluateRatesAlertsResolvesStaleWhenDisabled(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	staleEvent, err := store.RatesAlertSystemEvent(store.RatesBreach{
		Scope: store.RatesAlertScopeGlobal, RPM: 5, TPM: 10, MaxRPM: 1, MaxTPM: 1000, Sampled: now,
	}, "warning")
	if err != nil {
		t.Fatalf("RatesAlertSystemEvent(stale) error = %v", err)
	}
	if _, err := st.UpsertSystemEvent(staleEvent); err != nil {
		t.Fatalf("UpsertSystemEvent(stale) error = %v", err)
	}
	readEvent, err := store.RatesAlertSystemEvent(store.RatesBreach{
		Scope: store.RatesAlertScopeModel, Key: "gpt-read", RPM: 5, TPM: 10, MaxRPM: 1, MaxTPM: 1000, Sampled: now,
	}, "warning")
	if err != nil {
		t.Fatalf("RatesAlertSystemEvent(read) error = %v", err)
	}
	saved, err := st.UpsertSystemEvent(readEvent)
	if err != nil {
		t.Fatalf("UpsertSystemEvent(read) error = %v", err)
	}
	if err := st.MarkSystemEventRead(saved.ID); err != nil {
		t.Fatalf("MarkSystemEventRead() error = %v", err)
	}

	// The settings were enabled at the time the stale event was raised, then
	// disabled: SaveRatesAlertsSettings keeps thresholds on a disabled config.
	if _, err := st.SaveRatesAlertsSettings(t.Context(), store.RatesAlertsSettings{Enabled: false, MaxRPM: 1, MaxTPM: 1000, Severity: "warning"}); err != nil {
		t.Fatalf("SaveRatesAlertsSettings(disabled) error = %v", err)
	}
	// Traffic above the thresholds that are no longer active must not raise new
	// events while alerting is disabled.
	seedRateAlertLog(t, st, "disabled-1.http", now.Add(-10*time.Second), 25, "gpt-disabled", http.StatusOK)
	seedRateAlertLog(t, st, "disabled-2.http", now.Add(-10*time.Second), 25, "gpt-disabled", 2)

	evaluator := &ratesAlertsEvaluator{interval: time.Minute}
	previous := globalRatesAlertsEvaluator
	globalRatesAlertsEvaluator = evaluator
	t.Cleanup(func() { globalRatesAlertsEvaluator = previous })
	req := httptest.NewRequest(http.MethodGet, "/api/overview?sections=traffic", nil)
	maybeEvaluateRatesAlerts(st, req)

	cleared, err := st.GetSystemEventByFingerprint("rates:global")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(global) error = %v", err)
	}
	if cleared.Status != store.SystemEventStatusResolved {
		t.Fatalf("disabled stale event status = %q, want resolved", cleared.Status)
	}
	stillRead, err := st.GetSystemEventByFingerprint("rates:model:gpt-read")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(read) error = %v", err)
	}
	if stillRead.Status != store.SystemEventStatusRead {
		t.Fatalf("read event status = %q, want read preserved", stillRead.Status)
	}
	fingerprints, err := st.ListRatesAlertFingerprints()
	if err != nil {
		t.Fatalf("ListRatesAlertFingerprints() error = %v", err)
	}
	for _, fingerprint := range fingerprints {
		if fingerprint == "rates:model:gpt-disabled" {
			t.Fatalf("fingerprints = %v, disabled alerting raised a breach", fingerprints)
		}
	}
	if len(fingerprints) != 2 {
		t.Fatalf("fingerprints = %v, want only the two seeded events", fingerprints)
	}
}

// seedRateAlertLog indexes one client-visible log row for the rate-alert tests.
func seedRateAlertLog(t *testing.T, st *store.Store, name string, at time.Time, tokens int, model string, statusCode int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("# rate alert\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = name
	header.Meta.Time = at
	header.Meta.URL = "https://example.invalid/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = statusCode
	header.Meta.Model = model
	header.Meta.ExchangeKind = "entry"
	header.Meta.DurationMs = 10
	header.Usage.TotalTokens = tokens
	if err := st.UpsertLogWithGrouping(path, header, store.GroupingInfo{}); err != nil {
		t.Fatalf("UpsertLogWithGrouping(%q) error = %v", name, err)
	}
}
