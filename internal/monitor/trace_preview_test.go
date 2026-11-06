package monitor

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func previewTestItems(paths ...string) []traceListItem {
	items := make([]traceListItem, len(paths))
	for i, path := range paths {
		items[i] = traceListItem{logPath: path}
	}
	return items
}

// waitForTracePreviewSlots waits until every process-wide reader slot is free.
// The tests that deliberately block a read hold slots after the preview call has
// returned, so the next test must not start while those workers are unwinding.
func waitForTracePreviewSlots(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(tracePreviewSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if held := len(tracePreviewSlots); held != 0 {
		t.Fatalf("%d trace preview reader slots are still held; the preview workers leaked", held)
	}
}

// TestAttachTracePreviewsFastReaderKeepsOutput pins the non-deadline path: a
// fast reader's text lands on every distinct row, a shared cassette is read
// once, and nothing is counted as a deadline miss.
func TestAttachTracePreviewsFastReaderKeepsOutput(t *testing.T) {
	var calls atomic.Int64
	reader := func(_ context.Context, path string) string {
		calls.Add(1)
		return "preview:" + path
	}
	items := append(previewTestItems("a.http", "b.http"), previewTestItems("a.http")...)
	missesBefore := tracePreviewDeadlineMisses.Load()

	attachTracePreviewsWith(items, reader, time.Second)

	if got := calls.Load(); got != 2 {
		t.Fatalf("reader calls = %d, want 2 (one per distinct cassette)", got)
	}
	want := map[string]string{"a.http": "preview:a.http", "b.http": "preview:b.http"}
	for i, item := range items {
		if item.RequestPreview != want[item.logPath] {
			t.Fatalf("item %d preview = %q, want %q", i, item.RequestPreview, want[item.logPath])
		}
	}
	if misses := tracePreviewDeadlineMisses.Load() - missesBefore; misses != 0 {
		t.Fatalf("deadline misses = %d, want 0 for fast reads", misses)
	}
}

// TestAttachTracePreviewsHonoursHardBudget pins the hard bound: a reader that
// ignores its context and blocks forever must not hold the response open past
// the budget. The collector returns, the rows stay empty and every abandoned
// read is counted.
func TestAttachTracePreviewsHonoursHardBudget(t *testing.T) {
	waitForTracePreviewSlots(t)
	release := make(chan struct{})
	items := previewTestItems("a.http", "b.http", "c.http", "d.http", "e.http")
	missesBefore := tracePreviewDeadlineMisses.Load()

	start := time.Now()
	attachTracePreviewsWith(items, func(context.Context, string) string {
		<-release // ignores ctx on purpose: the collector must not wait for it
		return "late"
	}, 30*time.Millisecond)
	elapsed := time.Since(start)
	close(release)
	waitForTracePreviewSlots(t)

	if elapsed > time.Second {
		t.Fatalf("attachTracePreviewsWith returned after %v, want the 30ms budget to bound it", elapsed)
	}
	for i, item := range items {
		if item.RequestPreview != "" {
			t.Fatalf("item %d preview = %q, want none after a missed budget", i, item.RequestPreview)
		}
	}
	if misses := tracePreviewDeadlineMisses.Load() - missesBefore; misses < int64(len(items)) {
		t.Fatalf("deadline misses = %d, want at least %d", misses, len(items))
	}
}

// TestAttachTracePreviewsCancelsSlowReaderWithoutLeak pins that a slow reader
// which respects the context lets its workers stop: the call returns at the
// budget and the goroutine count falls back to where it started.
func TestAttachTracePreviewsCancelsSlowReaderWithoutLeak(t *testing.T) {
	waitForTracePreviewSlots(t)
	baseline := runtime.NumGoroutine()
	items := previewTestItems("a.http", "b.http", "c.http", "d.http", "e.http", "f.http")
	inFlight := atomic.Int64{}

	start := time.Now()
	attachTracePreviewsWith(items, func(ctx context.Context, _ string) string {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		<-ctx.Done()
		return ""
	}, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("attachTracePreviewsWith returned after %v, want the 30ms budget to bound it", elapsed)
	}

	// Workers notice the cancelled context and exit; poll instead of sleeping a
	// fixed amount so the assertion stays deterministic on a slow machine.
	deadline := time.Now().Add(2 * time.Second)
	for (inFlight.Load() != 0 || runtime.NumGoroutine() > baseline) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if left := inFlight.Load(); left != 0 {
		t.Fatalf("%d preview reads are still in flight after the call returned", left)
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Fatalf("goroutines after the call = %d, want no more than the %d at baseline", got, baseline)
	}
}

// TestAttachTracePreviewsBoundsConcurrentReads pins the process-wide buffer cap:
// several responses previewing at once must never run more than
// tracePreviewConcurrentReads cassette reads concurrently.
func TestAttachTracePreviewsBoundsConcurrentReads(t *testing.T) {
	waitForTracePreviewSlots(t)
	var inFlight, maxInFlight atomic.Int64
	reader := func(_ context.Context, _ string) string {
		current := inFlight.Add(1)
		for {
			observed := maxInFlight.Load()
			if current <= observed || maxInFlight.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		inFlight.Add(-1)
		return "preview"
	}
	const concurrentResponses = 4
	var wg sync.WaitGroup
	for i := 0; i < concurrentResponses; i++ {
		items := previewTestItems("a.http", "b.http", "c.http", "d.http", "e.http", "f.http", "g.http", "h.http")
		wg.Add(1)
		go func(items []traceListItem) {
			defer wg.Done()
			attachTracePreviewsWith(items, reader, 2*time.Second)
		}(items)
	}
	wg.Wait()

	peak := maxInFlight.Load()
	if peak > int64(tracePreviewConcurrentReads) {
		t.Fatalf("peak concurrent preview reads = %d, want at most %d", peak, tracePreviewConcurrentReads)
	}
	if peak < 2 {
		t.Fatalf("peak concurrent preview reads = %d; the test never exercised overlap", peak)
	}
}
