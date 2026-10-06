package store

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestOverviewMetricBucketsFollowUsageUpdates pins that the derived overview aggregate follows a
// usage update, not just the initial write.
//
// Recording inserts the hourly bucket contribution from the cassette header; `UpdateLogUsage` later
// replaces prompt/completion/total tokens on the log row (usage is repaired or re-parsed after the
// fact). The contribution has to be refreshed for that path, otherwise overview_metric_buckets drifts
// from the logs it summarizes and only `db summary rebuild overview` can repair it. The date is fixed
// and the hour boundary is asserted too, so a timezone or truncation change shows up here rather than
// as an off-by-one-hour bucket in production.
func TestOverviewMetricBucketsFollowUsageUpdates(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	path := filepath.Join(dir, "usage-update.http")
	if err := os.WriteFile(path, []byte("# usage update\n"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = "usage-update"
	header.Meta.Time = time.Date(2026, 7, 3, 9, 5, 0, 0, time.UTC)
	header.Meta.Model = "gpt-usage"
	header.Meta.Provider = "openai_compatible"
	header.Meta.Endpoint = "/v1/chat/completions"
	header.Meta.URL = "https://api.openai.com/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = http.StatusOK
	header.Meta.ExchangeKind = "entry"
	header.Usage.TotalTokens = 10
	if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{}); err != nil {
		t.Fatalf("UpsertLogWithGrouping error = %v", err)
	}
	st.flushDerivedRefresh()

	assertBuckets := func(stage string, want int) {
		t.Helper()
		var logTokens, memberTokens, bucketTokens int
		if err := st.db.QueryRow(`SELECT total_tokens FROM logs WHERE path = ?`, path).Scan(&logTokens); err != nil {
			t.Fatalf("%s: logs query error = %v", stage, err)
		}
		if err := st.db.QueryRow(`SELECT COALESCE(SUM(total_tokens), -1) FROM overview_metric_bucket_members WHERE path = ?`, path).Scan(&memberTokens); err != nil {
			t.Fatalf("%s: members query error = %v", stage, err)
		}
		if err := st.db.QueryRow(`SELECT COALESCE(SUM(total_tokens), -1) FROM overview_metric_buckets`).Scan(&bucketTokens); err != nil {
			t.Fatalf("%s: buckets query error = %v", stage, err)
		}
		if logTokens != want {
			t.Fatalf("%s: logs.total_tokens = %d, want %d", stage, logTokens, want)
		}
		if memberTokens != want {
			t.Fatalf("%s: overview_metric_bucket_members total = %d, want %d: the member contribution has to follow the log row", stage, memberTokens, want)
		}
		if bucketTokens != want {
			t.Fatalf("%s: overview_metric_buckets total = %d, want %d: the aggregate has to follow the log row", stage, bucketTokens, want)
		}
		var bucketStart string
		if err := st.db.QueryRow(`SELECT bucket_start FROM overview_metric_bucket_members WHERE path = ?`, path).Scan(&bucketStart); err != nil {
			t.Fatalf("%s: bucket_start query error = %v", stage, err)
		}
		if wantStart := header.Meta.Time.UTC().Truncate(time.Hour).Format(timeLayout); bucketStart != wantStart {
			t.Fatalf("%s: member bucket_start = %q, want %q (the recorded time truncated to its UTC hour)", stage, bucketStart, wantStart)
		}
	}
	assertBuckets("after insert", 10)

	entry, err := st.GetByRequestID("usage-update")
	if err != nil {
		t.Fatalf("GetByRequestID error = %v", err)
	}
	if err := st.UpdateLogUsage(entry.ID, recordfile.UsageInfo{TotalTokens: 42}); err != nil {
		t.Fatalf("UpdateLogUsage error = %v", err)
	}
	st.flushDerivedRefresh()
	assertBuckets("after UpdateLogUsage", 42)
}
