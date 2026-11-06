package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Rate (RPM/TPM) helpers.
//
// RPM is requests per minute and TPM is tokens per minute, the two vendor-style
// rate metrics operators compare against provider quotas. All three read the
// same indexed logs rows every other aggregate reads; there is no new table
// and no new column. Every caller passes the exact where clause of the
// aggregate it annotates, so a rate is a rate over the same rows as the
// totals it sits next to, including the client-visible filter.
//
// Two token conventions coexist on purpose, each surface keeps the one its
// totals already use: Overview and Usage totals sum total_tokens over every
// row, while Stats and Upstream totals only count tokens of 2xx responses.
// The helpers are named after the convention so a new caller picks one
// deliberately instead of inheriting one by accident.
const currentRateWindow = time.Minute

// rateWindowMinutes returns the span of [from, ref] in minutes, or 0 when the
// window has no measurable span (zero from, or ref not after from). A zero
// window means "all time" at the call sites that use it: average rates are
// reported as 0 rather than dividing by an unbounded span.
func rateWindowMinutes(from, ref time.Time) float64 {
	if from.IsZero() || ref.IsZero() || !ref.After(from) {
		return 0
	}
	return ref.Sub(from).Minutes()
}

// rateAvg divides window totals by the window span in minutes.
func rateAvg(count, tokens int, windowMinutes float64) (avgRPM, avgTPM float64) {
	if windowMinutes <= 0 {
		return 0, 0
	}
	return float64(count) / windowMinutes, float64(tokens) / windowMinutes
}

// currentRatesAll counts the trailing-60s rows matched by whereSQL: COUNT(*)
// for RPM and SUM(total_tokens) over every row for TPM. This is the Overview /
// Usage convention, whose window totals use the same unconditional token sum.
func (s *Store) currentRatesAll(whereSQL string, whereArgs []any) (rpm, tpm int, err error) {
	cutoff := time.Now().UTC().Add(-currentRateWindow).Format(timeLayout)
	args := append(append([]any(nil), whereArgs...), cutoff)
	err = s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM logs
		WHERE `+whereSQL+` AND recorded_at >= ?
	`, args...).Scan(&rpm, &tpm)
	return rpm, tpm, err
}

// currentRatesSuccessTokens counts the trailing-60s rows matched by whereSQL:
// COUNT(*) for RPM and SUM(total_tokens) over 2xx responses only for TPM.
// This is the Stats / Upstream convention, whose window totals use that same
// success-only token sum.
func (s *Store) currentRatesSuccessTokens(whereSQL string, whereArgs []any) (rpm, tpm int, err error) {
	cutoff := time.Now().UTC().Add(-currentRateWindow).Format(timeLayout)
	args := append(append([]any(nil), whereArgs...), cutoff)
	err = s.db.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN total_tokens ELSE 0 END), 0)
		FROM logs
		WHERE `+whereSQL+` AND recorded_at >= ?
	`, args...).Scan(&rpm, &tpm)
	return rpm, tpm, err
}

// currentRatesAllByGroup returns the trailing-60s COUNT(*)/SUM(total_tokens)
// for each value of groupKey, keyed by the raw column value. groupKey must be
// a validated column (see usageSummaryGroupKeys); it is interpolated into the
// query text.
func (s *Store) currentRatesAllByGroup(groupKey, whereSQL string, whereArgs []any) (map[string]struct {
	rpm int
	tpm int
}, error) {
	cutoff := time.Now().UTC().Add(-currentRateWindow).Format(timeLayout)
	args := append(append([]any(nil), whereArgs...), cutoff)
	rows, err := s.db.Query(`
		SELECT `+groupKey+`, COUNT(*), COALESCE(SUM(total_tokens), 0)
		FROM logs
		WHERE `+whereSQL+` AND recorded_at >= ?
		GROUP BY `+groupKey+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct {
		rpm int
		tpm int
	}{}
	for rows.Next() {
		var group string
		var rpm, tpm int
		if err := rows.Scan(&group, &rpm, &tpm); err != nil {
			return nil, err
		}
		if group == "" {
			continue
		}
		out[group] = struct {
			rpm int
			tpm int
		}{rpm: rpm, tpm: tpm}
	}
	return out, rows.Err()
}

// currentRatesSuccessTokensByGroup is currentRatesAllByGroup with the
// success-only token sum (Stats / Upstream convention).
func (s *Store) currentRatesSuccessTokensByGroup(groupKey, whereSQL string, whereArgs []any) (map[string]struct {
	rpm int
	tpm int
}, error) {
	cutoff := time.Now().UTC().Add(-currentRateWindow).Format(timeLayout)
	args := append(append([]any(nil), whereArgs...), cutoff)
	rows, err := s.db.Query(`
		SELECT
			`+groupKey+`,
			COUNT(*),
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN total_tokens ELSE 0 END), 0)
		FROM logs
		WHERE `+whereSQL+` AND recorded_at >= ?
		GROUP BY `+groupKey+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct {
		rpm int
		tpm int
	}{}
	for rows.Next() {
		var group string
		var rpm, tpm int
		if err := rows.Scan(&group, &rpm, &tpm); err != nil {
			return nil, err
		}
		if group == "" {
			continue
		}
		out[group] = struct {
			rpm int
			tpm int
		}{rpm: rpm, tpm: tpm}
	}
	return out, rows.Err()
}

// RatesAlertsSettings controls observation-only rate quota alerts. Alerts never
// block or rewrite traffic: when the trailing-60s rate crosses a threshold, an
// alert event is raised into system_events so the Monitor can show it. A zero
// threshold disables that dimension. The same numeric thresholds apply to the
// global scope and to each upstream and model dimension.
type RatesAlertsSettings struct {
	Enabled  bool   `json:"enabled"`
	MaxRPM   int    `json:"max_rpm"`
	MaxTPM   int    `json:"max_tpm"`
	Severity string `json:"severity"`
}

// RatesAlertsSettingKey is the app_settings row holding the alert settings.
const RatesAlertsSettingKey = "rates.alerts"

// RatesAlertSeverities are the allowed event severities.
var RatesAlertSeverities = map[string]struct{}{
	"warning":  {},
	"error":    {},
	"critical": {},
}

// Normalize trims and defaults the settings for storage and comparison.
func (s RatesAlertsSettings) Normalize() RatesAlertsSettings {
	s.Severity = strings.ToLower(strings.TrimSpace(s.Severity))
	if s.Severity == "" {
		s.Severity = "warning"
	}
	if s.MaxRPM < 0 {
		s.MaxRPM = 0
	}
	if s.MaxTPM < 0 {
		s.MaxTPM = 0
	}
	return s
}

// Validate rejects an enabled configuration that watches nothing, an invalid
// severity, or negative thresholds. The checks run on the raw values, before
// normalization, so a negative threshold is reported rather than silently
// clamped to a disabled dimension.
func (s RatesAlertsSettings) Validate() error {
	if s.MaxRPM < 0 || s.MaxTPM < 0 {
		return errors.New("max_rpm and max_tpm must be >= 0")
	}
	normalized := s.Normalize()
	if _, ok := RatesAlertSeverities[normalized.Severity]; !ok {
		return fmt.Errorf("severity %q is not supported; use warning, error or critical", s.Severity)
	}
	if normalized.Enabled && normalized.MaxRPM == 0 && normalized.MaxTPM == 0 {
		return errors.New("enabled alerts need a positive max_rpm or max_tpm")
	}
	return nil
}

// LoadRatesAlertsSettings reads the alert settings, returning the zero value
// (disabled) when none were saved.
func (s *Store) LoadRatesAlertsSettings(ctx context.Context) (RatesAlertsSettings, error) {
	var settings RatesAlertsSettings
	found, err := s.LoadAppSettingJSON(ctx, RatesAlertsSettingKey, &settings)
	if err != nil {
		return RatesAlertsSettings{}, err
	}
	if !found {
		return RatesAlertsSettings{}, nil
	}
	return settings.Normalize(), nil
}

// SaveRatesAlertsSettings validates and persists the alert settings.
func (s *Store) SaveRatesAlertsSettings(ctx context.Context, settings RatesAlertsSettings) (RatesAlertsSettings, error) {
	if err := settings.Validate(); err != nil {
		return RatesAlertsSettings{}, err
	}
	normalized := settings.Normalize()
	if normalized.MaxRPM < 0 || normalized.MaxTPM < 0 {
		return RatesAlertsSettings{}, errors.New("max_rpm and max_tpm must be >= 0")
	}
	if err := s.SaveAppSettingJSON(ctx, RatesAlertsSettingKey, normalized); err != nil {
		return RatesAlertsSettings{}, err
	}
	return normalized, nil
}

// RatesAlertScope names one evaluated dimension.
type RatesAlertScope string

const (
	RatesAlertScopeGlobal   RatesAlertScope = "global"
	RatesAlertScopeUpstream RatesAlertScope = "upstream"
	RatesAlertScopeModel    RatesAlertScope = "model"
)

// RatesBreach is one threshold crossing at evaluation time.
type RatesBreach struct {
	Scope   RatesAlertScope `json:"scope"`
	Key     string          `json:"key,omitempty"`
	RPM     int             `json:"current_rpm"`
	TPM     int             `json:"current_tpm"`
	MaxRPM  int             `json:"max_rpm"`
	MaxTPM  int             `json:"max_tpm"`
	Sampled time.Time       `json:"sampled_at"`
}

// Fingerprint is the stable system-event identity for one breach scope.
func (b RatesBreach) Fingerprint() string {
	if b.Key == "" {
		return "rates:" + string(b.Scope)
	}
	return "rates:" + string(b.Scope) + ":" + b.Key
}

// EvaluateRatesAlerts compares the trailing-60s rates against the thresholds
// for the global, per-upstream and per-model dimensions and returns the
// crossings plus the fingerprints of every scope that had traffic. It is
// read-only: persistence into system_events is the caller's job, so tests can
// assert the detection without writing event rows. The global scope uses the
// client-visible rows (the same rows the Overview rates count); upstream and
// model scopes mirror their usage aggregates' own where clauses.
func (s *Store) EvaluateRatesAlerts(settings RatesAlertsSettings) ([]RatesBreach, []string, error) {
	normalized := settings.Normalize()
	if !normalized.Enabled {
		return nil, nil, nil
	}
	var breaches []RatesBreach
	globalRPM, globalTPM, err := s.currentRatesAll(clientVisibleLogClause(""), nil)
	if err != nil {
		return nil, nil, err
	}
	evaluated := []string{"rates:" + string(RatesAlertScopeGlobal)}
	if breach := ratesBreachFor(RatesAlertScopeGlobal, "", globalRPM, globalTPM, normalized); breach != nil {
		breaches = append(breaches, *breach)
	}
	upstreams, err := s.currentRatesSuccessTokensByGroup("selected_upstream_id", "selected_upstream_id <> ''", nil)
	if err != nil {
		return nil, nil, err
	}
	for key, rates := range upstreams {
		evaluated = append(evaluated, "rates:"+string(RatesAlertScopeUpstream)+":"+key)
		if breach := ratesBreachFor(RatesAlertScopeUpstream, key, rates.rpm, rates.tpm, normalized); breach != nil {
			breaches = append(breaches, *breach)
		}
	}
	// Group by the lowercased model, matching listLogModels normalization, so
	// the fingerprint key is stable across case variants of the same model.
	models, err := s.currentRatesAllByGroup("LOWER(model)", `model <> '' AND LOWER(model) <> 'list_models'`, nil)
	if err != nil {
		return nil, nil, err
	}
	for key, rates := range models {
		evaluated = append(evaluated, "rates:"+string(RatesAlertScopeModel)+":"+key)
		if breach := ratesBreachFor(RatesAlertScopeModel, key, rates.rpm, rates.tpm, normalized); breach != nil {
			breaches = append(breaches, *breach)
		}
	}
	return breaches, evaluated, nil
}

func ratesBreachFor(scope RatesAlertScope, key string, rpm, tpm int, settings RatesAlertsSettings) *RatesBreach {
	overRPM := settings.MaxRPM > 0 && rpm > settings.MaxRPM
	overTPM := settings.MaxTPM > 0 && tpm > settings.MaxTPM
	if !overRPM && !overTPM {
		return nil
	}
	return &RatesBreach{
		Scope:   scope,
		Key:     key,
		RPM:     rpm,
		TPM:     tpm,
		MaxRPM:  settings.MaxRPM,
		MaxTPM:  settings.MaxTPM,
		Sampled: time.Now().UTC(),
	}
}

// RatesAlertEventDetails is the details_json attached to a rate breach event.
type RatesAlertEventDetails struct {
	Scope      string `json:"scope"`
	Key        string `json:"key,omitempty"`
	CurrentRPM int    `json:"current_rpm"`
	CurrentTPM int    `json:"current_tpm"`
	MaxRPM     int    `json:"max_rpm,omitempty"`
	MaxTPM     int    `json:"max_tpm,omitempty"`
	SampledAt  string `json:"sampled_at"`
}

// RatesAlertSystemEvent builds the notification row for one breach. The event
// is advisory only: it carries where and how far the rate ran past the
// threshold, never request bodies or tokens.
func RatesAlertSystemEvent(breach RatesBreach, severity string) (SystemEvent, error) {
	severity = strings.ToLower(strings.TrimSpace(severity))
	if severity == "" {
		severity = "warning"
	}
	details, err := json.Marshal(RatesAlertEventDetails{
		Scope:      string(breach.Scope),
		Key:        breach.Key,
		CurrentRPM: breach.RPM,
		CurrentTPM: breach.TPM,
		MaxRPM:     breach.MaxRPM,
		MaxTPM:     breach.MaxTPM,
		SampledAt:  breach.Sampled.UTC().Format(timeLayout),
	})
	if err != nil {
		return SystemEvent{}, err
	}
	scopeLabel := string(breach.Scope)
	if breach.Key != "" {
		scopeLabel += " " + breach.Key
	}
	event := SystemEvent{
		Fingerprint: breach.Fingerprint(),
		Source:      "rates",
		Category:    "rate_breach",
		Severity:    severity,
		Title:       "Rate threshold exceeded",
		Message:     fmt.Sprintf("%s: RPM %d/min, TPM %d/min exceed max_rpm=%d max_tpm=%d", scopeLabel, breach.RPM, breach.TPM, breach.MaxRPM, breach.MaxTPM),
		DetailsJSON: details,
	}
	switch breach.Scope {
	case RatesAlertScopeUpstream:
		event.UpstreamID = breach.Key
	case RatesAlertScopeModel:
		event.Model = breach.Key
	}
	return event, nil
}

// ListRatesAlertFingerprints returns the fingerprints of rate breach events
// in the given statuses. The alert evaluator uses it to find scopes that went
// silent (no traffic in the last minute) so their unread events can be
// resolved even though they were not evaluated this round.
func (s *Store) ListRatesAlertFingerprints(statuses ...string) ([]string, error) {
	whereSQL := `source = 'rates' AND category = 'rate_breach'`
	var args []any
	if len(statuses) > 0 {
		placeholders := make([]string, len(statuses))
		for i, status := range statuses {
			placeholders[i] = "?"
			args = append(args, status)
		}
		whereSQL += ` AND status IN (` + strings.Join(placeholders, ",") + `)`
	}
	rows, err := s.db.Query(`SELECT fingerprint FROM system_events WHERE `+whereSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fingerprint string
		if err := rows.Scan(&fingerprint); err != nil {
			return nil, err
		}
		if fingerprint != "" {
			out = append(out, fingerprint)
		}
	}
	return out, rows.Err()
}
