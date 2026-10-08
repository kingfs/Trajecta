package store

import "time"

// DerivedRefreshInterval bounds how long a deferred derived-table refresh can sit
// unapplied when no signal reaches the background flusher. The signal covers the
// normal case within microseconds; the ticker is the safety net for a signal that
// was never delivered, and for the retry after a flush that failed.
const DerivedRefreshInterval = 2 * time.Second

// StartDerivedRefresh moves the deferred derived-table queue off the read path.
//
// The write path already defers the session-summary rewrite instead of doing it
// inside the recording (see markSessionSummariesRefresh). What it deferred was
// applied by whoever read the derived tables next, which meant a read request
// could be the one that paid for rebuilding a whole session - the same cost the
// write path had just refused, just moved to a different request.
//
// With this running, a background goroutine is the only consumer, and a reader
// answers from whatever the read model already holds. The window a reader can
// observe is bounded by DerivedRefreshInterval in the worst case and is normally
// the time it takes the flusher to wake up, because a writer signals it directly.
// The session list and the session detail are read models over an append-only
// index, so a sub-second lag shows as a request that is briefly absent from a
// session's totals, never as a wrong or inconsistent row.
//
// A caller that never starts this keeps the synchronous behaviour, where a reader
// applies the queue before it answers. That is what every test and the
// server-less CLI commands want, because it makes a read immediately consistent
// with the write that preceded it.
func (s *Store) StartDerivedRefresh() {
	if s == nil || s.shared == nil {
		return
	}
	shared := s.shared
	shared.derivedStartOnce.Do(func() {
		shared.derivedStop = make(chan struct{})
		go shared.runDerivedRefresh(s)
	})
	// Set after the goroutine exists, so a reader can never see async mode with
	// nobody consuming the queue.
	shared.derivedAsync.Store(true)
}

// StopDerivedRefresh ends the background flusher. It is idempotent, and it does
// not drain: the caller flushes (Close does) so the last writes are not lost.
func (s *Store) StopDerivedRefresh() {
	if s == nil || s.shared == nil {
		return
	}
	s.shared.derivedStopOnce.Do(func() {
		if s.shared.derivedStop != nil {
			close(s.shared.derivedStop)
		}
	})
}

// signalDerivedRefresh wakes the background flusher. It never blocks: the channel
// holds one pending wake-up, and a flusher that is already running will observe
// the queue the caller just added to because the flush reads the queue under the
// lock rather than receiving its contents through the channel.
func (s *Store) signalDerivedRefresh() {
	if s == nil || s.shared == nil || !s.shared.derivedAsync.Load() {
		return
	}
	signal := s.shared.derivedSignal
	if signal == nil {
		return
	}
	select {
	case signal <- struct{}{}:
	default:
	}
}

// flushDerivedBeforeRead applies the deferred derived refreshes before a read
// answers, unless a background flusher owns the queue. It replaces a bare
// flushDerivedRefresh on the read path so that the same reader code keeps the
// synchronous guarantee in tests and stops paying for the rebuild in a served
// process.
func (s *Store) flushDerivedBeforeRead() {
	if s == nil || s.shared == nil {
		return
	}
	if s.shared.derivedAsync.Load() {
		return
	}
	s.flushDerivedRefresh()
}

func (shared *storeShared) runDerivedRefresh(s *Store) {
	ticker := time.NewTicker(DerivedRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-shared.derivedStop:
			return
		case <-shared.derivedSignal:
		case <-ticker.C:
		}
		s.flushDerivedRefresh()
	}
}

// derivedQueueDepth reports how many refreshes are waiting. Tests use it to
// assert that the background flusher drains the queue; nothing on a request path
// reads it.
func (s *Store) derivedQueueDepth() int {
	if s == nil || s.shared == nil {
		return 0
	}
	shared := s.shared
	shared.derivedMu.Lock()
	defer shared.derivedMu.Unlock()
	return shared.derivedQueueSizeLocked()
}
