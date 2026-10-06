package store

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestZZProbeBucketDrift(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	path := filepath.Join(dir, "drift.http")
	if err := os.WriteFile(path, []byte("# drift\n"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = "drift"
	header.Meta.Time = time.Date(2026, 7, 3, 9, 5, 0, 0, time.UTC)
	header.Meta.Model = "gpt-drift"
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

	report := func(stage string) {
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
		t.Logf("%-24s logs=%d members=%d buckets=%d", stage, logTokens, memberTokens, bucketTokens)
	}
	report("after insert")

	entry, err := st.GetByRequestID("drift")
	if err != nil {
		t.Fatalf("GetByRequestID error = %v", err)
	}
	if err := st.UpdateLogUsage(entry.ID, recordfile.UsageInfo{TotalTokens: 42}); err != nil {
		t.Fatalf("UpdateLogUsage error = %v", err)
	}
	st.flushDerivedRefresh()
	report("after UpdateLogUsage")

}
