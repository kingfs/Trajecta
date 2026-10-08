package monitor

import (
	"testing"
	"time"
)

// TestStartOfDisplayDayUsesTheConfiguredTimezone pins the fix for the window
// boundary bug: every day-based window used to be computed at UTC midnight,
// which for the default Asia/Shanghai deployment is 08:00 local, so the first
// eight hours of the operator's day were reported under yesterday.
func TestStartOfDisplayDayUsesTheConfiguredTimezone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("Asia/Shanghai is unavailable: %v", err)
	}
	SetDisplayLocation(shanghai)
	t.Cleanup(func() { SetDisplayLocation(time.UTC) })

	// 09:00 local is 01:00 UTC, so the UTC day and the local day disagree: the
	// UTC boundary sits at 08:00 local.
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, shanghai)
	got := startOfDisplayDay(now)
	want := time.Date(2026, 10, 8, 0, 0, 0, 0, shanghai)
	if !got.Equal(want) {
		t.Fatalf("startOfDisplayDay() = %s, want %s", got.In(shanghai), want.In(shanghai))
	}
	if hour := got.In(shanghai).Hour(); hour != 0 {
		t.Fatalf("startOfDisplayDay() = %s, want local midnight", got.In(shanghai))
	}

	// The last instant of the previous local day still belongs to that day.
	previous := startOfDisplayDay(now.Add(-9*time.Hour - time.Nanosecond))
	if wantPrevious := time.Date(2026, 10, 7, 0, 0, 0, 0, shanghai); !previous.Equal(wantPrevious) {
		t.Fatalf("startOfDisplayDay(23:59:59.999 local) = %s, want %s", previous.In(shanghai), wantPrevious.In(shanghai))
	}
}

// TestDisplayDayWindowsAreLocalMidnight checks the parsing entry points rather
// than only the helper, because the bug was in which of the two they used.
func TestDisplayDayWindowsAreLocalMidnight(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("Asia/Shanghai is unavailable: %v", err)
	}
	SetDisplayLocation(shanghai)
	t.Cleanup(func() { SetDisplayLocation(time.UTC) })

	_, since, bucketSize, bucketCount := parseOverviewWindow("today")
	local := since.In(shanghai)
	if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 {
		t.Fatalf("parseOverviewWindow(today) since = %s, want local midnight", local)
	}
	if bucketSize != time.Hour || bucketCount != 24 {
		t.Fatalf("parseOverviewWindow(today) = %s x %d, want 1h x 24", bucketSize, bucketCount)
	}

	_, upstreamSince := parseUpstreamWindow("today")
	if got := upstreamSince.In(shanghai); got.Hour() != 0 {
		t.Fatalf("parseUpstreamWindow(today) since = %s, want local midnight", got)
	}

	_, analyticsSince := parseAnalyticsWindow("today")
	if got := analyticsSince.In(shanghai); got.Hour() != 0 {
		t.Fatalf("parseAnalyticsWindow(today) since = %s, want local midnight", got)
	}

	// "all" stays the zero time, which every query treats as "no lower bound".
	if _, all := parseUpstreamWindow("all"); !all.IsZero() {
		t.Fatalf("parseUpstreamWindow(all) since = %s, want the zero time", all)
	}
}

// TestSetDisplayLocationIgnoresNil keeps a caller that has no configuration from
// silently reverting the whole process to UTC.
func TestSetDisplayLocationIgnoresNil(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("Asia/Shanghai is unavailable: %v", err)
	}
	SetDisplayLocation(shanghai)
	t.Cleanup(func() { SetDisplayLocation(time.UTC) })

	SetDisplayLocation(nil)
	if got := DisplayLocation(); got != shanghai {
		t.Fatalf("DisplayLocation() = %v after a nil set, want %v", got, shanghai)
	}
}
