package monitor

import (
	"net/http/httptest"
	"testing"
	"time"
)

// The list endpoints take the Monitor's window as an opt-in parameter. It has to
// stay opt-in: `GET /api/traces` without it means every recorded request, which
// is what the API has always answered and what an external caller still gets,
// while the console always sends the parameter its range control holds.
func TestParseListFilterTakesTheWindowOnlyWhenItIsSent(t *testing.T) {
	unbounded := parseListFilter(httptest.NewRequest("GET", "/api/traces", nil))
	if !unbounded.Since.IsZero() {
		t.Errorf("parseListFilter(no window).Since = %v, want the zero time", unbounded.Since)
	}

	blank := parseListFilter(httptest.NewRequest("GET", "/api/traces?window=", nil))
	if !blank.Since.IsZero() {
		t.Errorf("parseListFilter(window=).Since = %v, want the zero time", blank.Since)
	}

	all := parseListFilter(httptest.NewRequest("GET", "/api/traces?window=all", nil))
	if !all.Since.IsZero() {
		t.Errorf("parseListFilter(window=all).Since = %v, want the zero time", all.Since)
	}

	now := time.Now().UTC()
	for _, tc := range []struct {
		window string
		want   time.Time
	}{
		{"today", startOfDisplayDay(now)},
		{"7d", now.Add(-7 * 24 * time.Hour)},
		{"30d", now.Add(-30 * 24 * time.Hour)},
	} {
		filter := parseListFilter(httptest.NewRequest("GET", "/api/sessions?window="+tc.window, nil))
		if filter.Since.IsZero() {
			t.Fatalf("parseListFilter(window=%s).Since is zero", tc.window)
		}
		if delta := filter.Since.Sub(tc.want); delta > time.Minute || delta < -time.Minute {
			t.Errorf("parseListFilter(window=%s).Since = %v, want about %v", tc.window, filter.Since, tc.want)
		}
	}
}
