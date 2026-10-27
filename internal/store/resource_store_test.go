package store

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// resourceTestTrace indexes one minimal cassette so SaveTraceResources can read
// the trace's own recorded_at and the log filters have a row to page over.
func resourceTestTrace(t *testing.T, st *Store, requestID string, at time.Time) LogEntry {
	t.Helper()
	path := filepath.Join(t.TempDir(), requestID+".http")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("write cassette: %v", err)
	}
	if err := st.UpsertLog(path, recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  requestID,
			Time:       at.UTC(),
			Model:      "gpt-5",
			Provider:   "openai_compatible",
			Endpoint:   "/v1/chat/completions",
			URL:        "/v1/chat/completions",
			Method:     http.MethodPost,
			StatusCode: http.StatusOK,
		},
	}); err != nil {
		t.Fatalf("upsert log: %v", err)
	}
	entry, err := st.GetByRequestID(requestID)
	if err != nil {
		t.Fatalf("get trace: %v", err)
	}
	return entry
}

func TestResourceIndexDedupesAndIsIdempotent(t *testing.T) {
	st := configTransactionTestStore(t)
	first := resourceTestTrace(t, st, "resources-first", time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
	second := resourceTestTrace(t, st, "resources-second", time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC))

	const url = "https://api.example.com/repos/1?page=2"
	const path = "/home/alice/project/main.go"
	if err := st.SaveTraceResources(first.ID, []ResourceAccess{
		{Kind: "url", Value: url, Count: 2, Position: 0},
		{Kind: "local_path", Value: path, Count: 1, Position: 1},
		{Kind: "url", Value: url, Count: 9, Position: 2},
	}); err != nil {
		t.Fatalf("save resources: %v", err)
	}

	items, err := st.ListTraceResources(first.ID)
	if err != nil {
		t.Fatalf("list trace resources: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("resources = %d, want 2: %+v", len(items), items)
	}
	if items[0].Value != url || items[0].AccessCount != 2 {
		t.Errorf("first resource = %+v", items[0])
	}
	if count, err := st.CountTraceResources(first.ID); err != nil || count != 2 {
		t.Fatalf("CountTraceResources = %d, %v; want 2", count, err)
	}

	// A reparse that no longer sees the path replaces the relation instead of
	// adding to it, and repeating the same save is a no-op.
	for i := 0; i < 2; i++ {
		if err := st.SaveTraceResources(first.ID, []ResourceAccess{
			{Kind: "url", Value: url, Count: 1, Position: 0},
		}); err != nil {
			t.Fatalf("re-save resources: %v", err)
		}
	}
	items, err = st.ListTraceResources(first.ID)
	if err != nil {
		t.Fatalf("list trace resources: %v", err)
	}
	if len(items) != 1 || items[0].Value != url {
		t.Fatalf("after reparse resources = %+v, want just the URL", items)
	}
	// The path lost its last reference, so it is no longer part of the visible
	// catalogue even though its identity row is kept.
	if orphan, err := st.ListResources(ResourceFilter{Query: path}); err != nil || orphan.Total != 0 {
		t.Fatalf("orphan catalogue lookup = %+v, %v; want none", orphan, err)
	}

	// A second trace sharing the URL joins the same catalogue row.
	if err := st.SaveTraceResources(second.ID, []ResourceAccess{
		{Kind: "url", Value: url, Count: 3, Position: 0},
	}); err != nil {
		t.Fatalf("save second trace resources: %v", err)
	}
	page, err := st.ListResources(ResourceFilter{Query: "api.example.com"})
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("catalogue = %+v, want one shared URL", page)
	}
	shared := page.Items[0]
	if shared.TraceCount != 2 || shared.AccessCount != 4 {
		t.Errorf("shared aggregate = traces %d, nodes %d; want 2 and 4", shared.TraceCount, shared.AccessCount)
	}
	if shared.LastTraceID != second.ID {
		t.Errorf("last trace = %q, want %q", shared.LastTraceID, second.ID)
	}
	if !shared.LastSeenAt.Equal(second.Header.Meta.Time) {
		t.Errorf("last seen = %v, want the newer trace time %v", shared.LastSeenAt, second.Header.Meta.Time)
	}
}

func TestResourceCatalogFiltersByKindAndValue(t *testing.T) {
	st := configTransactionTestStore(t)
	trace := resourceTestTrace(t, st, "resources-filters", time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC))
	if err := st.SaveTraceResources(trace.ID, []ResourceAccess{
		{Kind: "url", Value: "https://docs.example.com/guide", Count: 1, Position: 0},
		{Kind: "local_path", Value: "/etc/hosts", Count: 1, Position: 1},
	}); err != nil {
		t.Fatalf("save resources: %v", err)
	}

	byKind, err := st.ListResources(ResourceFilter{Kind: "local_path"})
	if err != nil {
		t.Fatalf("list by kind: %v", err)
	}
	if byKind.Total != 1 || byKind.Items[0].Value != "/etc/hosts" {
		t.Fatalf("kind filter = %+v", byKind)
	}

	byQuery, err := st.ListResources(ResourceFilter{Query: "DOCS.EXAMPLE"})
	if err != nil {
		t.Fatalf("list by query: %v", err)
	}
	if byQuery.Total != 1 || byQuery.Items[0].Value != "https://docs.example.com/guide" {
		t.Fatalf("query filter = %+v", byQuery)
	}

	since, err := st.ListResources(ResourceFilter{Since: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if since.Total != 0 {
		t.Fatalf("future window returned %+v, want none", since.Items)
	}
}

func TestResourceBackfillQueuesOnlyUnindexedTraces(t *testing.T) {
	st := configTransactionTestStore(t)
	indexed := resourceTestTrace(t, st, "backfill-indexed", time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC))
	missing := resourceTestTrace(t, st, "backfill-missing", time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC))
	if err := st.SaveTraceResources(indexed.ID, []ResourceAccess{
		{Kind: "local_path", Value: "/etc/hosts", Count: 1, Position: 0},
	}); err != nil {
		t.Fatalf("save resources: %v", err)
	}

	queued, err := st.EnqueueResourceBackfill(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want 1 (only the unindexed trace)", queued)
	}
	jobs, err := st.ListParseJobs("queued", 10)
	if err != nil {
		t.Fatalf("list parse jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].TraceID != missing.ID {
		t.Fatalf("queued jobs = %+v, want the unindexed trace", jobs)
	}
}
