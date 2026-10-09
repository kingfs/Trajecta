package monitor

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// systemHostTopLevelKeys are the keys the page reads unconditionally. A missing
// key is a broken panel, so the test pins the whole contract rather than only
// the fields this machine happens to populate.
var systemHostTopLevelKeys = []string{
	"generated_at", "unsupported", "reason", "host", "cpu", "memory",
	"disk", "network", "process", "warnings",
}

func TestSystemHostAPIRouteIsAdminOnly(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	adminToken := systemTestToken(t, manager, 1, "admin", "admin")
	viewerToken := systemTestToken(t, manager, 2, "viewer", "viewer")

	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	if rr := systemTestRequest(mux, "/api/system/host", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/system/host without credentials = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	if rr := systemTestRequest(mux, "/api/system/host", viewerToken); rr.Code != http.StatusForbidden {
		t.Fatalf("GET /api/system/host as a non-admin = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if rr := systemTestRequest(mux, "/api/system/host", adminToken); rr.Code != http.StatusOK {
		t.Fatalf("GET /api/system/host as admin = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestSystemHostAPIReportsHostMetrics(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	token := systemTestToken(t, manager, 1, "admin", "admin")
	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	rr := systemTestRequest(mux, "/api/system/host", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/system/host = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &keys); err != nil {
		t.Fatalf("decode host payload keys: %v; body=%s", err, rr.Body.String())
	}
	for _, key := range systemHostTopLevelKeys {
		if _, ok := keys[key]; !ok {
			t.Fatalf("top-level key %q is missing from the host payload: %s", key, rr.Body.String())
		}
	}

	var payload systemHostResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode host payload: %v", err)
	}
	if payload.GeneratedAt.IsZero() {
		t.Fatalf("generated_at was not set: %s", rr.Body.String())
	}
	if payload.Warnings == nil {
		t.Fatalf("warnings is null; the page iterates it unconditionally: %s", rr.Body.String())
	}

	if runtime.GOOS != "linux" {
		// Other platforms answer the structured unsupported payload; the
		// numbers below only exist where /proc does.
		if !payload.Unsupported || payload.Reason == "" {
			t.Fatalf("unsupported/reason = %v/%q, want a structured unsupported answer", payload.Unsupported, payload.Reason)
		}
		return
	}
	if payload.Unsupported {
		t.Fatalf("linux build reports unsupported: %s", payload.Reason)
	}
	if payload.CPU.Cores < 1 {
		t.Fatalf("cpu.cores = %d, want at least one core", payload.CPU.Cores)
	}
	if len(payload.CPU.PerCorePercent) != payload.CPU.Cores {
		t.Fatalf("len(per_core_percent) = %d, want cpu.cores = %d", len(payload.CPU.PerCorePercent), payload.CPU.Cores)
	}
	if payload.Memory.TotalBytes == 0 {
		t.Fatalf("memory.total_bytes = 0, want the installed RAM: %s", rr.Body.String())
	}
	if len(payload.Disk) == 0 {
		t.Fatalf("disk is empty, want at least the root filesystem: %s", rr.Body.String())
	}
	if payload.Host.UptimeSeconds < 0 {
		t.Fatalf("host.uptime_seconds = %v, want a non-negative value", payload.Host.UptimeSeconds)
	}
	if payload.Process.PID != os.Getpid() {
		t.Fatalf("process.pid = %d, want this test process %d", payload.Process.PID, os.Getpid())
	}
	if payload.Process.RSSBytes == 0 {
		t.Fatalf("process.rss_bytes = 0, want this process's resident set")
	}
}

// TestSystemHostSamplerReusesRatesWithinTheMinimumInterval covers the rate
// cache: two samples taken from the same instant must not be divided by a
// near-zero interval, and the second answer must still be a complete document.
func TestSystemHostSamplerReusesRatesWithinTheMinimumInterval(t *testing.T) {
	t.Parallel()

	now := time.Now()
	first := buildSystemHost(now)
	second := buildSystemHost(now)

	if second.GeneratedAt.IsZero() {
		t.Fatalf("second sample has no generated_at: %+v", second)
	}
	if _, err := json.Marshal(second); err != nil {
		t.Fatalf("second sample does not encode: %v", err)
	}
	if first.Unsupported != second.Unsupported {
		t.Fatalf("unsupported changed between two immediate samples: %v then %v", first.Unsupported, second.Unsupported)
	}
	if runtime.GOOS != "linux" {
		if !second.Unsupported || second.Reason == "" {
			t.Fatalf("unsupported/reason = %v/%q, want a structured unsupported answer", second.Unsupported, second.Reason)
		}
		return
	}
	if second.Unsupported {
		t.Fatalf("linux build reports unsupported: %s", second.Reason)
	}
	if second.CPU.Cores < 1 || len(second.CPU.PerCorePercent) != second.CPU.Cores {
		t.Fatalf("cpu cores/per_core_percent = %d/%d, want the cached document to keep its shape",
			second.CPU.Cores, len(second.CPU.PerCorePercent))
	}
	if len(second.Disk) == 0 || second.Memory.TotalBytes == 0 {
		t.Fatalf("cached document is incomplete: disks=%d total_bytes=%d", len(second.Disk), second.Memory.TotalBytes)
	}
}
