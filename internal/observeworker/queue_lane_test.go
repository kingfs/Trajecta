package observeworker

import (
	"context"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// The historical lane is a continuous background sweep, not a one-shot request:
// it walks backwards through recorded_at one chunk at a time and stops when it
// reaches the oldest trace. The cursor is what makes it terminate, because a
// trace that declares no working directory never gets a project row and would
// otherwise be queued on every pass.

func settingsFor(t *testing.T, mutate func(*store.AnalysisQueueSettings)) *RuntimeSettings {
	t.Helper()
	policy := store.DefaultAnalysisQueueSettings()
	policy.ParseBatchSize = 5
	policy.BackfillBatchSize = 5
	policy.BackfillChunkSize = 1
	policy.BackfillLowWatermark = 1
	if mutate != nil {
		mutate(&policy)
	}
	return NewRuntimeSettings(policy)
}

func TestWorkerPausesTheBackfillLane(t *testing.T) {
	dir := t.TempDir()
	st, err := newTestStore(dir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedResponseTrace(t, st, dir)
	if err := st.EnqueueParseJobInLane(traceID, store.ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	worker := New(st, Options{Settings: settingsFor(t, func(policy *store.AnalysisQueueSettings) {
		policy.BackfillEnabled = false
	})})
	worker.RunOnce(context.Background())

	if _, err := st.GetObservationSummary(traceID); err == nil {
		t.Fatal("the trace was parsed while the historical lane was paused")
	}
	queued, err := st.CountParseJobs("queued", store.ParsePriorityBackfill)
	if err != nil {
		t.Fatalf("CountParseJobs() error = %v", err)
	}
	if queued != 1 {
		t.Fatalf("backfill queued = %d, want the job left in place", queued)
	}
}

func TestWorkerBackfillSweepWalksBackwardsAndCompletes(t *testing.T) {
	dir := t.TempDir()
	st, err := newTestStore(dir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	older := writeIndexedResponseTraceAt(t, st, dir, "older", "req-older", time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC))
	newer := writeIndexedResponseTraceAt(t, st, dir, "newer", "req-newer", time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC))

	worker := New(st, Options{Settings: settingsFor(t, nil)})
	ctx := context.Background()

	// Pass 1 queues the newest un-attributed trace; the sweep has not reached
	// the older one yet.
	worker.RunOnce(ctx)
	state := st.LoadBackfillState(ctx)
	if state.CompletedAt.IsZero() == false {
		t.Fatalf("state = %+v, want the sweep unfinished after one chunk", state)
	}
	if !state.ProjectThrough.Equal(time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("cursor = %s, want the newest trace's time", state.ProjectThrough)
	}

	// Pass 2 parses the queued trace and queues the older one.
	worker.RunOnce(ctx)
	worker.RunOnce(ctx)
	state = st.LoadBackfillState(ctx)
	if !state.ProjectThrough.Equal(time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("cursor = %s, want the older trace's time", state.ProjectThrough)
	}

	// Pass 3 finds nothing older and records completion.
	worker.RunOnce(ctx)
	state = st.LoadBackfillState(ctx)
	if state.CompletedAt.IsZero() {
		t.Fatalf("state = %+v, want the sweep complete", state)
	}
	for _, traceID := range []string{older, newer} {
		if _, err := st.GetObservationSummary(traceID); err != nil {
			t.Fatalf("trace %s was never parsed: %v", traceID, err)
		}
	}

	// A completed sweep stays quiet: the same traces declare nothing, so a
	// selector without the cursor would queue them again forever.
	queuedBefore, err := st.CountParseJobs("queued", store.ParsePriorityBackfill)
	if err != nil {
		t.Fatalf("CountParseJobs() error = %v", err)
	}
	worker.RunOnce(ctx)
	queuedAfter, err := st.CountParseJobs("queued", store.ParsePriorityBackfill)
	if err != nil {
		t.Fatalf("CountParseJobs() error = %v", err)
	}
	if queuedAfter != queuedBefore {
		t.Fatalf("backfill queued %d -> %d after completion, want no new work", queuedBefore, queuedAfter)
	}
}

func TestWorkerProcessesFreshAndBackfillInOnePass(t *testing.T) {
	dir := t.TempDir()
	st, err := newTestStore(dir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	fresh := writeIndexedResponseTraceAt(t, st, dir, "fresh", "req-fresh", time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC))
	historical := writeIndexedResponseTraceAt(t, st, dir, "historical", "req-historical", time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC))
	if err := st.EnqueueParseJob(fresh); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}
	if err := st.EnqueueParseJobInLane(historical, store.ParsePriorityBackfill); err != nil {
		t.Fatalf("enqueue backfill: %v", err)
	}
	worker := New(st, Options{Settings: settingsFor(t, nil)})
	worker.RunOnce(context.Background())

	for _, traceID := range []string{fresh, historical} {
		if _, err := st.GetObservationSummary(traceID); err != nil {
			t.Fatalf("trace %s was not parsed: %v", traceID, err)
		}
	}
}
