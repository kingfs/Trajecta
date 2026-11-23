package store

import (
	"errors"
	"testing"
	"time"
)

type storeEntry struct {
	traceID   string
	sessionID string
}

func projectTestDeclaration(root string, source string) *ProjectRecord {
	return &ProjectRecord{Root: root, RootKey: root, Source: source}
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
	// Deliberately no RebuildSessionSummaries: the page aggregates logs, so a
	// deployment that never backfilled the summary read model still sees it.
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

// A session is in the window when it has a request at or after the window
// start, and its counts cover the whole session - the same rule the session
// row follows, so the project row and the sessions it opens agree.
func TestListProjectsWindowCountsWholeSession(t *testing.T) {
	st := configTransactionTestStore(t)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	old := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID:  "window-old",
		sessionID:  "s-window",
		recordedAt: now.Add(-48 * time.Hour),
		statusCode: 200,
	})
	recent := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID:  "window-recent",
		sessionID:  "s-window",
		recordedAt: now.Add(-2 * time.Hour),
		statusCode: 200,
	})
	// Same project, but only activity outside the window: it must not appear.
	cleanupInsertLog(t, st, cleanupLogSpec{
		requestID:  "window-stale",
		sessionID:  "s-stale",
		recordedAt: now.Add(-72 * time.Hour),
		statusCode: 200,
	})

	if err := st.SaveTraceProject(old.ID, "s-window", projectTestDeclaration("/repo/window", "codex_env")); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if err := st.RebuildSessionProject("s-window"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	_ = recent

	page, err := st.ListProjects(ProjectFilter{Since: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatalf("ListProjects(): %v", err)
	}
	if page.TotalSessions != 1 || page.IdentifiedSessions != 1 {
		t.Fatalf("coverage = %d/%d, want 1/1 (only the active session)", page.IdentifiedSessions, page.TotalSessions)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("projects = %+v", page.Items)
	}
	if got := page.Items[0].RequestCount; got != 2 {
		t.Fatalf("request_count = %d, want the whole session's 2 requests", got)
	}
}

// A trace cleanup deletes the project row with the trace and repairs the
// session rollup in the same transaction, so a deleted request cannot leave a
// stale attribution behind.
func TestRequestCleanupRemovesProjectAttribution(t *testing.T) {
	st := configTransactionTestStore(t)
	entry := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID:  "cleanup-project",
		sessionID:  "s-cleanup",
		statusCode: 404,
	})
	if err := st.SaveTraceProject(entry.ID, "s-cleanup", projectTestDeclaration("/repo/cleanup", "claude_env")); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if err := st.RebuildSessionProject("s-cleanup"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, ok, err := st.GetSessionProject("s-cleanup"); err != nil || !ok {
		t.Fatalf("precondition: session rollup ok=%v err=%v", ok, err)
	}

	if _, err := st.ApplyRequestCleanup(RequestCleanupFilter{StatusCodes: []int{404}}, RequestCleanupApplyOptions{}); err != nil {
		t.Fatalf("ApplyRequestCleanup(): %v", err)
	}
	if _, ok, err := st.GetTraceProject(entry.ID); err != nil || ok {
		t.Fatalf("trace project survived cleanup: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.GetSessionProject("s-cleanup"); err != nil || ok {
		t.Fatalf("session rollup survived cleanup: ok=%v err=%v", ok, err)
	}
}

// A rejected declaration is stored and reported for detection health, but it
// never attributes a session: a placeholder or an unreadable block is not a
// project.
func TestRejectedProjectRecordIsReportedWithoutAttribution(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	declared := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "project-declared", sessionID: "s-declared", recordedAt: base, statusCode: 200,
	})
	rejected := cleanupInsertLog(t, st, cleanupLogSpec{
		requestID: "project-rejected", sessionID: "s-rejected", recordedAt: base.Add(time.Minute), statusCode: 200,
	})
	if _, err := st.db.Exec(`UPDATE logs SET client_harness = ? WHERE trace_id = ?`, "codex-cli", rejected.ID); err != nil {
		t.Fatalf("set harness: %v", err)
	}
	if err := st.SaveTraceProject(declared.ID, "s-declared", projectTestDeclaration("/repo/declared", "dsh_runtime_context")); err != nil {
		t.Fatalf("save declared: %v", err)
	}
	if err := st.SaveTraceProject(rejected.ID, "s-rejected", &ProjectRecord{
		Rejected: true,
		Source:   "codex_env",
		Reason:   "unreadable",
		Evidence: "<environment_context> without <cwd>",
	}); err != nil {
		t.Fatalf("save rejected: %v", err)
	}
	if err := st.RebuildSessionProject("s-declared"); err != nil {
		t.Fatalf("rebuild declared: %v", err)
	}
	if err := st.RebuildSessionProject("s-rejected"); err != nil {
		t.Fatalf("rebuild rejected: %v", err)
	}

	if _, ok, err := st.GetSessionProject("s-rejected"); err != nil || ok {
		t.Fatalf("rejected session got an attribution: ok=%v err=%v", ok, err)
	}
	record, ok, err := st.GetTraceProject(rejected.ID)
	if err != nil || !ok {
		t.Fatalf("GetTraceProject() ok=%v err=%v", ok, err)
	}
	if record.Status != ProjectRejected || record.Reason != "unreadable" || record.Evidence == "" {
		t.Fatalf("record = %+v", record)
	}

	report, err := st.ProjectDetectionReport(ProjectDetectionFilter{})
	if err != nil {
		t.Fatalf("ProjectDetectionReport(): %v", err)
	}
	if report.Declared != 1 || report.Rejected != 1 {
		t.Fatalf("report = %+v, want 1 declared / 1 rejected", report)
	}
	if len(report.ByReason) != 1 || report.ByReason[0].Label != "unreadable" || report.ByReason[0].Count != 1 {
		t.Fatalf("by reason = %+v", report.ByReason)
	}
	foundHarness := false
	for _, harness := range report.ByHarness {
		if harness.Harness == "codex-cli" && harness.Rejected == 1 {
			foundHarness = true
		}
	}
	if !foundHarness {
		t.Fatalf("by harness = %+v, want codex-cli with 1 rejection", report.ByHarness)
	}
	if len(report.Recent) != 1 || report.Recent[0].TraceID != rejected.ID {
		t.Fatalf("recent = %+v", report.Recent)
	}
}

// A merge is a read-time view: the alias folds the two paths into one project
// row and one session set, and deleting it restores both.
func TestProjectAliasesMergeReads(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 23, 16, 0, 0, 0, time.UTC)
	container := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "alias-container", sessionID: "s-container", recordedAt: base, statusCode: 200})
	host := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "alias-host", sessionID: "s-host", recordedAt: base.Add(time.Minute), statusCode: 200})
	other := cleanupInsertLog(t, st, cleanupLogSpec{requestID: "alias-other", sessionID: "s-other", recordedAt: base.Add(2 * time.Minute), statusCode: 200})
	for _, item := range []struct {
		entry storeEntry
		root  string
	}{
		{storeEntry{container.ID, "s-container"}, "/app"},
		{storeEntry{host.ID, "s-host"}, "/data/src/app"},
		{storeEntry{other.ID, "s-other"}, "/repo/other"},
	} {
		if err := st.SaveTraceProject(item.entry.traceID, item.entry.sessionID, projectTestDeclaration(item.root, "dsh_runtime_context")); err != nil {
			t.Fatalf("save project %s: %v", item.root, err)
		}
		if err := st.RebuildSessionProject(item.entry.sessionID); err != nil {
			t.Fatalf("rebuild %s: %v", item.entry.sessionID, err)
		}
	}

	before, err := st.ListProjects(ProjectFilter{})
	if err != nil {
		t.Fatalf("ListProjects() before merge: %v", err)
	}
	if before.Total != 3 {
		t.Fatalf("before merge total = %d, want 3", before.Total)
	}

	if err := st.SaveProjectAlias(ProjectAlias{
		AliasKey: "/app", CanonicalKey: "/data/src/app", CanonicalRoot: "/data/src/app",
	}); err != nil {
		t.Fatalf("SaveProjectAlias(): %v", err)
	}
	after, err := st.ListProjects(ProjectFilter{})
	if err != nil {
		t.Fatalf("ListProjects() after merge: %v", err)
	}
	if after.Total != 2 {
		t.Fatalf("after merge total = %d, want 2", after.Total)
	}
	var merged ProjectSummary
	for _, item := range after.Items {
		if item.RootKey == "/data/src/app" {
			merged = item
		}
	}
	if merged.SessionCount != 2 || merged.RequestCount != 2 || merged.PathCount != 2 {
		t.Fatalf("merged = %+v, want 2 sessions / 2 requests / 2 paths", merged)
	}

	// Filtering by the canonical key or by a merged path lists both sessions.
	for _, key := range []string{"/data/src/app", "/app"} {
		page, err := st.ListSessionPage(1, 50, ListFilter{Project: key})
		if err != nil {
			t.Fatalf("ListSessionPage(%q): %v", key, err)
		}
		if page.Total != 2 {
			t.Fatalf("filter %q total = %d, want the merged 2", key, page.Total)
		}
	}

	// The session view reports the canonical project.
	rollup, ok, err := st.GetSessionProject("s-container")
	if err != nil || !ok {
		t.Fatalf("GetSessionProject() ok=%v err=%v", ok, err)
	}
	if rollup.CanonicalRoot != "/data/src/app" || rollup.CanonicalKey != "/data/src/app" {
		t.Fatalf("canonical = %q/%q", rollup.CanonicalRoot, rollup.CanonicalKey)
	}

	// Deleting the alias restores the separate projects.
	if err := st.DeleteProjectAlias("/app"); err != nil {
		t.Fatalf("DeleteProjectAlias(): %v", err)
	}
	restored, err := st.ListProjects(ProjectFilter{})
	if err != nil {
		t.Fatalf("ListProjects() after delete: %v", err)
	}
	if restored.Total != 3 {
		t.Fatalf("after delete total = %d, want 3", restored.Total)
	}
}

func TestProjectAliasValidation(t *testing.T) {
	st := configTransactionTestStore(t)
	if err := st.SaveProjectAlias(ProjectAlias{AliasKey: "/a", CanonicalKey: "/a"}); !errors.Is(err, ErrProjectAliasSelf) {
		t.Fatalf("self alias error = %v, want ErrProjectAliasSelf", err)
	}
	if err := st.SaveProjectAlias(ProjectAlias{AliasKey: "", CanonicalKey: "/a"}); !errors.Is(err, ErrProjectAliasRequired) {
		t.Fatalf("missing key error = %v", err)
	}
	if err := st.SaveProjectAlias(ProjectAlias{AliasKey: "/a", CanonicalKey: "/b"}); err != nil {
		t.Fatalf("first alias: %v", err)
	}
	// /b is now a merge target, so making it an alias would chain.
	if err := st.SaveProjectAlias(ProjectAlias{AliasKey: "/b", CanonicalKey: "/c"}); !errors.Is(err, ErrProjectAliasChain) {
		t.Fatalf("chain error = %v, want ErrProjectAliasChain", err)
	}
	// /c -> /a would chain too (/c -> /a -> /b).
	if err := st.SaveProjectAlias(ProjectAlias{AliasKey: "/c", CanonicalKey: "/a"}); !errors.Is(err, ErrProjectAliasChain) {
		t.Fatalf("reverse chain error = %v, want ErrProjectAliasChain", err)
	}
}
