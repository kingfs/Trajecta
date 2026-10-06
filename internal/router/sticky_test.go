package router

import (
	"strconv"
	"testing"
	"time"
)

// TestStickyBindingsAreReclaimedWhenExpired pins that bindings do not
// accumulate for the life of the process.
//
// A binding key comes from the client (a session id, a Codex window id, or a
// previous_response_id) and used to be removed only when that same key was
// looked up again after its TTL had passed. A client that varies its session id
// therefore grew the map without bound, and nothing else ever reclaimed the
// memory.
func TestStickyBindingsAreReclaimedWhenExpired(t *testing.T) {
	now := time.Now()
	store := NewStickyBindingStore(time.Minute)
	store.now = func() time.Time { return now }

	for i := 0; i < 50; i++ {
		store.Bind("session-"+strconv.Itoa(i), "target-a")
	}

	// Move past the TTL and the sweep interval, then write once.
	now = now.Add(2 * time.Minute)
	store.Bind("session-fresh", "target-a")

	store.mu.Lock()
	size := len(store.bindings)
	_, freshKept := store.bindings["session-fresh"]
	store.mu.Unlock()

	if size != 1 {
		t.Fatalf("bindings after expiry = %d, want only the fresh binding", size)
	}
	if !freshKept {
		t.Fatal("the fresh binding was dropped by the sweep")
	}
	if _, ok := store.Lookup("session-0"); ok {
		t.Fatal("an expired binding still resolved")
	}
}

// TestStickyBindingsStayBoundedUnderDistinctKeys pins the hard cap: a client
// that never reuses a session id must not be able to grow the map past
// maxStickyBindings, and the entries kept must be the ones furthest from expiry.
func TestStickyBindingsStayBoundedUnderDistinctKeys(t *testing.T) {
	now := time.Now()
	store := NewStickyBindingStore(time.Hour)
	store.now = func() time.Time { return now }

	// Advance the clock per bind so the entries have distinct expiry times and
	// the documented eviction order (closest to expiry first) is observable.
	total := maxStickyBindings + 5_000
	for i := 0; i < total; i++ {
		now = now.Add(time.Microsecond)
		store.Bind("session-"+strconv.Itoa(i), "target-a")
	}

	store.mu.Lock()
	size := len(store.bindings)
	store.mu.Unlock()

	if size > maxStickyBindings {
		t.Fatalf("bindings = %d, want at most %d", size, maxStickyBindings)
	}
	// The oldest keys are the ones closest to expiry, so they are the ones
	// evicted; the newest must survive.
	if _, ok := store.Lookup("session-0"); ok {
		t.Fatal("the oldest binding survived eviction")
	}
	if _, ok := store.Lookup("session-" + strconv.Itoa(total-1)); !ok {
		t.Fatal("the newest binding was evicted")
	}
}

// TestStickyLookupStillReclaimsItsOwnExpiredKey pins that the lazy path is
// unchanged: a lookup of an expired key removes that key and reports a miss.
func TestStickyLookupStillReclaimsItsOwnExpiredKey(t *testing.T) {
	now := time.Now()
	store := NewStickyBindingStore(time.Minute)
	store.now = func() time.Time { return now }

	store.Bind("session-a", "target-a")
	if got, ok := store.Lookup("session-a"); !ok || got != "target-a" {
		t.Fatalf("Lookup before expiry = %q, %v", got, ok)
	}

	now = now.Add(2 * time.Minute)
	if got, ok := store.Lookup("session-a"); ok {
		t.Fatalf("Lookup after expiry = %q, %v, want a miss", got, ok)
	}

	store.mu.Lock()
	size := len(store.bindings)
	store.mu.Unlock()
	if size != 0 {
		t.Fatalf("bindings after expired lookup = %d, want 0", size)
	}
}
