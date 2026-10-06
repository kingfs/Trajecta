package store

import (
	"sync"
	"testing"
	"time"
)

// TestSystemEventNotificationsAreSafeAgainstConcurrentUnsubscribe pins the
// invariant that a subscriber may be torn down at any moment without the
// publisher touching a closed channel. Sending on a closed channel is a ready
// select case, so the publisher's non-blocking `default:` branch does not
// protect it: the process panics.
//
// This is reachable from ordinary operation, not just from a test. The SSE
// handler unsubscribes when a browser closes /api/events/stream
// (internal/monitor/server.go), while the parse and reanalysis workers publish
// through UpsertSystemEvent from their own goroutines
// (internal/observeworker). A panic on a worker goroutine terminates the whole
// server because nothing in the repository recovers.
func TestSystemEventNotificationsAreSafeAgainstConcurrentUnsubscribe(t *testing.T) {
	st := configTransactionTestStore(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Publishers, mirroring the worker/recording callers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := st.UpsertSystemEvent(SystemEvent{
					Fingerprint: "fp-subscribe-race",
					Source:      "test",
					Category:    "concurrency",
					Severity:    "info",
					Title:       "race",
				}); err != nil {
					return
				}
			}
		}(i)
	}

	// Subscribers that attach and immediately detach, mirroring a client that
	// opens and closes the SSE stream repeatedly.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, unsubscribe := st.SubscribeSystemEvents(1)
				unsubscribe()
			}
		}()
	}

	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()
}
