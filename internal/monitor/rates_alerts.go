// Rate quota alerts: threshold settings and the opportunistic evaluation that
// turns trailing-60s RPM/TPM crossings into system_events. Alerts never block
// or rewrite traffic; they only tell the operator the last minute ran past
// the configured numbers.

package monitor

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

type ratesAlertsSettingsView struct {
	Enabled  bool   `json:"enabled"`
	MaxRPM   int    `json:"max_rpm"`
	MaxTPM   int    `json:"max_tpm"`
	Severity string `json:"severity"`
}

func newRatesAlertsSettingsView(settings store.RatesAlertsSettings) ratesAlertsSettingsView {
	settings = settings.Normalize()
	return ratesAlertsSettingsView{
		Enabled:  settings.Enabled,
		MaxRPM:   settings.MaxRPM,
		MaxTPM:   settings.MaxTPM,
		Severity: settings.Severity,
	}
}

// ratesAlertsSettingsAPIHandler serves GET and PATCH
// /api/settings/rates-alerts. Admin-only like the network proxy settings:
// changing an alert threshold changes what the monitor surfaces.
func ratesAlertsSettingsAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			settings, err := st.LoadRatesAlertsSettings(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, newRatesAlertsSettingsView(settings))
		case http.MethodPatch:
			var req ratesAlertsSettingsView
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid rates alerts settings payload"})
				return
			}
			settings, err := st.SaveRatesAlertsSettings(r.Context(), store.RatesAlertsSettings{
				Enabled:  req.Enabled,
				MaxRPM:   req.MaxRPM,
				MaxTPM:   req.MaxTPM,
				Severity: req.Severity,
			})
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, newRatesAlertsSettingsView(settings))
		default:
			http.NotFound(w, r)
		}
	}
}

// ratesAlertsEvaluator owns the evaluation throttle. Overview already polls
// about once a minute per open tab, so one evaluation per minute process-wide
// is the steady-state cadence; more than one in-flight evaluation is dropped
// rather than queued.
type ratesAlertsEvaluator struct {
	mu       sync.Mutex
	lastEval time.Time
	inFlight bool
	interval time.Duration
}

var globalRatesAlertsEvaluator = &ratesAlertsEvaluator{interval: time.Minute}

// maybeEvaluateRatesAlerts is called after a successful traffic Overview: it
// raises or (when the scope clears) resolves the breach events for the
// configured thresholds. It handles concurrency explicitly (drop overlapping
// runs) so concurrent Overview hits cannot double-count occurrences.
func maybeEvaluateRatesAlerts(st *store.Store, r *http.Request) {
	evaluator := globalRatesAlertsEvaluator
	evaluator.mu.Lock()
	if evaluator.inFlight || (!evaluator.lastEval.IsZero() && time.Since(evaluator.lastEval) < evaluator.interval) {
		evaluator.mu.Unlock()
		return
	}
	evaluator.inFlight = true
	evaluator.lastEval = time.Now()
	evaluator.mu.Unlock()
	defer func() {
		evaluator.mu.Lock()
		evaluator.inFlight = false
		evaluator.mu.Unlock()
	}()

	settings, err := st.LoadRatesAlertsSettings(r.Context())
	if err != nil || !settings.Enabled {
		return
	}
	settings = settings.Normalize()
	breaches, evaluated, err := st.EvaluateRatesAlerts(settings)
	if err != nil {
		return
	}
	breached := make(map[string]bool, len(breaches))
	for _, breach := range breaches {
		event, err := store.RatesAlertSystemEvent(breach, settings.Severity)
		if err != nil {
			continue
		}
		if _, err := st.UpsertSystemEvent(event); err != nil {
			continue
		}
		breached[breach.Fingerprint()] = true
	}
	// Resolve previously breached scopes that have cleared, but only when the
	// operator never read them: a deliberate read/ignore must survive the
	// automatic clear. Every unread fingerprint either breached (keep) or
	// cleared/silent (resolve), including scopes that went silent with no
	// traffic this round and were not evaluated.
	_ = evaluated
	unread, err := st.ListRatesAlertFingerprints(store.SystemEventStatusUnread)
	if err != nil {
		return
	}
	for _, fingerprint := range unread {
		if breached[fingerprint] {
			continue
		}
		event, err := st.GetSystemEventByFingerprint(fingerprint)
		if err != nil {
			continue
		}
		_ = st.ResolveSystemEvent(event.ID)
	}
}
