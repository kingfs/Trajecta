package store

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestBackgroundDerivedRefreshDrainsTheQueue is the contract the served process
// depends on: once StartDerivedRefresh is running, a write that defers a
// derived-table refresh is applied without any reader arriving.
func TestBackgroundDerivedRefreshDrainsTheQueue(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	st.markSessionSummariesOnly(t, "session-bg")
	if got := st.derivedQueueDepth(); got == 0 {
		t.Fatal("the deferred queue is empty, so the test cannot observe the flusher")
	}

	st.StartDerivedRefresh()
	deadline := time.Now().Add(10 * time.Second)
	for st.derivedQueueDepth() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the background flusher did not drain the queue; depth = %d", st.derivedQueueDepth())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBackgroundDerivedRefreshDrainsOnTheWriteSignal is the freshness guarantee a
// reader now relies on instead of flushing the queue itself: a write wakes the
// flusher directly, so the window a page can observe is milliseconds rather than
// the interval the ticker would impose. The bound is deliberately well below
// DerivedRefreshInterval so a regression to ticker-only draining fails here.
func TestBackgroundDerivedRefreshDrainsOnTheWriteSignal(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()
	st.StartDerivedRefresh()

	st.markSessionSummariesOnly(t, "session-signal")
	deadline := time.Now().Add(DerivedRefreshInterval / 4)
	for st.derivedQueueDepth() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the write did not wake the flusher within %s; depth = %d",
				DerivedRefreshInterval/4, st.derivedQueueDepth())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestReaderDoesNotFlushWhenTheBackgroundFlusherOwnsTheQueue pins the point of
// StartDerivedRefresh: in a served process a read must not be the thing that pays
// for the deferred rebuild. The queue is left untouched by the read, and the
// background flusher - not the reader - is what empties it.
func TestReaderDoesNotFlushWhenTheBackgroundFlusherOwnsTheQueue(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()
	st.StartDerivedRefresh()

	st.markSessionSummariesOnly(t, "session-read-barrier")
	before := st.derivedQueueDepth()
	if before == 0 {
		t.Fatal("the deferred queue is empty, so the test cannot observe the read")
	}

	// A session read is the read path that used to apply the queue itself.
	if _, err := st.GetSession("session-read-barrier"); err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if _, err := st.ListSessionPage(1, 10, ListFilter{}); err != nil {
		t.Fatalf("ListSessionPage() error = %v", err)
	}

	// The flusher may legitimately have drained the queue between the mark and the
	// reads. What must not happen is the queue being drained *by* the reads while
	// the flusher is stopped - so stop the flusher first, then read again, and
	// require the queue to survive.
	st.StopDerivedRefresh()
	st.markSessionSummariesOnly(t, "session-after-stop")
	if _, err := st.GetSession("session-after-stop"); err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	// Async mode is still latched after StopDerivedRefresh, so the read above did
	// not consume the queue either. The depth can only be non-zero if the reader
	// respected the barrier.
	if depth := st.derivedQueueDepth(); depth == 0 {
		t.Fatal("a read drained the deferred queue while the background flusher owned it")
	}
}

// TestReaderFlushesWhenNoBackgroundFlusherIsRunning keeps the synchronous
// guarantee that every test and CLI command relies on: a read observes the writes
// that preceded it, because the reader applies the queue itself.
func TestReaderFlushesWhenNoBackgroundFlusherIsRunning(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	st.markSessionSummariesOnly(t, "session-sync")
	if got := st.derivedQueueDepth(); got == 0 {
		t.Fatal("the deferred queue is empty, so the test cannot observe the reader")
	}
	if _, err := st.GetSession("session-sync"); err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if got := st.derivedQueueDepth(); got != 0 {
		t.Fatalf("a synchronous read left %d deferred refreshes unapplied, want 0", got)
	}
}

// TestStartDerivedRefreshIsIdempotentAndStopIsSafe covers the lifecycle calls a
// caller is allowed to make in any order, including on a store that was never
// started.
func TestStartDerivedRefreshIsIdempotentAndStopIsSafe(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	st.StopDerivedRefresh()
	st.StopDerivedRefresh()
	st.StartDerivedRefresh()
	st.StartDerivedRefresh()
	st.StopDerivedRefresh()
	st.StopDerivedRefresh()

	// Async stays latched, and a read still must not throw work away.
	if !st.shared.derivedAsync.Load() {
		t.Fatal("async mode was cleared by StopDerivedRefresh")
	}
	st.markSessionSummariesOnly(t, "session-lifecycle")
	// Close drains regardless of who owns the queue, so the deferred work is not
	// lost on the way out.
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// markSessionSummariesOnly defers a session refresh with one deterministic write,
// so a test can drive the derived queue without a full recording. It goes through
// the same marker the recording path uses rather than reimplementing it: indexing
// one row that belongs to a session defers that session's summary rewrite unless
// the queue is already at its bound.
func (s *Store) markSessionSummariesOnly(t *testing.T, sessionID string) {
	t.Helper()
	path := filepath.Join(s.outputDir, sessionID+".http")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  sessionID,
			Time:       time.Now().UTC(),
			Provider:   "openai_compatible",
			Operation:  "chat_completions",
			Endpoint:   "/v1/chat/completions",
			URL:        "https://api.openai.com/v1/chat/completions",
			Method:     http.MethodPost,
			StatusCode: http.StatusOK,
			DurationMs: 500,
			TTFTMs:     100,
		},
		Usage: recordfile.UsageInfo{TotalTokens: 10},
	}
	if err := s.UpsertLogWithGrouping(path, header, GroupingInfo{SessionID: sessionID, SessionSource: "header.session_id"}); err != nil {
		t.Fatalf("UpsertLogWithGrouping(%s) error = %v", path, err)
	}
	if got := s.derivedQueueDepth(); got == 0 {
		t.Fatalf("indexing a session row did not defer a derived refresh (queue depth 0)")
	}
}
