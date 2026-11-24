package store

import (
	"encoding/json"
	"testing"
	"time"
)

// The queue has two lanes so a historical sweep of a hundred thousand traces
// cannot delay the trace the proxy recorded a second ago. These tests pin the
// isolation and the retry policy, which are the two properties the worker relies
// on.

func TestParseJobLanesAreClaimedSeparately(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	if err := st.EnqueueParseJobInLane("backfill-1", ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	if err := st.EnqueueParseJobInLane("backfill-2", ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	if err := st.EnqueueParseJob("fresh-1"); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}

	fresh, err := st.ClaimParseJobs(10)
	if err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	if len(fresh) != 1 || fresh[0].TraceID != "fresh-1" {
		t.Fatalf("fresh claim = %+v, want only the fresh trace", fresh)
	}
	backfill, err := st.ClaimBackfillParseJobs(10)
	if err != nil {
		t.Fatalf("ClaimBackfillParseJobs() error = %v", err)
	}
	if len(backfill) != 2 {
		t.Fatalf("backfill claim = %+v, want both historical traces", backfill)
	}
	for _, job := range backfill {
		if job.Priority != ParsePriorityBackfill {
			t.Fatalf("job %s priority = %q", job.TraceID, job.Priority)
		}
	}
}

func TestEnqueuePromotesABackfillTraceToFresh(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	if err := st.EnqueueParseJobInLane("t1", ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	// The proxy recorded the same trace again: it must not wait behind the sweep.
	if err := st.EnqueueParseJob("t1"); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}
	fresh, err := st.ClaimParseJobs(10)
	if err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	if len(fresh) != 1 || fresh[0].TraceID != "t1" {
		t.Fatalf("fresh claim = %+v, want the promoted trace", fresh)
	}
	// The reverse must not happen: a historical sweep that sees a queued fresh
	// trace leaves it in the fresh lane.
	if err := st.EnqueueParseJob("t2"); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}
	if err := st.EnqueueParseJobInLane("t2", ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	fresh, err = st.ClaimParseJobs(10)
	if err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	if len(fresh) != 1 || fresh[0].TraceID != "t2" {
		t.Fatalf("fresh claim = %+v, want the fresh trace kept in its lane", fresh)
	}
}

func TestSweepMovesAFinishedFreshJobToTheBackfillLane(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	// A trace parsed before project attribution existed: its job is fresh and
	// done. The sweep has to be able to re-queue it, otherwise history would only
	// ever be walked once, in the parse lane.
	if err := st.EnqueueParseJob("t1"); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}
	jobs, err := st.ClaimParseJobs(1)
	if err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	if err := st.MarkParseJobDone(jobs[0].ID); err != nil {
		t.Fatalf("MarkParseJobDone() error = %v", err)
	}
	if err := st.EnqueueParseJobInLane("t1", ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	queuedBackfill, err := st.CountParseJobs("queued", ParsePriorityBackfill)
	if err != nil {
		t.Fatalf("CountParseJobs() error = %v", err)
	}
	if queuedBackfill != 1 {
		t.Fatalf("backfill queued = %d, want the finished fresh job moved to the sweep", queuedBackfill)
	}
	// A trace still waiting in the fresh lane must stay there.
	if err := st.EnqueueParseJob("t2"); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}
	if err := st.EnqueueParseJobInLane("t2", ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	queuedFresh, err := st.CountParseJobs("queued", ParsePriorityFresh)
	if err != nil {
		t.Fatalf("CountParseJobs() error = %v", err)
	}
	if queuedFresh != 1 {
		t.Fatalf("fresh queued = %d, want the waiting trace kept in its lane", queuedFresh)
	}
}

func TestRequeueOrFailParseJobRespectsAttempts(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	if err := st.EnqueueParseJob("t1"); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	claimed := func() ParseJobRecord {
		t.Helper()
		jobs, err := st.ClaimParseJobs(1)
		if err != nil {
			t.Fatalf("ClaimParseJobs() error = %v", err)
		}
		if len(jobs) != 1 {
			t.Fatalf("claimed = %d, want 1", len(jobs))
		}
		return jobs[0]
	}

	job := claimed()
	if status, err := st.RequeueOrFailParseJob(job.ID, 2, "transient"); err != nil || status != "queued" {
		t.Fatalf("first failure = %q, %v; want requeued", status, err)
	}
	job = claimed()
	if job.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", job.Attempts)
	}
	if status, err := st.RequeueOrFailParseJob(job.ID, 2, "permanent"); err != nil || status != "failed" {
		t.Fatalf("second failure = %q, %v; want failed at the cap", status, err)
	}
	failed, err := st.ListParseJobs("failed", 10)
	if err != nil {
		t.Fatalf("ListParseJobs() error = %v", err)
	}
	if len(failed) != 1 || failed[0].LastError != "permanent" {
		t.Fatalf("failed = %+v", failed)
	}
}

func TestAnalysisQueueSettingsDefaultsAndBounds(t *testing.T) {
	defaults := DefaultAnalysisQueueSettings()
	if defaults.ParseBatchSize != 10 || defaults.ParseIntervalMs != 5000 {
		t.Fatalf("defaults = %+v, want the pre-existing 10/5s", defaults)
	}
	if !defaults.BackfillEnabled || defaults.BackfillBatchSize != 2 {
		t.Fatalf("defaults = %+v, want a slow historical lane that is on", defaults)
	}
	if message := ValidateAnalysisQueueSettings(defaults); message != "" {
		t.Fatalf("defaults rejected: %s", message)
	}

	// A stored row written by an older build (zero fields) must normalize back to
	// a usable policy rather than spinning the worker.
	normalized := NormalizeAnalysisQueueSettings(AnalysisQueueSettings{ParseIntervalMs: 0, ParseBatchSize: 0})
	if normalized.ParseIntervalMs != defaults.ParseIntervalMs || normalized.ParseBatchSize != defaults.ParseBatchSize {
		t.Fatalf("normalized = %+v", normalized)
	}
	if normalized.BackfillIntervalMs < 200 {
		t.Fatalf("backfill interval = %d, want a floor", normalized.BackfillIntervalMs)
	}
	if message := ValidateAnalysisQueueSettings(AnalysisQueueSettings{ParseIntervalMs: 1, ParseBatchSize: 10, ParseMaxAttempts: 3, BackfillBatchSize: 2, BackfillIntervalMs: 5000, BackfillChunkSize: 1000, BackfillLowWatermark: 200}); message == "" {
		t.Fatal("an interval below the floor was accepted")
	}
}

func TestAnalysisQueueSettingsRoundTrip(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()
	ctx := t.Context()

	if got := st.LoadAnalysisQueueSettings(ctx); got.ParseBatchSize != DefaultAnalysisQueueSettings().ParseBatchSize {
		t.Fatalf("unset settings = %+v, want the defaults", got)
	}
	settings := DefaultAnalysisQueueSettings()
	settings.ParseBatchSize = 40
	settings.BackfillEnabled = false
	if err := st.SaveAnalysisQueueSettings(ctx, settings); err != nil {
		t.Fatalf("SaveAnalysisQueueSettings() error = %v", err)
	}
	loaded := st.LoadAnalysisQueueSettings(ctx)
	if loaded.ParseBatchSize != 40 || loaded.BackfillEnabled {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want the save time")
	}
}

func TestBackfillStateResetsToEmpty(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()
	ctx := t.Context()

	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	if err := st.SaveBackfillState(ctx, BackfillCursor{ProjectThrough: at}); err != nil {
		t.Fatalf("SaveBackfillState() error = %v", err)
	}
	state := st.LoadBackfillState(ctx)
	if !state.ProjectThrough.Equal(at) {
		t.Fatalf("state = %+v", state)
	}
	if err := st.ResetBackfillState(ctx); err != nil {
		t.Fatalf("ResetBackfillState() error = %v", err)
	}
	if got := st.LoadBackfillState(ctx); !got.ProjectThrough.IsZero() || !got.CompletedAt.IsZero() {
		t.Fatalf("state after reset = %+v, want the zero value", got)
	}
}

func TestBackfillCursorWireFormOmitsUnsetTimestamps(t *testing.T) {
	empty, err := json.Marshal(BackfillCursor{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(empty) != "{}" {
		t.Fatalf("zero cursor = %s, want {} (a zero time must not read as a real one)", empty)
	}
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	set, err := json.Marshal(BackfillCursor{ProjectThrough: at})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(set, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := decoded["project_through"]; !ok {
		t.Fatalf("payload = %s, want project_through", set)
	}
	if _, ok := decoded["completed_at"]; ok {
		t.Fatalf("payload = %s, want completed_at omitted", set)
	}
}

func TestReleaseParseJobReturnsTheClaimWithoutCountingAnAttempt(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	if err := st.EnqueueParseJob("t1"); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	jobs, err := st.ClaimParseJobs(1)
	if err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	if jobs[0].Attempts != 1 {
		t.Fatalf("attempts after claim = %d, want 1", jobs[0].Attempts)
	}
	if err := st.ReleaseParseJob(jobs[0].ID); err != nil {
		t.Fatalf("ReleaseParseJob() error = %v", err)
	}
	queued, err := st.ListParseJobs("queued", 10)
	if err != nil {
		t.Fatalf("ListParseJobs() error = %v", err)
	}
	if len(queued) != 1 || queued[0].Attempts != 0 {
		t.Fatalf("released job = %+v, want queued with the attempt un-counted", queued)
	}
}
