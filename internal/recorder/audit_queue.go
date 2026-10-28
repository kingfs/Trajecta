package recorder

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// DefaultAuditQueueDepth is how many completed mask-audit batches may wait for
// their own writer before the finalize worker writes one itself.
//
// The bound is larger than the finalize queue because a batch is tiny (the
// records of one exchange) and because the audit write is the part most likely
// to be briefly unavailable: a short database hiccup should lengthen this queue,
// not push the work back into the finalizer.
const DefaultAuditQueueDepth = 1024

// DefaultAuditWorkers is how many mask-audit batches are written at once. One is
// enough: the write is a small transaction, and serialising it keeps the
// per-rule event upserts from racing on the same fingerprint.
const DefaultAuditWorkers = 1

// maskAuditWriteAttempts is how many times one batch is tried before it is
// reported as lost. The delay grows between attempts so a database that is
// restarting has a moment to come back, while a shutdown never waits long.
const maskAuditWriteAttempts = 3

// asyncMaskAuditWriter is the side queue for credential-mask audit rows and the
// event they raise.
//
// Why it is separate from the cassette finalizer: finalising a recording is
// already off the request goroutine, but it is still a serial chain - rewrite the
// cassette, index the trace row, enqueue the parse job - and two workers serve
// every in-flight recording. A database write that is slow, locked, or briefly
// failing used to hold one of those workers through its retry budget, delaying
// the next recording's cassette. The audit is a derived fact: it can be written a
// moment later, and if it cannot be written at all the cassette is still the
// durable record.
//
// Robustness:
//
//   - The queue is bounded and never drops a batch silently. A full queue makes
//     the finalizer write that batch inline, exactly as it did before this type
//     existed; the worst case is the old latency, not a missing row.
//   - A failed write is retried with a short backoff. The audit and its event are
//     one transaction (see store.UpsertCredentialMaskAudits), so a retry can never
//     create a row whose notification is missing.
//   - Close drains the queue before the store closes. A batch that still fails
//     after the retries is logged with its trace id and count; the next request
//     that masks the same rule writes a fresh row and event, and
//     store.ReconcileCredentialMaskEvents heals a historical rule on startup.
type asyncMaskAuditWriter struct {
	queue     chan []store.CredentialMaskAuditRecord
	write     func([]store.CredentialMaskAuditRecord) error
	workers   int
	wg        sync.WaitGroup
	startOnce sync.Once
	closeOnce sync.Once
	// mu guards the queue close against a concurrent submit, exactly as the
	// finalize queue does: a send that races the close would panic on the request
	// or finalizer goroutine.
	mu      sync.RWMutex
	started atomic.Bool
	closed  atomic.Bool
	inline  atomic.Int64
	queued  atomic.Int64
	retried atomic.Int64
}

func newAsyncMaskAuditWriter(write func([]store.CredentialMaskAuditRecord) error, workers, depth int) *asyncMaskAuditWriter {
	if workers <= 0 {
		workers = DefaultAuditWorkers
	}
	if depth <= 0 {
		depth = DefaultAuditQueueDepth
	}
	return &asyncMaskAuditWriter{
		queue:   make(chan []store.CredentialMaskAuditRecord, depth),
		write:   write,
		workers: workers,
	}
}

func (w *asyncMaskAuditWriter) start() {
	if w == nil {
		return
	}
	w.startOnce.Do(func() {
		w.started.Store(true)
		for i := 0; i < w.workers; i++ {
			w.wg.Add(1)
			go func() {
				defer w.wg.Done()
				for records := range w.queue {
					w.queued.Add(1)
					w.writeWithRetry(records)
				}
			}()
		}
	})
}

// submit queues one batch, or writes it inline when no worker is running or the
// queue is at its bound. It never blocks the caller.
func (w *asyncMaskAuditWriter) submit(records []store.CredentialMaskAuditRecord) error {
	if w == nil || len(records) == 0 {
		return nil
	}
	batch := append([]store.CredentialMaskAuditRecord(nil), records...)
	w.mu.RLock()
	if w.closed.Load() || !w.started.Load() {
		w.mu.RUnlock()
		w.inline.Add(1)
		return w.writeWithRetry(batch)
	}
	select {
	case w.queue <- batch:
		w.mu.RUnlock()
		return nil
	default:
		w.mu.RUnlock()
		w.inline.Add(1)
		return w.writeWithRetry(batch)
	}
}

func (w *asyncMaskAuditWriter) writeWithRetry(records []store.CredentialMaskAuditRecord) error {
	if w == nil || len(records) == 0 || w.write == nil {
		return nil
	}
	var err error
	delay := 50 * time.Millisecond
	for attempt := 1; attempt <= maskAuditWriteAttempts; attempt++ {
		if err = w.write(records); err == nil {
			return nil
		}
		if attempt == maskAuditWriteAttempts {
			break
		}
		w.retried.Add(1)
		slog.Warn("Credential mask audit write failed, retrying",
			"attempt", attempt, "of", maskAuditWriteAttempts, "count", len(records), "error", err)
		time.Sleep(delay)
		delay *= 2
	}
	slog.Error("Credential mask audit write failed after retries",
		"attempts", maskAuditWriteAttempts, "count", len(records), "trace_id", records[0].TraceID, "error", err)
	return err
}

// close stops accepting work, waits for the running writes and finalises
// whatever is still queued. The timeout is the caller's shutdown budget; a queue
// that outlives it is still drained here, so a normal shutdown never abandons an
// audit batch.
func (w *asyncMaskAuditWriter) close(ctx context.Context, timeout time.Duration) error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed.Store(true)
		close(w.queue)
		w.mu.Unlock()
	})
	if timeout <= 0 {
		timeout = FinalizeTimeout
	}
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
	case <-time.After(timeout):
		err = errors.New("recorder: timed out draining the mask audit queue")
	}
	for records := range w.queue {
		w.queued.Add(1)
		w.writeWithRetry(records)
	}
	return err
}

type maskAuditStats struct {
	Queued  int64
	Inline  int64
	Retried int64
	Depth   int
}

func (w *asyncMaskAuditWriter) stats() maskAuditStats {
	if w == nil {
		return maskAuditStats{}
	}
	return maskAuditStats{
		Queued:  w.queued.Load(),
		Inline:  w.inline.Load(),
		Retried: w.retried.Load(),
		Depth:   len(w.queue),
	}
}
