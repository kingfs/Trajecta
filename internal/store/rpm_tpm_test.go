package store

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// seedRateLog writes one entry row with the given timestamp and token count.
func seedRateLog(t *testing.T, st *Store, dir string, name string, at time.Time, tokens int, model string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("# rate\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = name
	header.Meta.Time = at
	header.Meta.URL = "https://api.openai.com/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = http.StatusOK
	header.Meta.Model = model
	header.Meta.ExchangeKind = "entry"
	header.Meta.DurationMs = 100
	header.Usage.TotalTokens = tokens
	if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{}); err != nil {
		t.Fatalf("UpsertLogWithGrouping(%q) error = %v", name, err)
	}
}

// Current rates count only the trailing 60s; the window totals count all rows
// since the window start; average divides totals by the elapsed span; the peak
// comes from the per-minute buckets of the timeline scan.
func TestOverviewRates(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	// Three rows 10s ago, all inside the trailing 60s; one row 5 minutes ago
	// is inside the 10-minute window but outside the current window. Its 25
	// tokens keep the busiest-minute peak on the current minute.
	seedRateLog(t, st, dir, "rate-current-1.http", now.Add(-10*time.Second), 10, "gpt-rate")
	seedRateLog(t, st, dir, "rate-current-2.http", now.Add(-10*time.Second), 20, "gpt-rate")
	seedRateLog(t, st, dir, "rate-current-3.http", now.Add(-10*time.Second), 30, "gpt-rate")
	seedRateLog(t, st, dir, "rate-old.http", now.Add(-5*time.Minute), 25, "gpt-rate")

	dashboard, err := st.Overview(OverviewOptions{
		Since:       now.Add(-10 * time.Minute),
		Limit:       5,
		BucketSize:  time.Hour,
		BucketCount: 2,
		Sections:    OverviewSections{Traffic: true},
	})
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	summary := dashboard.Summary
	if summary.RequestCount != 4 || summary.TotalTokens != 85 {
		t.Fatalf("window totals = requests:%d tokens:%d, want 4/85", summary.RequestCount, summary.TotalTokens)
	}
	if summary.CurrentRPM != 3 || summary.CurrentTPM != 60 {
		t.Fatalf("current rates = %d/%d, want 3/60 (trailing 60s only)", summary.CurrentRPM, summary.CurrentTPM)
	}
	if summary.PeakRPM != 3 || summary.PeakTPM != 60 {
		t.Fatalf("peak rates = %d/%d, want 3/60 (busiest minute)", summary.PeakRPM, summary.PeakTPM)
	}
	if summary.AvgRPM <= 0 || summary.AvgTPM <= 0 {
		t.Fatalf("avg rates = %v/%v, want positive for a 10-minute window", summary.AvgRPM, summary.AvgTPM)
	}
	// Average divides by the span from Since to the latest row (~10m, not the
	// 5-minute gap to the newest row).
	if math.Abs(summary.AvgRPM-0.4) > 0.05 {
		t.Fatalf("avg rpm = %v, want ~0.4 (4 requests over ~10 minutes)", summary.AvgRPM)
	}
	// Timeline bucket RPM/TPM normalize by the 60-minute bucket span.
	var found bool
	for _, item := range dashboard.Timeline {
		if item.RequestCount == 4 {
			found = true
			if math.Abs(item.RPM-4.0/60.0) > 1e-9 || math.Abs(item.TPM-85.0/60.0) > 1e-9 {
				t.Fatalf("timeline bucket rpm/tpm = %v/%v, want 4/60 and 85/60", item.RPM, item.TPM)
			}
		}
	}
	if !found {
		t.Fatalf("timeline has no bucket with 4 requests: %+v", dashboard.Timeline)
	}

	stats, err := st.Stats(ListFilter{Since: now.Add(-10 * time.Minute)})
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.CurrentRPM != 3 || stats.CurrentTPM != 60 {
		t.Fatalf("stats current rates = %d/%d, want 3/60", stats.CurrentRPM, stats.CurrentTPM)
	}
	if stats.AvgRPM <= 0 || stats.AvgTPM <= 0 {
		t.Fatalf("stats avg rates = %v/%v, want positive", stats.AvgRPM, stats.AvgTPM)
	}

	// Unwindowed queries have no finite span: averages stay zero.
	all, err := st.Overview(OverviewOptions{
		Limit:       5,
		BucketSize:  time.Hour,
		BucketCount: 2,
		Sections:    OverviewSections{Traffic: true},
	})
	if err != nil {
		t.Fatalf("Overview(all) error = %v", err)
	}
	if all.Summary.AvgRPM != 0 || all.Summary.AvgTPM != 0 {
		t.Fatalf("unwindowed avg rates = %v/%v, want 0/0", all.Summary.AvgRPM, all.Summary.AvgTPM)
	}
	if all.Summary.CurrentRPM != 3 {
		t.Fatalf("unwindowed current rpm = %d, want 3", all.Summary.CurrentRPM)
	}
}

// Rate helpers are pure arithmetic; pin the edge semantics they share.
func TestRateWindowMinutes(t *testing.T) {
	from := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	if got := rateWindowMinutes(from, to); got != 30 {
		t.Fatalf("rateWindowMinutes = %v, want 30", got)
	}
	if got := rateWindowMinutes(time.Time{}, to); got != 0 {
		t.Fatalf("rateWindowMinutes(zero from) = %v, want 0", got)
	}
	if got := rateWindowMinutes(from, from); got != 0 {
		t.Fatalf("rateWindowMinutes(empty span) = %v, want 0", got)
	}
	if avgRPM, avgTPM := rateAvg(0, 0, 0); avgRPM != 0 || avgTPM != 0 {
		t.Fatalf("rateAvg(zero span) = %v/%v, want 0/0", avgRPM, avgTPM)
	}
	if avgRPM, avgTPM := rateAvg(60, 3000, 30); avgRPM != 2 || avgTPM != 100 {
		t.Fatalf("rateAvg(60,3000,30m) = %v/%v, want 2/100", avgRPM, avgTPM)
	}
}

// Usage summaries annotate their window aggregates with the same trailing-60s
// rates, and trends normalize each bucket by its span.
func TestUsageRates(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		seedRateLog(t, st, dir, fmt.Sprintf("usage-rate-%d.http", i), now.Add(-10*time.Second), 50, "gpt-usage")
	}

	since := now.Add(-10 * time.Minute)
	record, err := st.usageSummary("model = ?", []any{"gpt-usage"}, since)
	if err != nil {
		t.Fatalf("usageSummary() error = %v", err)
	}
	if record.CurrentRPM != 2 || record.CurrentTPM != 100 {
		t.Fatalf("usage current rates = %d/%d, want 2/100", record.CurrentRPM, record.CurrentTPM)
	}
	if record.AvgRPM <= 0 {
		t.Fatalf("usage avg rpm = %v, want positive", record.AvgRPM)
	}

	grouped, err := st.usageSummariesByModel(since)
	if err != nil {
		t.Fatalf("usageSummariesByModel() error = %v", err)
	}
	if grouped["gpt-usage"].CurrentRPM != 2 {
		t.Fatalf("grouped current rpm = %d, want 2", grouped["gpt-usage"].CurrentRPM)
	}

	trends, err := st.usageTrends("model = ?", []any{"gpt-usage"}, since, time.Hour, 2, time.UTC)
	if err != nil {
		t.Fatalf("usageTrends() error = %v", err)
	}
	for _, trend := range trends {
		if trend.RequestCount == 2 {
			if math.Abs(trend.RPM-2.0/60.0) > 1e-9 || math.Abs(trend.TPM-100.0/60.0) > 1e-9 {
				t.Fatalf("trend rpm/tpm = %v/%v, want 2/60 and 100/60", trend.RPM, trend.TPM)
			}
		}
	}
}
