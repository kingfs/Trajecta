package main

import (
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// startDrainTestServer starts an HTTP server whose single handler blocks until the test
// releases it, and reports when that handler has been entered.
func startDrainTestServer(t *testing.T) (*http.Server, string, <-chan struct{}, chan<- struct{}) {
	t.Helper()

	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if once.CompareAndSwap(false, true) {
				close(entered)
			}
			<-release
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "drained")
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ln.Addr().String(), entered, release
}

// TestGracefulShutdownDrainsInFlightRequests pins what a shutdown signal must do to the
// request that is being proxied at that moment.
//
// The process used to die on the first SIGTERM, because nothing installed a handler:
// http.Server never stopped accepting, the in-flight request was cut mid-stream, and
// every deferred step - settling the derived read models, closing the store, stopping
// the parse and analysis workers - was skipped, which left a recording that had been
// written but not yet finalised with its prelude unusable. A drain has to keep the
// in-flight request alive and wait for it, not just stop listening.
func TestGracefulShutdownDrainsInFlightRequests(t *testing.T) {
	srv, addr, entered, release := startDrainTestServer(t)

	type result struct {
		status int
		body   string
		err    error
	}
	served := make(chan result, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + addr + "/v1/chat/completions")
		if err != nil {
			served <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		served <- result{status: resp.StatusCode, body: string(body)}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler was never entered")
	}

	shutdownReturned := make(chan struct{})
	go func() {
		gracefulShutdown(10*time.Second, srv)
		close(shutdownReturned)
	}()

	// The request is still blocked in the handler, so a drain that aborts instead of
	// waiting would return here and deliver a connection error.
	select {
	case <-shutdownReturned:
		t.Fatal("gracefulShutdown returned while a request was still in flight")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case got := <-served:
		if got.err != nil {
			t.Fatalf("the in-flight request failed during shutdown: %v", got.err)
		}
		if got.status != http.StatusOK || got.body != "drained" {
			t.Fatalf("in-flight request got status %d body %q, want 200 %q", got.status, got.body, "drained")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never completed")
	}

	select {
	case <-shutdownReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("gracefulShutdown did not return after the in-flight request finished")
	}

	// New connections are refused once the drain has completed.
	if _, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/"); err == nil {
		t.Fatal("the server still accepted a new connection after shutdown")
	}
}

// TestGracefulShutdownGivesUpOnAStuckRequest keeps a drain from holding the process open
// forever: the timeout expires and the call returns even though the handler never does.
func TestGracefulShutdownGivesUpOnAStuckRequest(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	go func() {
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
	}()
	// Give the request time to reach the handler before draining.
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		gracefulShutdown(150*time.Millisecond, srv)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gracefulShutdown did not respect its timeout with a stuck request")
	}

	// A nil server is skipped rather than panicking the drain.
	gracefulShutdown(50*time.Millisecond, nil)
}
