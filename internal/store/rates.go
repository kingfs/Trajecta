package store

import "time"

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
