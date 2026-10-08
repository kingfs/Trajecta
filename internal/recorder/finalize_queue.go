package recorder

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultFinalizeQueueDepth is how many completed recordings may be waiting to
// be finalised before the request goroutine does the work itself.
const DefaultFinalizeQueueDepth = 256

// DefaultFinalizeWorkers is how many recordings are finalised at once.
const DefaultFinalizeWorkers = 2

// FinalizeTimeout bounds how long Close waits for the queue to drain. It is
// generous because draining replays a full file rewrite per pending recording,
// and the process is shutting down anyway.
const FinalizeTimeout = 30 * time.Second

// asyncFinalizer moves the finalisation of a completed recording - the cassette
// rewrite that prepends the prelude, the trace index upsert and the parse-job
// enqueue - off the proxy's request goroutine.
//
// Why it matters here: finalising a cassette rewrites the whole file to put the
// prelude in front of the record it already holds, and a recording is as large
// as the response body it captured (the reference deployment averages 406 kB per
// request and sums to 97 GB). Doing that inline means the request goroutine, and
// the client connection it still owns, stays busy for the duration of a full
// read and write of that file on a rotational disk, long after the client has
// received the last byte.
//
// The queue is deliberately simple and deliberately lossless:
//
//   - It is bounded, and a full queue does NOT drop the recording. Submit falls
//     back to finalising inline, which is exactly the behaviour this type
//     replaces, so the worst case is the old latency rather than a lost trace.
//   - It is drained on Close before the store is closed, so a normal shutdown
//     finalises everything that was submitted.
//   - If the process is killed outright, the cassette is left in its
//     record-first state. That state is indistinguishable from a partial write
//     and cannot be repaired, because the metadata that becomes the prelude only
//     ever existed in memory. It is counted and logged rather than silently
//     ignored; see Store.Sync and the fragment handling there.
type asyncFinalizer struct {
	queue      chan *LogInfo
	finalize   func(*LogInfo) error
	workers    int
	wg         sync.WaitGroup
	startOnce  sync.Once
	closeOnce  sync.Once
	started    atomic.Bool
	closed     atomic.Bool
	inlineRuns atomic.Int64
	queuedRuns atomic.Int64
}

func newAsyncFinalizer(finalize func(*LogInfo) error, workers int, depth int) *asyncFinalizer {
	if workers <= 0 {
		workers = DefaultFinalizeWorkers
	}
	if depth <= 0 {
		depth = DefaultFinalizeQueueDepth
	}
	return &asyncFinalizer{
		queue:    make(chan *LogInfo, depth),
		finalize: finalize,
		workers:  workers,
	}
}

// start launches the workers. It is safe to call more than once; only the first
// call has an effect, so a caller that never opts in keeps the synchronous
// behaviour and never pays for goroutines it does not use.
func (f *asyncFinalizer) start() {
	if f == nil {
		return
	}
	f.startOnce.Do(func() {
		f.started.Store(true)
		for i := 0; i < f.workers; i++ {
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				for info := range f.queue {
					f.run(info)
				}
			}()
		}
	})
}

// submit hands one recording to the workers, or finalises it inline when the
// queue is full or the finalizer is closed. It never blocks on the queue and it
// never drops a recording.
func (f *asyncFinalizer) submit(info *LogInfo) error {
	if f == nil || info == nil {
		return nil
	}
	// Before the workers exist and after they stop, the only correct thing to do
	// with the recording is the work itself. Queuing without a consumer would
	// leave it unfinalised for as long as the process lives.
	if f.closed.Load() || !f.started.Load() {
		f.inlineRuns.Add(1)
		return f.finalize(info)
	}
	select {
	case f.queue <- info:
		f.queuedRuns.Add(1)
		return nil
	default:
		// The queue is at its bound: the caller does the work rather than waiting
		// for a worker or dropping the recording.
		f.inlineRuns.Add(1)
		return f.finalize(info)
	}
}

func (f *asyncFinalizer) run(info *LogInfo) {
	if info == nil {
		return
	}
	if err := f.finalize(info); err != nil {
		slog.Error("Failed to finalize recording", "path", info.Path, "error", err)
	}
}

// close stops accepting work and waits for the queue to drain. A context
// deadline or a timeout ends the wait; whatever is still queued is then
// finalised by the caller's goroutine so a shutdown never abandons a recording.
func (f *asyncFinalizer) close(ctx context.Context, timeout time.Duration) error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		f.closed.Store(true)
		close(f.queue)
	})
	if timeout <= 0 {
		timeout = FinalizeTimeout
	}
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
	case <-time.After(timeout):
		err = errors.New("recorder: timed out draining the finalize queue")
	}
	// A queue that never had workers, or a drain that timed out, can still hold
	// recordings. Finalise them here so shutting down never abandons one.
	for info := range f.queue {
		f.run(info)
	}
	return err
}

// Close drains the finalize queue and stops the workers. It is safe to call more
// than once and on a recorder that never queued anything.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.finalizer.close(ctx, FinalizeTimeout)
}

// FinalizeStats reports how the queue has been used, so an operator can tell
// whether the workers are keeping up or every request is falling back to the
// inline path.
type FinalizeStats struct {
	Queued int64 `json:"queued"`
	Inline int64 `json:"inline"`
	Depth  int   `json:"depth"`
}

func (r *Recorder) FinalizeStats() FinalizeStats {
	if r == nil || r.finalizer == nil {
		return FinalizeStats{}
	}
	return FinalizeStats{
		Queued: r.finalizer.queuedRuns.Load(),
		Inline: r.finalizer.inlineRuns.Load(),
		Depth:  len(r.finalizer.queue),
	}
}
