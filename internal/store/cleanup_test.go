package store

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

type cleanupLogSpec struct {
	requestID        string
	sessionID        string
	statusCode       int
	errorText        string
	exchangeKind     string
	model            string
	endpoint         string
	requestAuditID   string
	responseID       string
	exchangeID       string
	parentExchangeID string
	recordedAt       time.Time
}

// cleanupInsertLog indexes one cassette the way the recorder does, and returns
// the indexed entry so a test can use its derived trace id.
func cleanupInsertLog(t *testing.T, st *Store, spec cleanupLogSpec) LogEntry {
	t.Helper()
	if spec.requestID == "" {
		t.Fatal("cleanupInsertLog: requestID is required")
	}
	if spec.recordedAt.IsZero() {
		spec.recordedAt = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	}
	path := filepath.Join(st.outputDir, spec.requestID+".http")
	if err := os.WriteFile(path, []byte("cassette:"+spec.requestID), 0o644); err != nil {
		t.Fatalf("write cassette %s: %v", spec.requestID, err)
	}
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:        spec.requestID,
			RequestAuditID:   spec.requestAuditID,
			ResponseID:       spec.responseID,
			ExchangeID:       spec.exchangeID,
			ExchangeKind:     spec.exchangeKind,
			ParentExchangeID: spec.parentExchangeID,
			Time:             spec.recordedAt.UTC(),
			Model:            spec.model,
			Provider:         "openai_compatible",
			Endpoint:         spec.endpoint,
			URL:              "/v1/chat/completions",
			Method:           http.MethodPost,
			StatusCode:       spec.statusCode,
			Error:            spec.errorText,
		},
	}
	if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{SessionID: spec.sessionID, SessionSource: "test"}); err != nil {
		t.Fatalf("upsert log %s: %v", spec.requestID, err)
	}
	entry, err := st.GetByRequestID(spec.requestID)
	if err != nil {
		t.Fatalf("get log %s: %v", spec.requestID, err)
	}
	return entry
}

func cleanupCount(t *testing.T, st *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := st.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return count
}

func TestRequestCleanupPlanMatchesStatusAndExpandsChildren(t *testing.T) {
	st := configTransactionTestStore(t)
	parent404 := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "cleanup-parent-404", sessionID: "s-alpha", statusCode: 404,
		requestAuditID: "audit-1", responseID: "resp-1",
	})
	parent200 := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "cleanup-parent-200", sessionID: "s-alpha", statusCode: 200,
	})
	child500 := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "cleanup-child-500", sessionID: "s-alpha", statusCode: 500, exchangeKind: "model",
		requestAuditID: "audit-1", responseID: "resp-1", parentExchangeID: "entry:resp-1",
	})

	plan, err := st.PlanRequestCleanup(RequestCleanupFilter{StatusCodes: []int{404}})
	if err != nil {
		t.Fatalf("PlanRequestCleanup() error = %v", err)
	}
	if plan.MatchedRequests != 1 || plan.ChildExchanges != 1 || plan.TotalLogRows != 2 {
		t.Fatalf("plan counts = matched %d child %d total %d; want 1, 1, 2",
			plan.MatchedRequests, plan.ChildExchanges, plan.TotalLogRows)
	}
	if plan.AffectedSessions != 1 {
		t.Fatalf("affected sessions = %d, want 1", plan.AffectedSessions)
	}
	if len(plan.CassettePaths) != 2 || len(plan.TraceIDs) != 2 {
		t.Fatalf("cassette paths = %v, trace ids = %v; want the 404 parent and its child", plan.CassettePaths, plan.TraceIDs)
	}
	if len(plan.StatusCounts) != 2 || plan.StatusCounts[0].StatusCode != 404 || plan.StatusCounts[1].StatusCode != 500 {
		t.Fatalf("status counts = %+v, want 404 and 500", plan.StatusCounts)
	}
	if plan.CassetteCount != 2 || plan.CassetteBytes == 0 || plan.MissingCassettes != 0 {
		t.Fatalf("cassette stats = count %d bytes %d missing %d", plan.CassetteCount, plan.CassetteBytes, plan.MissingCassettes)
	}
	if len(plan.SampleRequests) != 1 || plan.SampleRequests[0].TraceID != parent404.ID {
		t.Fatalf("sample = %+v, want only the 404 parent", plan.SampleRequests)
	}

	result, err := st.ApplyRequestCleanup(RequestCleanupFilter{StatusCodes: []int{404}}, RequestCleanupApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyRequestCleanup() error = %v", err)
	}
	if result.DeletedLogs != 2 {
		t.Fatalf("deleted logs = %d, want 2", result.DeletedLogs)
	}
	remaining := cleanupCount(t, st, `SELECT COUNT(*) FROM logs`)
	if remaining != 1 {
		t.Fatalf("remaining logs = %d, want the 200 parent only", remaining)
	}
	if cleanupCount(t, st, `SELECT COUNT(*) FROM logs WHERE trace_id IN (?, ?)`, parent404.ID, child500.ID) != 0 {
		t.Fatal("the 404 parent or its child exchange survived the cleanup")
	}
	if cleanupCount(t, st, `SELECT COUNT(*) FROM logs WHERE trace_id = ?`, parent200.ID) != 1 {
		t.Fatal("the 200 parent was deleted")
	}
}

func TestRequestCleanupRebuildsSessionSummaries(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	cleanupInsertLog(t, st, cleanupLogSpec{requestID: "sess-keep-200", sessionID: "s-keep", statusCode: 200, recordedAt: base})
	cleanupInsertLog(t, st, cleanupLogSpec{requestID: "sess-keep-500", sessionID: "s-keep", statusCode: 500, recordedAt: base.Add(time.Minute)})
	cleanupInsertLog(t, st, cleanupLogSpec{requestID: "sess-drop-500", sessionID: "s-drop", statusCode: 500, recordedAt: base.Add(2 * time.Minute)})
	if err := st.RebuildSessionSummaries(); err != nil {
		t.Fatalf("RebuildSessionSummaries() error = %v", err)
	}

	result, err := st.ApplyRequestCleanup(RequestCleanupFilter{Failed: true}, RequestCleanupApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyRequestCleanup() error = %v", err)
	}
	if result.DeletedLogs != 2 {
		t.Fatalf("deleted logs = %d, want 2", result.DeletedLogs)
	}
	if result.RebuiltSessions != 1 || result.RemovedSessionSummaries != 1 {
		t.Fatalf("session rebuild = rebuilt %d removed %d; want 1 and 1", result.RebuiltSessions, result.RemovedSessionSummaries)
	}
	var requestCount, failedCount int
	if err := st.db.QueryRow(`SELECT request_count, failed_request FROM session_summaries WHERE session_id = ?`, "s-keep").Scan(&requestCount, &failedCount); err != nil {
		t.Fatalf("read kept session summary: %v", err)
	}
	if requestCount != 1 || failedCount != 0 {
		t.Fatalf("kept session summary = requests %d failed %d; want 1 and 0", requestCount, failedCount)
	}
	if cleanupCount(t, st, `SELECT COUNT(*) FROM session_summaries WHERE session_id = ?`, "s-drop") != 0 {
		t.Fatal("the session summary with no remaining requests survived")
	}
}

func TestRequestCleanupDeletesDerivedRowsAndPrunesResources(t *testing.T) {
	st := configTransactionTestStore(t)
	entry := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "cleanup-derived-404", sessionID: "s-derived", statusCode: 404, requestAuditID: "audit-9",
	})
	kept := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "cleanup-derived-200", sessionID: "s-derived", statusCode: 200})
	traceID := entry.ID
	now := time.Now().UTC()

	resource, err := st.db.Exec(
		`INSERT INTO resources (kind, value, value_hash, last_trace_id, first_seen_at, last_seen_at) VALUES ('url', 'https://x', 'hash-x', ?, ?, ?)`,
		traceID, now, now,
	)
	if err != nil {
		t.Fatalf("insert resource: %v", err)
	}
	resourceID, err := resource.LastInsertId()
	if err != nil {
		t.Fatalf("resource id: %v", err)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.db.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO trace_resources (trace_id, resource_id, access_count, first_position, created_at) VALUES (?, ?, 1, 0, ?)`, traceID, resourceID, now)
	mustExec(`INSERT INTO trace_observations (trace_id, parser, parser_version, status, created_at, updated_at) VALUES (?, 'p', 'v', 'ok', ?, ?)`, traceID, now, now)
	mustExec(`INSERT INTO trace_findings (trace_id, finding_id, category, severity, detector, detector_version, created_at) VALUES (?, 'f1', 'cat', 'low', 'd', 'v', ?)`, traceID, now)
	mustExec(`INSERT INTO credential_mask_audits (id, trace_id, event_index, detected_at, created_at) VALUES ('audit-9-0', ?, 0, ?, ?)`, traceID, now, now)
	mustExec(`INSERT INTO credential_mask_event_states (trace_id, rule, status, updated_at) VALUES (?, 'r', 'open', ?)`, traceID, now)
	mustExec(`INSERT INTO analysis_runs (trace_id, kind, analyzer, analyzer_version, status, created_at) VALUES (?, 'k', 'a', 'v', 'ok', ?)`, traceID, now)
	mustExec(`INSERT INTO parse_jobs (trace_id, status, created_at, updated_at) VALUES (?, 'queued', ?, ?)`, traceID, now, now)
	mustExec(`INSERT INTO analysis_jobs (job_type, target_type, target_id, status, created_at, updated_at) VALUES ('reanalysis', 'trace', ?, 'queued', ?, ?)`, traceID, now, now)
	mustExec(`INSERT INTO upstream_exchanges (id, trace_id, cassette_path, status_code, started_at) VALUES ('ux-1', ?, ?, 404, ?)`, traceID, entry.LogPath, now)
	// Derived rows of the other trace must survive the cleanup.
	mustExec(`INSERT INTO parse_jobs (trace_id, status, created_at, updated_at) VALUES (?, 'queued', ?, ?)`, kept.ID, now, now)

	result, err := st.ApplyRequestCleanup(RequestCleanupFilter{StatusCodes: []int{404}}, RequestCleanupApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyRequestCleanup() error = %v", err)
	}
	wantDerived := map[string]int{
		"trace_observations":           1,
		"trace_findings":               1,
		"trace_resources":              1,
		"credential_mask_audits":       1,
		"credential_mask_event_states": 1,
		"analysis_runs":                1,
		"parse_jobs":                   1,
	}
	for table, want := range wantDerived {
		if got := result.DeletedDerived[table]; got != want {
			t.Errorf("deleted %s = %d, want %d", table, got, want)
		}
	}
	if result.DeletedAnalysisJobs != 1 {
		t.Errorf("deleted analysis jobs = %d, want 1", result.DeletedAnalysisJobs)
	}
	if result.DeletedUpstreamExchanges != 1 {
		t.Errorf("deleted upstream exchanges = %d, want 1", result.DeletedUpstreamExchanges)
	}
	if result.PrunedResources != 1 {
		t.Errorf("pruned resources = %d, want 1", result.PrunedResources)
	}
	if cleanupCount(t, st, `SELECT COUNT(*) FROM resources`) != 0 {
		t.Error("the orphan resource catalogue row survived")
	}
	if cleanupCount(t, st, `SELECT COUNT(*) FROM parse_jobs WHERE trace_id = ?`, kept.ID) != 1 {
		t.Error("the surviving trace lost its parse job")
	}
	for _, table := range []string{"trace_observations", "trace_findings", "analysis_runs", "upstream_exchanges"} {
		if got := cleanupCount(t, st, `SELECT COUNT(*) FROM `+table+` WHERE trace_id = ?`, traceID); got != 0 {
			t.Errorf("%s still has %d row(s) for the deleted trace", table, got)
		}
	}
}

func TestRequestCleanupTraceIDMatchesChildExchange(t *testing.T) {
	st := configTransactionTestStore(t)
	child := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "cleanup-child-only", sessionID: "s-child", statusCode: 500,
		exchangeKind: "model", parentExchangeID: "entry:none",
	})

	// A status filter would skip this row because a model exchange is not
	// client-visible; an explicit trace id has to match it.
	plan, err := st.PlanRequestCleanup(RequestCleanupFilter{TraceIDs: []string{child.ID}})
	if err != nil {
		t.Fatalf("PlanRequestCleanup() error = %v", err)
	}
	if plan.MatchedRequests != 1 || plan.TotalLogRows != 1 {
		t.Fatalf("explicit trace id plan = matched %d total %d; want 1 and 1", plan.MatchedRequests, plan.TotalLogRows)
	}
}

func TestRequestCleanupLimitCapsSelection(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		cleanupInsertLog(t, st, cleanupLogSpec{
			requestID:  "cleanup-limit-" + string(rune('a'+i)),
			sessionID:  "s-limit",
			statusCode: 404,
			recordedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	plan, err := st.PlanRequestCleanup(RequestCleanupFilter{StatusCodes: []int{404}, Limit: 2})
	if err != nil {
		t.Fatalf("PlanRequestCleanup() error = %v", err)
	}
	if plan.MatchedRequests != 2 || plan.TotalLogRows != 2 {
		t.Fatalf("limited plan = matched %d total %d; want 2 and 2", plan.MatchedRequests, plan.TotalLogRows)
	}
}

func TestRequestCleanupRequiresASelector(t *testing.T) {
	st := configTransactionTestStore(t)
	if _, err := st.PlanRequestCleanup(RequestCleanupFilter{Model: "gpt-5"}); err == nil {
		t.Fatal("PlanRequestCleanup() with only a narrowing filter = nil error, want a selector error")
	}
}

func TestRequestCleanupPurgeReferencesIsOptIn(t *testing.T) {
	st := configTransactionTestStore(t)
	entry := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "cleanup-ref-404", sessionID: "s-ref", statusCode: 404})
	now := time.Now().UTC()
	if _, err := st.db.Exec(`INSERT INTO system_events (id, fingerprint, source, category, severity, status, trace_id, first_seen_at, last_seen_at, created_at, updated_at) VALUES ('ev-1', 'fp-1', 'proxy', 'request', 'warning', 'open', ?, ?, ?, ?, ?)`,
		entry.ID, now, now, now, now); err != nil {
		t.Fatalf("insert system_event: %v", err)
	}

	result, err := st.ApplyRequestCleanup(RequestCleanupFilter{StatusCodes: []int{404}}, RequestCleanupApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyRequestCleanup() error = %v", err)
	}
	if cleanupCount(t, st, `SELECT COUNT(*) FROM system_events`) != 1 {
		t.Fatal("system_events was purged without --purge-references")
	}
	if result.DeletedReferences != nil {
		t.Fatalf("deleted references = %v, want none", result.DeletedReferences)
	}
	if result.Plan.ReferenceCounts["system_events"] != 1 {
		t.Fatalf("reference counts = %v, want one system_events row", result.Plan.ReferenceCounts)
	}
}

func TestRequestCleanupPurgeReferencesRemovesCuratedRows(t *testing.T) {
	st := configTransactionTestStore(t)
	entry := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "cleanup-purge-404", sessionID: "s-purge", statusCode: 404})
	now := time.Now().UTC()
	if _, err := st.db.Exec(`INSERT INTO system_events (id, fingerprint, source, category, severity, status, trace_id, first_seen_at, last_seen_at, created_at, updated_at) VALUES ('ev-2', 'fp-2', 'proxy', 'request', 'warning', 'open', ?, ?, ?, ?, ?)`,
		entry.ID, now, now, now, now); err != nil {
		t.Fatalf("insert system_event: %v", err)
	}

	result, err := st.ApplyRequestCleanup(RequestCleanupFilter{StatusCodes: []int{404}}, RequestCleanupApplyOptions{PurgeReferences: true})
	if err != nil {
		t.Fatalf("ApplyRequestCleanup() error = %v", err)
	}
	if result.DeletedReferences["system_events"] != 1 {
		t.Fatalf("deleted references = %v, want one system_events row", result.DeletedReferences)
	}
	if cleanupCount(t, st, `SELECT COUNT(*) FROM system_events`) != 0 {
		t.Fatal("--purge-references left the system_events row behind")
	}
}
