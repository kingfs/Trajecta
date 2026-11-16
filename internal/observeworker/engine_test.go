package observeworker

import (
	"context"
	"testing"
)

// TestWorkerXirEngineParsesNonStreamingTrace pins the opt-in engine wiring:
// the worker parses through the XIR projection when analysis.parser_engine is
// xir, and the stored summary records the xir parser.
func TestWorkerXirEngineParsesNonStreamingTrace(t *testing.T) {
	dir := t.TempDir()
	st, err := newTestStore(dir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedResponseTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	New(st, Options{BatchSize: 5, ParserEngine: "xir"}).RunOnce(context.Background())

	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Parser != "xir" || summary.Status != "parsed" {
		t.Fatalf("summary = %+v, want the xir parser", summary)
	}
}

// TestWorkerXirEngineFallsBackForEntryExchange pins the fallback boundary: an
// entry exchange has no XIR codec, so the worker must still store the observe
// entry observation instead of failing the parse job.
func TestWorkerXirEngineFallsBackForEntryExchange(t *testing.T) {
	dir := t.TempDir()
	st, err := newTestStore(dir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedEntryTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	New(st, Options{BatchSize: 5, ParserEngine: "xir"}).RunOnce(context.Background())

	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Parser != "entry" || summary.Status != "parsed" {
		t.Fatalf("summary = %+v, want the observe entry fallback", summary)
	}
}
