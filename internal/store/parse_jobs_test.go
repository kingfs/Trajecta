package store

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
)

// TestParseJobsHoldOneRowPerTrace pins the invariant that a trace has exactly
// one parse job. EnqueueParseJob (recording) and SaveObservation (parsing) both
// wrote a row, so every parsed trace appeared twice and the queue table doubled.
func TestParseJobsHoldOneRowPerTrace(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	traceID := "trace-single-parse-job"
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob(second) error = %v", err)
	}
	if count := countParseJobs(t, st, traceID); count != 1 {
		t.Fatalf("parse_jobs rows after two enqueues = %d, want 1", count)
	}

	if err := st.SaveObservation(observe.TraceObservation{
		TraceID:       traceID,
		Provider:      "openai_compatible",
		Operation:     "chat.completions",
		Model:         "gpt-test",
		Parser:        "store-test",
		ParserVersion: "1",
		Status:        observe.ParseStatusParsed,
	}); err != nil {
		t.Fatalf("SaveObservation() error = %v", err)
	}
	if count := countParseJobs(t, st, traceID); count != 1 {
		t.Fatalf("parse_jobs rows after parsing = %d, want 1", count)
	}
	if status := parseJobStatus(t, st, traceID); status != string(observe.ParseStatusParsed) {
		t.Fatalf("parse job status = %q, want the observation status %q", status, observe.ParseStatusParsed)
	}
}

// TestSaveObservationWithoutEnqueueCreatesTheJob keeps the path where a trace is
// parsed without ever being queued working.
func TestSaveObservationWithoutEnqueueCreatesTheJob(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	traceID := "trace-parsed-without-queue"
	if err := st.SaveObservation(observe.TraceObservation{
		TraceID:       traceID,
		Provider:      "openai_compatible",
		Operation:     "chat.completions",
		Model:         "gpt-test",
		Parser:        "store-test",
		ParserVersion: "1",
		Status:        observe.ParseStatusParsed,
	}); err != nil {
		t.Fatalf("SaveObservation() error = %v", err)
	}
	if count := countParseJobs(t, st, traceID); count != 1 {
		t.Fatalf("parse_jobs rows = %d, want 1", count)
	}
}

func countParseJobs(t *testing.T, st *Store, traceID string) int {
	t.Helper()

	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM parse_jobs WHERE trace_id = ?`, traceID).Scan(&count); err != nil {
		t.Fatalf("count parse_jobs(%q) error = %v", traceID, err)
	}
	return count
}

func parseJobStatus(t *testing.T, st *Store, traceID string) string {
	t.Helper()

	var status string
	if err := st.db.QueryRow(`SELECT status FROM parse_jobs WHERE trace_id = ?`, traceID).Scan(&status); err != nil {
		t.Fatalf("select parse_jobs status(%q) error = %v", traceID, err)
	}
	return status
}
