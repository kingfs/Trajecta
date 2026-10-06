package store

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestOverviewMetricBucketsAgreeWithTheirMembersAndARebuild is an invariant gate
// over the derived aggregate the dashboard reads.
//
// overview_metric_buckets is a materialised aggregate of
// overview_metric_bucket_members, and two independent code paths write it:
// recording maintains both incrementally, while
// RebuildOverviewMetricBuckets() recomputes them from the recorded logs. The
// invariant is that the incremental result and the rebuilt result agree exactly,
// and that each bucket row equals the sum of its member rows. A recording path
// that updates one table without the other leaves the dashboard totals wrong
// until an operator notices, and until this test nothing compared the two paths
// against each other.
func TestOverviewMetricBucketsAgreeWithTheirMembersAndARebuild(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	base := time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC)
	recorded := []struct {
		name    string
		at      time.Time
		status  int
		errText string
		tokens  int
		kind    string
	}{
		{name: "first", at: base.Add(1 * time.Minute), status: http.StatusOK, tokens: 11, kind: "entry"},
		{name: "failed", at: base.Add(30 * time.Minute), status: http.StatusTooManyRequests, errText: "rate limited", tokens: 3, kind: "entry"},
		// A child model exchange must not be counted as a request.
		{name: "child", at: base.Add(5 * time.Minute), status: http.StatusOK, tokens: 99, kind: "model"},
		// A second hour, so the aggregate has more than one bucket.
		{name: "next-hour", at: base.Add(2 * time.Hour), status: http.StatusOK, tokens: 7, kind: "entry"},
	}
	paths := map[string]string{}
	for _, tc := range recorded {
		recordPath := filepath.Join(dir, tc.name+".http")
		if err := os.WriteFile(recordPath, []byte("# "+tc.name+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", tc.name, err)
		}
		paths[tc.name] = recordPath
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = "req-overview-invariant-" + tc.name
		header.Meta.Time = tc.at
		header.Meta.URL = "https://api.openai.com/v1/chat/completions"
		header.Meta.Method = http.MethodPost
		header.Meta.StatusCode = tc.status
		header.Meta.Error = tc.errText
		header.Meta.Model = "gpt-invariant"
		header.Meta.ExchangeKind = tc.kind
		header.Meta.TTFTMs = 50
		header.Meta.DurationMs = 100
		header.Usage.TotalTokens = tc.tokens
		if err := st.UpsertLogWithGrouping(recordPath, header, GroupingInfo{}); err != nil {
			t.Fatalf("UpsertLogWithGrouping(%q) error = %v", tc.name, err)
		}
	}

	// The usage-update path rewrites the log's tokens after the fact, which is
	// where an incremental aggregate most easily drifts from a rebuild.
	var traceID string
	if err := st.db.QueryRow(`SELECT trace_id FROM logs WHERE path = ?`, paths["first"]).Scan(&traceID); err != nil {
		t.Fatalf("query trace_id error = %v", err)
	}
	if err := st.UpdateLogUsage(traceID, recordfile.UsageInfo{PromptTokens: 10, CompletionTokens: 15, TotalTokens: 25}); err != nil {
		t.Fatalf("UpdateLogUsage() error = %v", err)
	}
	st.flushDerivedRefresh()

	incremental := readAllOverviewMetricBuckets(t, st)
	if len(incremental) != 2 {
		t.Fatalf("incremental buckets = %d, want 2 (one per recorded hour)", len(incremental))
	}

	members := readOverviewMemberSums(t, st)
	if len(members) != len(incremental) {
		t.Fatalf("member buckets = %d, bucket rows = %d; every bucket needs its members", len(members), len(incremental))
	}
	for key, bucket := range incremental {
		if members[key] != bucket {
			t.Fatalf("bucket %s = %+v, but its members sum to %+v", key, bucket, members[key])
		}
	}

	// A member that points at a path with no log row is a phantom: it inflates the
	// aggregate forever, because nothing in the rebuild would recreate it.
	var orphans int
	if err := st.db.QueryRow(`
		SELECT COUNT(*) FROM overview_metric_bucket_members m
		LEFT JOIN logs l ON l.path = m.path
		WHERE l.path IS NULL
	`).Scan(&orphans); err != nil {
		t.Fatalf("orphan member query error = %v", err)
	}
	if orphans != 0 {
		t.Fatalf("overview_metric_bucket_members has %d rows with no matching log", orphans)
	}

	// Dropping both derived tables and rebuilding from the logs must reproduce the
	// incrementally maintained aggregate exactly.
	if _, err := st.db.Exec(`DELETE FROM overview_metric_bucket_members`); err != nil {
		t.Fatalf("delete members error = %v", err)
	}
	if _, err := st.db.Exec(`DELETE FROM overview_metric_buckets`); err != nil {
		t.Fatalf("delete buckets error = %v", err)
	}
	if err := st.RebuildOverviewMetricBuckets(); err != nil {
		t.Fatalf("RebuildOverviewMetricBuckets() error = %v", err)
	}
	rebuilt := readAllOverviewMetricBuckets(t, st)
	if !reflect.DeepEqual(incremental, rebuilt) {
		t.Fatalf("the incremental aggregate and the rebuilt aggregate disagree:\n incremental = %+v\n rebuilt     = %+v", incremental, rebuilt)
	}
}

// readAllOverviewMetricBuckets returns every bucket row keyed by bucket start and
// size, so two whole aggregates can be compared without naming each column.
func readAllOverviewMetricBuckets(t *testing.T, st *Store) map[string]overviewMetricBucketForTest {
	t.Helper()
	rows, err := st.db.Query(`
		SELECT bucket_start, bucket_size_seconds, request_count, success_request, failed_request,
			total_tokens, ttft_sum, ttft_count, duration_sum, duration_count, stream_count
		FROM overview_metric_buckets
	`)
	if err != nil {
		t.Fatalf("query overview_metric_buckets error = %v", err)
	}
	defer rows.Close()

	out := map[string]overviewMetricBucketForTest{}
	for rows.Next() {
		var start string
		var size int
		var bucket overviewMetricBucketForTest
		if err := rows.Scan(&start, &size, &bucket.requestCount, &bucket.successRequest, &bucket.failedRequest,
			&bucket.totalTokens, &bucket.ttftSum, &bucket.ttftCount, &bucket.durationSum, &bucket.durationCount,
			&bucket.streamCount); err != nil {
			t.Fatalf("scan overview_metric_buckets error = %v", err)
		}
		out[fmt.Sprintf("%s|%d", start, size)] = bucket
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate overview_metric_buckets error = %v", err)
	}
	return out
}

// readOverviewMemberSums returns the per-bucket sum of the member rows, in the
// same shape as readAllOverviewMetricBuckets so the two can be compared.
func readOverviewMemberSums(t *testing.T, st *Store) map[string]overviewMetricBucketForTest {
	t.Helper()
	rows, err := st.db.Query(`
		SELECT bucket_start, bucket_size_seconds, SUM(request_count), SUM(success_request), SUM(failed_request),
			SUM(total_tokens), SUM(ttft_sum), SUM(ttft_count), SUM(duration_sum), SUM(duration_count), SUM(stream_count)
		FROM overview_metric_bucket_members
		GROUP BY bucket_start, bucket_size_seconds
	`)
	if err != nil {
		t.Fatalf("query overview_metric_bucket_members error = %v", err)
	}
	defer rows.Close()

	out := map[string]overviewMetricBucketForTest{}
	for rows.Next() {
		var start string
		var size int
		var bucket overviewMetricBucketForTest
		if err := rows.Scan(&start, &size, &bucket.requestCount, &bucket.successRequest, &bucket.failedRequest,
			&bucket.totalTokens, &bucket.ttftSum, &bucket.ttftCount, &bucket.durationSum, &bucket.durationCount,
			&bucket.streamCount); err != nil {
			t.Fatalf("scan overview_metric_bucket_members error = %v", err)
		}
		out[fmt.Sprintf("%s|%d", start, size)] = bucket
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate overview_metric_bucket_members error = %v", err)
	}
	return out
}

// TestCloseSettlesTheDeferredDerivedQueue pins the shutdown half of the derived refresh.
//
// The queues in storeShared are process-local, so whatever is still queued when the database
// handle goes away is gone: the buckets can then only be corrected by rebuilding them from
// the index, and nothing calls that rebuild in normal operation. serve flushes on its own
// shutdown path, but every other store caller - the analyze and db commands that repair usage
// or re-index a trace - only closes the store, so a repair committed the index row and left
// the overview aggregate showing the previous numbers forever.
func TestCloseSettlesTheDeferredDerivedQueue(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	at := time.Date(2026, 7, 5, 8, 15, 0, 0, time.UTC)
	recordPath := filepath.Join(dir, "close-settles.http")
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = "req-close-settles"
	header.Meta.Time = at
	header.Meta.URL = "https://api.openai.com/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = http.StatusOK
	header.Meta.Model = "gpt-close-settles"
	header.Meta.ExchangeKind = "entry"
	header.Usage.TotalTokens = 42
	if err := os.WriteFile(recordPath, closeSettlesPrelude(t, header), 0o644); err != nil {
		t.Fatalf("WriteFile(record) error = %v", err)
	}
	if err := st.UpsertLogWithGrouping(recordPath, header, GroupingInfo{}); err != nil {
		t.Fatalf("UpsertLogWithGrouping() error = %v", err)
	}
	// Deliberately no flush: the queue is what a close has to settle.
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// Close is idempotent, so a defer next to an explicit close is harmless.
	if err := st.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("New() after close error = %v", err)
	}
	defer reopened.Close()
	bucket := readOverviewMetricBucketForTest(t, reopened, at.Truncate(time.Hour))
	if bucket.requestCount != 1 || bucket.totalTokens != 42 {
		t.Fatalf("bucket after close = %+v, want the contribution the log row owed", bucket)
	}
}

func closeSettlesPrelude(t *testing.T, header recordfile.RecordHeader) []byte {
	t.Helper()
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	return prelude
}

// TestFailedDerivedRefreshStaysQueued pins the other half: a refresh that fails must keep its
// work, because the queue is the only record that the aggregate is owed an update. Dropping
// it left the buckets short until somebody rebuilt them from the index, which no command did.
func TestFailedDerivedRefreshStaysQueued(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	at := time.Date(2026, 7, 5, 9, 15, 0, 0, time.UTC)
	recordPath := filepath.Join(dir, "refresh-fails.http")
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = "req-refresh-fails"
	header.Meta.Time = at
	header.Meta.URL = "https://api.openai.com/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = http.StatusOK
	header.Meta.Model = "gpt-refresh-fails"
	header.Meta.ExchangeKind = "entry"
	header.Usage.TotalTokens = 17
	if err := os.WriteFile(recordPath, closeSettlesPrelude(t, header), 0o644); err != nil {
		t.Fatalf("WriteFile(record) error = %v", err)
	}
	if err := st.UpsertLogWithGrouping(recordPath, header, GroupingInfo{}); err != nil {
		t.Fatalf("UpsertLogWithGrouping() error = %v", err)
	}

	// Break the refresh, then let a flush try and fail.
	if _, err := st.db.Exec(`DROP TABLE overview_metric_bucket_members`); err != nil {
		t.Fatalf("DROP TABLE error = %v", err)
	}
	st.flushDerivedRefresh()
	if got := deferredDerivedQueueSizeForTest(st); got == 0 {
		t.Fatal("a failed refresh dropped its work; the aggregate keeps the old numbers forever")
	}

	// Repair the schema: the retry must now apply the work the queue kept.
	if err := st.ensureOverviewMetricBucketsSchema(); err != nil {
		t.Fatalf("ensureOverviewMetricBucketsSchema() error = %v", err)
	}
	st.flushDerivedRefresh()
	bucket := readOverviewMetricBucketForTest(t, st, at.Truncate(time.Hour))
	if bucket.requestCount != 1 || bucket.totalTokens != 17 {
		t.Fatalf("bucket after retry = %+v, want the queued contribution applied", bucket)
	}
}

func deferredDerivedQueueSizeForTest(st *Store) int {
	st.shared.derivedMu.Lock()
	defer st.shared.derivedMu.Unlock()
	return st.shared.derivedQueueSizeLocked()
}
