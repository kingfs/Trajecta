package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRecordingManyExchangesLeavesNothingBehind drives a batch of ordinary exchanges
// through the whole recording path - prepare, stream the exchange into the cassette,
// finalise it by writing the prelude through a temporary file and renaming it into
// place, then index it - and asserts that the process returns to where it started.
//
// A proxy runs for months, so the failure modes that matter are the ones that only
// show up as a function of traffic: a goroutine left per request, a temporary file the
// finalisation did not clean up, or a recording that silently stopped being written.
// None of those are visible in a single-request test, and each of them is cheap to
// observe from the process's own state and from the trace directory.
func TestRecordingManyExchangesLeavesNothingBehind(t *testing.T) {
	handler, _, traceDir := newBodyLimitHandler(t, 64<<20)

	do := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	countFiles := func() (cassettes, temporary int) {
		t.Helper()
		err := filepath.WalkDir(traceDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			switch {
			case strings.HasPrefix(d.Name(), ".cassette-rewrite-"):
				temporary++
			case strings.HasSuffix(d.Name(), ".http"):
				cassettes++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk trace directory: %v", err)
		}
		return cassettes, temporary
	}

	// Warm up first: the first exchanges also start the store's background workers, and
	// those are part of the baseline rather than a leak.
	const warmup = 20
	for i := 0; i < warmup; i++ {
		do()
	}
	runtime.GC()
	baselineGoroutines := runtime.NumGoroutine()
	beforeCassettes, beforeTemporary := countFiles()

	const exchanges = 300
	for i := 0; i < exchanges; i++ {
		do()
	}
	runtime.GC()

	afterCassettes, afterTemporary := countFiles()
	if got, want := afterCassettes-beforeCassettes, exchanges; got != want {
		t.Fatalf("recorded %d cassettes for %d exchanges, want %d: recording stopped or overwrote a file", got, exchanges, want)
	}
	if beforeTemporary != 0 || afterTemporary != 0 {
		t.Fatalf("temporary prelude files left in the trace directory: %d before, %d after", beforeTemporary, afterTemporary)
	}
	if got := runtime.NumGoroutine(); got > baselineGoroutines+2 {
		t.Fatalf("goroutines grew from %d to %d over %d exchanges", baselineGoroutines, got, exchanges)
	}
}
