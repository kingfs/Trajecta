package store

import (
	"testing"
	"time"
)

// A job is claimed by moving it to running, and nothing moves it back. A
// process killed while parsing (a redeploy, an OOM) therefore stranded its
// trace in running forever: the trace was never re-parsed, so it never got an
// observation, a finding, a resource index or a project attribution. The
// reference deployment had accumulated 734 of them, which is why a project
// backfill queued 500 traces and wrote no rows.

func TestReclaimStaleParseJobsReturnsInterruptedRunsToTheQueue(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	for _, traceID := range []string{"stale-1", "stale-2", "fresh-1"} {
		if err := st.EnqueueParseJob(traceID); err != nil {
			t.Fatalf("EnqueueParseJob(%s) error = %v", traceID, err)
		}
	}
	claimed, err := st.ClaimParseJobs(10)
	if err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	if len(claimed) != 3 {
		t.Fatalf("claimed = %d, want 3", len(claimed))
	}
	// Two of them were claimed long ago; the third is still in flight.
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := st.db.Exec(`UPDATE parse_jobs SET updated_at = ? WHERE trace_id IN ('stale-1','stale-2')`, past); err != nil {
		t.Fatalf("age the running rows: %v", err)
	}

	reclaimed, err := st.ReclaimStaleParseJobs(time.Now().UTC().Add(-5 * time.Minute))
	if err != nil {
		t.Fatalf("ReclaimStaleParseJobs() error = %v", err)
	}
	if reclaimed != 2 {
		t.Fatalf("reclaimed = %d, want the two interrupted rows", reclaimed)
	}
	queuedJobs, err := st.ListParseJobs("queued", 10)
	if err != nil {
		t.Fatalf("ListParseJobs(queued) error = %v", err)
	}
	runningJobs, err := st.ListParseJobs("running", 10)
	if err != nil {
		t.Fatalf("ListParseJobs(running) error = %v", err)
	}
	queued := map[string]bool{}
	for _, job := range queuedJobs {
		queued[job.TraceID] = true
	}
	if !queued["stale-1"] || !queued["stale-2"] {
		t.Fatalf("queued = %+v, want the interrupted rows back in the queue", queued)
	}
	if len(runningJobs) != 1 || runningJobs[0].TraceID != "fresh-1" {
		t.Fatalf("running = %+v, want the job still in flight left alone", runningJobs)
	}
	// The reclaimed rows must be claimable again, which is the point.
	again, err := st.ClaimParseJobs(10)
	if err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	if len(again) != 2 {
		t.Fatalf("claimed after reclaim = %d, want 2", len(again))
	}
}

func TestReclaimStaleParseJobsIsANoOpWithoutACutoff(t *testing.T) {
	st, err := newSQLiteTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()
	if err := st.EnqueueParseJob("t1"); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	if _, err := st.ClaimParseJobs(1); err != nil {
		t.Fatalf("ClaimParseJobs() error = %v", err)
	}
	count, err := st.ReclaimStaleParseJobs(time.Time{})
	if err != nil || count != 0 {
		t.Fatalf("ReclaimStaleParseJobs(zero) = %d, %v; want a no-op", count, err)
	}
}
