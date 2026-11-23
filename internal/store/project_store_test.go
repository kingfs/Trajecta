package store

import (
	"testing"
	"time"
)

func projectTestDeclaration(root string, source string) *ProjectDeclaration {
	return &ProjectDeclaration{Root: root, RootKey: root, Source: source}
}

// A session keeps the project it started in, remembers the latest declaration
// and counts the changes, even when the parse jobs land out of order: the order
// comes from the trace time, not from the write order.
func TestSessionProjectRollupUsesTraceOrder(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	first := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "project-first", sessionID: "s-rollup", recordedAt: base,
	})
	second := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "project-second", sessionID: "s-rollup", recordedAt: base.Add(2 * time.Hour),
	})

	// The later trace is indexed before the earlier one on purpose.
	if err := st.SaveTraceProject(second.ID, "s-rollup", projectTestDeclaration("/repo/two", "dsh_runtime_context")); err != nil {
		t.Fatalf("save second project: %v", err)
	}
	if err := st.RebuildSessionProject("s-rollup"); err != nil {
		t.Fatalf("rebuild after second: %v", err)
	}
	rollup, ok, err := st.GetSessionProject("s-rollup")
	if err != nil || !ok {
		t.Fatalf("GetSessionProject() ok=%v err=%v", ok, err)
	}
	if rollup.Root != "/repo/two" || rollup.SwitchCount != 0 {
		t.Fatalf("after first declaration: root=%q switches=%d", rollup.Root, rollup.SwitchCount)
	}

	if err := st.SaveTraceProject(first.ID, "s-rollup", projectTestDeclaration("/repo/one", "dsh_runtime_context")); err != nil {
		t.Fatalf("save first project: %v", err)
	}
	if err := st.RebuildSessionProject("s-rollup"); err != nil {
		t.Fatalf("rebuild after first: %v", err)
	}
	rollup, ok, err = st.GetSessionProject("s-rollup")
	if err != nil || !ok {
		t.Fatalf("GetSessionProject() ok=%v err=%v", ok, err)
	}
	if rollup.Root != "/repo/one" || rollup.RootKey != "/repo/one" || rollup.Source != "dsh_runtime_context" {
		t.Fatalf("rollup root=%q key=%q source=%q, want the earliest trace", rollup.Root, rollup.RootKey, rollup.Source)
	}
	if rollup.LatestRoot != "/repo/two" || rollup.SwitchCount != 1 {
		t.Fatalf("latest=%q switches=%d, want /repo/two and 1", rollup.LatestRoot, rollup.SwitchCount)
	}
	if rollup.FirstTraceID != first.ID {
		t.Fatalf("first trace = %q, want %q", rollup.FirstTraceID, first.ID)
	}
}

// Re-parsing a trace that no longer declares anything must remove its row
// rather than leave the old project behind.
func TestSaveTraceProjectNilRemovesDeclaration(t *testing.T) {
	st := configTransactionTestStore(t)
	entry := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "project-clear", sessionID: "s-clear"})
	if err := st.SaveTraceProject(entry.ID, "s-clear", projectTestDeclaration("/repo/one", "claude_env")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.SaveTraceProject(entry.ID, "s-clear", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok, err := st.GetTraceProject(entry.ID); err != nil || ok {
		t.Fatalf("GetTraceProject() ok=%v err=%v, want absent", ok, err)
	}
	if err := st.RebuildSessionProject("s-clear"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, ok, err := st.GetSessionProject("s-clear"); err != nil || ok {
		t.Fatalf("GetSessionProject() ok=%v err=%v, want absent after the declaration was cleared", ok, err)
	}
}

// The project page aggregates sessions per root and reports how much of the
// window it could attribute instead of hiding the sessions it could not.
func TestListProjectsAggregatesAndReportsCoverage(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	sessions := []struct {
		sessionID string
		requestID string
		root      string
	}{
		{"s-alpha", "project-alpha", "/repo/shared"},
		{"s-beta", "project-beta", "/repo/shared"},
		{"s-gamma", "project-gamma", "/repo/other"},
		{"s-anon", "project-anon", ""},
	}
	for index, item := range sessions {
		entry := cleanupInsertLog(t, st, cleanupLogSpec{
			requestID:  item.requestID,
			sessionID:  item.sessionID,
			recordedAt: base.Add(time.Duration(index) * time.Minute),
			statusCode: 200,
		})
		if item.root == "" {
			continue
		}
		if err := st.SaveTraceProject(entry.ID, item.sessionID, projectTestDeclaration(item.root, "dsh_runtime_context")); err != nil {
			t.Fatalf("save project for %s: %v", item.sessionID, err)
		}
		if err := st.RebuildSessionProject(item.sessionID); err != nil {
			t.Fatalf("rebuild %s: %v", item.sessionID, err)
		}
	}
	if err := st.RebuildSessionSummaries(); err != nil {
		t.Fatalf("rebuild summaries: %v", err)
	}

	page, err := st.ListProjects(ProjectFilter{})
	if err != nil {
		t.Fatalf("ListProjects(): %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("Total = %d, want 2 projects", page.Total)
	}
	if page.IdentifiedSessions != 3 || page.TotalSessions != 4 {
		t.Fatalf("coverage = %d/%d, want 3/4", page.IdentifiedSessions, page.TotalSessions)
	}
	byRoot := map[string]ProjectSummary{}
	for _, item := range page.Items {
		byRoot[item.RootKey] = item
	}
	shared := byRoot["/repo/shared"]
	if shared.SessionCount != 2 || shared.RequestCount != 2 {
		t.Fatalf("shared project = %d sessions / %d requests, want 2/2", shared.SessionCount, shared.RequestCount)
	}
	if len(shared.Sources) != 1 || shared.Sources[0] != "dsh_runtime_context" {
		t.Fatalf("shared sources = %v", shared.Sources)
	}
	if _, ok := byRoot["/repo/other"]; !ok {
		t.Fatalf("missing /repo/other in %v", byRoot)
	}
	if page.Items[0].RootKey != "/repo/shared" {
		t.Fatalf("first project = %q, want the one with more sessions", page.Items[0].RootKey)
	}
}

// The project filter has to answer the same sessions on both session read
// paths; the summary row cannot see a per-request declaration, so the predicate
// comes from session_projects in both.
func TestSessionProjectFilterAgreesAcrossReadPaths(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	alpha := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "filter-alpha", sessionID: "s-alpha", recordedAt: base, statusCode: 200})
	cleanupInsertLog(t, st, cleanupLogSpec{requestID: "filter-beta", sessionID: "s-beta", recordedAt: base.Add(time.Minute), statusCode: 200})
	if err := st.SaveTraceProject(alpha.ID, "s-alpha", projectTestDeclaration("/repo/alpha", "codex_env")); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if err := st.RebuildSessionProject("s-alpha"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if err := st.RebuildSessionSummaries(); err != nil {
		t.Fatalf("rebuild summaries: %v", err)
	}

	filter := ListFilter{Project: "/repo/alpha"}
	listSessions := func(fromSummaries bool) []string {
		st.useSessionSummaryRead = fromSummaries
		defer func() { st.useSessionSummaryRead = false }()
		result, err := st.ListSessionPage(1, 50, filter)
		if err != nil {
			t.Fatalf("ListSessionPage(summaries=%v): %v", fromSummaries, err)
		}
		ids := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.SessionID)
		}
		return ids
	}

	for _, fromSummaries := range []bool{false, true} {
		ids := listSessions(fromSummaries)
		if len(ids) != 1 || ids[0] != "s-alpha" {
			t.Fatalf("summaries=%v: sessions = %v, want [s-alpha]", fromSummaries, ids)
		}
	}
}

// Backfill queues only traces without a project row, and never doubles a job
// that is already queued for the same trace.
func TestEnqueueProjectBackfillSkipsIndexedTraces(t *testing.T) {
	st := configTransactionTestStore(t)
	indexed := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "backfill-indexed", sessionID: "s-one"})
	pending := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "backfill-pending", sessionID: "s-two"})
	if err := st.SaveTraceProject(indexed.ID, "s-one", projectTestDeclaration("/repo/one", "dsh_runtime_context")); err != nil {
		t.Fatalf("save project: %v", err)
	}

	queued, err := st.EnqueueProjectBackfill(time.Time{}, 50)
	if err != nil {
		t.Fatalf("EnqueueProjectBackfill(): %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want 1", queued)
	}
	jobs, err := st.ListParseJobs("queued", 50)
	if err != nil {
		t.Fatalf("ListParseJobs(): %v", err)
	}
	if len(jobs) != 1 || jobs[0].TraceID != pending.ID {
		t.Fatalf("jobs = %+v, want only %s", jobs, pending.ID)
	}
	// A second run sees the queued job and does not add another.
	if queued, err = st.EnqueueProjectBackfill(time.Time{}, 50); err != nil || queued != 0 {
		t.Fatalf("second run queued=%d err=%v, want 0", queued, err)
	}
}
