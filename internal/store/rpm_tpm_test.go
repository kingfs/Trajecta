package store

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
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
	// Timeline bucket RPM/TPM normalize by the 60-minute bucket span. Assert the
	// arithmetic and the totals rather than "one bucket holds all four rows":
	// the rows are seeded relative to the wall-clock now, so the 5-minute-old row
	// falls in the previous hour bucket whenever the test runs in the first five
	// minutes after the hour.
	var bucketRequests, bucketTokens int
	for _, item := range dashboard.Timeline {
		bucketRequests += item.RequestCount
		bucketTokens += item.TotalTokens
		if item.RequestCount == 0 && item.TotalTokens == 0 {
			if item.RPM != 0 || item.TPM != 0 {
				t.Fatalf("empty timeline bucket rates = %v/%v, want 0/0", item.RPM, item.TPM)
			}
			continue
		}
		if math.Abs(item.RPM-float64(item.RequestCount)/60.0) > 1e-9 || math.Abs(item.TPM-float64(item.TotalTokens)/60.0) > 1e-9 {
			t.Fatalf("timeline bucket (requests=%d tokens=%d) rpm/tpm = %v/%v, want %v/%v",
				item.RequestCount, item.TotalTokens, item.RPM, item.TPM,
				float64(item.RequestCount)/60.0, float64(item.TotalTokens)/60.0)
		}
	}
	if bucketRequests != 4 || bucketTokens != 85 {
		t.Fatalf("timeline totals = requests:%d tokens:%d, want 4/85 across buckets: %+v", bucketRequests, bucketTokens, dashboard.Timeline)
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

// Stats average uses the latest row as the span end (same as Overview/Usage),
// not wall-clock: a window whose traffic ended hours ago still reports the
// rate the traffic actually sustained.
func TestStatsAvgUsesLatestRow(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	// One row 30 minutes ago, inside a 60-minute window but far from now.
	seedRateLog(t, st, dir, "stats-old.http", now.Add(-30*time.Minute), 60, "gpt-stats")
	stats, err := st.Stats(ListFilter{Since: now.Add(-60 * time.Minute)})
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.CurrentRPM != 0 {
		t.Fatalf("current rpm = %d, want 0 (outside trailing 60s)", stats.CurrentRPM)
	}
	// Span is since (-60m) to latest row (-30m) = 30 minutes: 1/30 and 60/30.
	if math.Abs(stats.AvgRPM-1.0/30.0) > 0.01 || math.Abs(stats.AvgTPM-2.0) > 0.1 {
		t.Fatalf("stats avg = %v/%v, want ~0.033/2 (data-derived span)", stats.AvgRPM, stats.AvgTPM)
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

// Model identity is case-insensitive throughout the index, so one model recorded
// under two spellings must produce one catalog card. The grouped usage summary
// used to aggregate the raw column while the catalog looked its summary up by the
// lowercased id, so a model whose rows were spelled "GPT-5" showed zero traffic -
// and, since this change, zero RPM/TPM - on its card.
func TestModelCatalogRatesTreatModelCasingAsOneModel(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	seedRateLog(t, st, dir, "casing-lower.http", now.Add(-20*time.Second), 30, "gpt-casing")
	seedRateLog(t, st, dir, "casing-upper.http", now.Add(-10*time.Second), 70, "GPT-Casing")

	since := now.Add(-10 * time.Minute)
	grouped, err := st.usageSummariesByModel(since)
	if err != nil {
		t.Fatalf("usageSummariesByModel() error = %v", err)
	}
	if _, ok := grouped["GPT-Casing"]; ok {
		t.Fatalf("grouped summaries = %v, want the model keyed by its lowercase id only", grouped)
	}
	record := grouped["gpt-casing"]
	if record.RequestCount != 2 || record.TotalTokens != 100 {
		t.Fatalf("grouped gpt-casing = requests:%d tokens:%d, want 2/100", record.RequestCount, record.TotalTokens)
	}
	if record.CurrentRPM != 2 || record.CurrentTPM != 100 {
		t.Fatalf("grouped gpt-casing rates = %d/%d, want 2/100", record.CurrentRPM, record.CurrentTPM)
	}

	catalog, err := st.ListModelCatalogAnalytics(since, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ListModelCatalogAnalytics() error = %v", err)
	}
	var found bool
	for _, item := range catalog {
		if item.Model != "gpt-casing" {
			continue
		}
		found = true
		if item.Summary.RequestCount != 2 || item.Summary.TotalTokens != 100 {
			t.Fatalf("catalog gpt-casing summary = requests:%d tokens:%d, want 2/100", item.Summary.RequestCount, item.Summary.TotalTokens)
		}
		if item.Summary.CurrentRPM != 2 || item.Summary.CurrentTPM != 100 {
			t.Fatalf("catalog gpt-casing rates = %d/%d, want 2/100", item.Summary.CurrentRPM, item.Summary.CurrentTPM)
		}
	}
	if !found {
		t.Fatalf("catalog = %+v, want gpt-casing", catalog)
	}
}

// An empty window must stay the zero summary: no rates, no sample instant, and no
// division by a span the aggregate never saw (the average span end now comes from
// the aggregate's own MAX(recorded_at)).
func TestStatsAvgEmptyWindow(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	stats, err := st.Stats(ListFilter{Since: time.Now().UTC().Add(-time.Hour)})
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats != (Stats{}) {
		t.Fatalf("Stats(empty window) = %+v, want the zero summary", stats)
	}
}

// TestPostgresRateQueries runs the same rate paths against Postgres when a
// disposable test database is configured, because the trailing-60s comparison
// binds a formatted string against a timestamptz column there (an instant) while
// it is a text comparison on SQLite. Both must agree.
func TestPostgresRateQueries(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	if err := appdbmigrate.MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}
	dir := t.TempDir()
	st, err := NewWithDatabaseOptions(dir, "postgres", dsn, 4, 4, DatabaseOptions{AutoMigrate: false})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("Close(postgres store) error = %v", err)
		}
	})

	// A unique model keeps the assertions independent of the rows other Postgres
	// tests in this shared database may have written.
	model := "rate-pg-" + strings.ReplaceAll(t.Name(), "/", "_") + "-" + time.Now().UTC().Format("20060102150405.000000000")
	now := time.Now().UTC()
	seedRateLog(t, st, dir, "pg-current.http", now.Add(-10*time.Second), 40, model)
	seedRateLog(t, st, dir, "pg-old.http", now.Add(-10*time.Minute), 7, model)
	since := now.Add(-20 * time.Minute)

	stats, err := st.Stats(ListFilter{Model: model, Since: since})
	if err != nil {
		t.Fatalf("Stats(postgres) error = %v", err)
	}
	if stats.CurrentRPM != 1 || stats.CurrentTPM != 40 {
		t.Fatalf("postgres stats rates = %d/%d, want 1/40 (trailing 60s only)", stats.CurrentRPM, stats.CurrentTPM)
	}
	if stats.AvgRPM <= 0 || stats.AvgTPM <= 0 {
		t.Fatalf("postgres stats avg rates = %v/%v, want positive for a 20-minute window", stats.AvgRPM, stats.AvgTPM)
	}

	grouped, err := st.usageSummariesByModel(since)
	if err != nil {
		t.Fatalf("usageSummariesByModel(postgres) error = %v", err)
	}
	if got := grouped[model]; got.RequestCount != 2 || got.CurrentRPM != 1 || got.CurrentTPM != 40 {
		t.Fatalf("postgres grouped summary = %+v, want requests 2 and rates 1/40", got)
	}

	breaches, _, err := st.EvaluateRatesAlerts(RatesAlertsSettings{Enabled: true, MaxRPM: 1, MaxTPM: 1000000, Severity: "warning"})
	if err != nil {
		t.Fatalf("EvaluateRatesAlerts(postgres) error = %v", err)
	}
	var modelBreach *RatesBreach
	for index := range breaches {
		if breaches[index].Scope == RatesAlertScopeModel && breaches[index].Key == model {
			modelBreach = &breaches[index]
		}
	}
	if modelBreach == nil {
		t.Fatalf("postgres model breaches = %+v, want one for %q", breaches, model)
	}
	event, err := RatesAlertSystemEvent(*modelBreach, "warning")
	if err != nil {
		t.Fatalf("RatesAlertSystemEvent(postgres) error = %v", err)
	}
	if _, err := st.UpsertSystemEvent(event); err != nil {
		t.Fatalf("UpsertSystemEvent(postgres) error = %v", err)
	}
	loaded, err := st.GetSystemEventByFingerprint(event.Fingerprint)
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint(postgres) error = %v", err)
	}
	if loaded.Category != "rate_breach" || loaded.Source != "rates" {
		t.Fatalf("postgres event = %+v, want rates/rate_breach", loaded)
	}
}
