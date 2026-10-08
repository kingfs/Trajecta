package monitor

import (
	"net/http"
	"net/http/pprof"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// systemSlowQueryLimit caps the ring snapshot the page renders. The ring itself
// holds 50 entries; asking for exactly that keeps one page render to one copy.
const systemSlowQueryLimit = 50

// processStartedAt approximates the process start. The monitor package is
// imported by the server binary, so its package initialisation runs before the
// listeners come up; a millisecond of skew does not matter for an uptime.
var processStartedAt = time.Now()

type systemRuntimeResponse struct {
	GeneratedAt   time.Time           `json:"generated_at"`
	GoVersion     string              `json:"go_version"`
	GOOS          string              `json:"goos"`
	GOARCH        string              `json:"goarch"`
	NumCPU        int                 `json:"num_cpu"`
	GOMAXPROCS    int                 `json:"gomaxprocs"`
	Goroutines    int                 `json:"goroutines"`
	UptimeSeconds float64             `json:"uptime_seconds"`
	Heap          systemRuntimeHeap   `json:"heap"`
	GC            systemRuntimeGC     `json:"gc"`
	DBPool        systemRuntimeDBPool `json:"db_pool"`
}

type systemRuntimeHeap struct {
	// AllocBytes is cumulative: every byte ever allocated for heap objects.
	AllocBytes uint64 `json:"alloc_bytes"`
	// InUseBytes is live heap objects plus not-yet-reused garbage.
	InUseBytes uint64 `json:"in_use_bytes"`
	Objects    uint64 `json:"objects"`
	TotalSys   uint64 `json:"total_sys_bytes"`
	StackBytes uint64 `json:"stack_bytes"`
}

type systemRuntimeGC struct {
	Cycles           uint64    `json:"cycles"`
	RecentPauses     int       `json:"recent_pause_count"`
	PauseTotalMs     float64   `json:"pause_total_ms"`
	LastGC           time.Time `json:"last_gc,omitempty"`
	RecentPauseMinMs float64   `json:"recent_pause_min_ms"`
	RecentPauseP25Ms float64   `json:"recent_pause_p25_ms"`
	RecentPauseP50Ms float64   `json:"recent_pause_p50_ms"`
	RecentPauseP75Ms float64   `json:"recent_pause_p75_ms"`
	RecentPauseMaxMs float64   `json:"recent_pause_max_ms"`
	HeapGoalBytes    uint64    `json:"heap_goal_bytes"`
}

type systemRuntimeDBPool struct {
	Driver            string  `json:"driver"`
	MaxOpen           int     `json:"max_open"`
	Open              int     `json:"open"`
	InUse             int     `json:"in_use"`
	Idle              int     `json:"idle"`
	WaitCount         int64   `json:"wait_count"`
	WaitDurationMs    float64 `json:"wait_duration_ms"`
	MaxIdleClosed     int64   `json:"max_idle_closed"`
	MaxLifetimeClosed int64   `json:"max_lifetime_closed"`
}

// systemRuntimeAPIHandler reports Go process facts. It reads no database rows;
// the only database access is the pool's own in-memory counters.
func systemRuntimeAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildSystemRuntime(st))
	}
}

func buildSystemRuntime(st *store.Store) systemRuntimeResponse {
	pool := systemRuntimeDBPool{}
	if st != nil {
		stats := st.PoolStats()
		pool = systemRuntimeDBPool{
			Driver:            st.DriverName(),
			MaxOpen:           stats.MaxOpenConnections,
			Open:              stats.OpenConnections,
			InUse:             stats.InUse,
			Idle:              stats.Idle,
			WaitCount:         stats.WaitCount,
			WaitDurationMs:    float64(stats.WaitDuration.Nanoseconds()) / float64(time.Millisecond),
			MaxIdleClosed:     stats.MaxIdleClosed,
			MaxLifetimeClosed: stats.MaxLifetimeClosed,
		}
	}

	now := time.Now()
	response := systemRuntimeResponse{
		GeneratedAt:   now.UTC(),
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		GOMAXPROCS:    runtime.GOMAXPROCS(0),
		Goroutines:    runtime.NumGoroutine(),
		UptimeSeconds: now.Sub(processStartedAt).Seconds(),
		Heap:          readSystemHeapMetrics(),
		GC:            readSystemGCMetrics(),
		DBPool:        pool,
	}
	if response.UptimeSeconds < 0 {
		response.UptimeSeconds = 0
	}
	return response
}

// systemMetricNames are the runtime/metrics series the page shows. They are
// read in one call so a request cannot observe a half-updated set.
var systemMetricNames = []string{
	"/gc/heap/allocs:bytes",
	"/memory/classes/heap/objects:bytes",
	"/gc/heap/objects:objects",
	"/memory/classes/total:bytes",
	"/memory/classes/heap/stacks:bytes",
	"/gc/cycles/total:gc-cycles",
	"/gc/heap/goal:bytes",
}

func readSystemMetrics() map[string]metrics.Value {
	samples := make([]metrics.Sample, len(systemMetricNames))
	for i, name := range systemMetricNames {
		samples[i].Name = name
	}
	metrics.Read(samples)
	values := make(map[string]metrics.Value, len(samples))
	for _, sample := range samples {
		if sample.Value.Kind() == metrics.KindBad {
			continue
		}
		values[sample.Name] = sample.Value
	}
	return values
}

func readSystemHeapMetrics() systemRuntimeHeap {
	values := readSystemMetrics()
	return systemRuntimeHeap{
		AllocBytes: metricUint64(values, "/gc/heap/allocs:bytes"),
		InUseBytes: metricUint64(values, "/memory/classes/heap/objects:bytes"),
		Objects:    metricUint64(values, "/gc/heap/objects:objects"),
		TotalSys:   metricUint64(values, "/memory/classes/total:bytes"),
		StackBytes: metricUint64(values, "/memory/classes/heap/stacks:bytes"),
	}
}

func readSystemGCMetrics() systemRuntimeGC {
	values := readSystemMetrics()
	out := systemRuntimeGC{
		Cycles:        metricUint64(values, "/gc/cycles/total:gc-cycles"),
		HeapGoalBytes: metricUint64(values, "/gc/heap/goal:bytes"),
	}
	// debug.ReadGCStats summarises the most recent pauses (a rolling history of
	// the last 256 collections), which is what "recent" has to mean here:
	// runtime/metrics only exposes the histogram since process start.
	var stats debug.GCStats
	stats.PauseQuantiles = make([]time.Duration, 5)
	debug.ReadGCStats(&stats)
	out.RecentPauses = len(stats.Pause)
	out.PauseTotalMs = float64(stats.PauseTotal.Nanoseconds()) / float64(time.Millisecond)
	if !stats.LastGC.IsZero() {
		out.LastGC = stats.LastGC.UTC()
	}
	if len(stats.PauseQuantiles) == 5 {
		out.RecentPauseMinMs = durationMs(stats.PauseQuantiles[0])
		out.RecentPauseP25Ms = durationMs(stats.PauseQuantiles[1])
		out.RecentPauseP50Ms = durationMs(stats.PauseQuantiles[2])
		out.RecentPauseP75Ms = durationMs(stats.PauseQuantiles[3])
		out.RecentPauseMaxMs = durationMs(stats.PauseQuantiles[4])
	}
	return out
}

func durationMs(value time.Duration) float64 {
	return float64(value.Nanoseconds()) / float64(time.Millisecond)
}

func metricUint64(values map[string]metrics.Value, name string) uint64 {
	value, ok := values[name]
	if !ok {
		return 0
	}
	switch value.Kind() {
	case metrics.KindUint64:
		return value.Uint64()
	case metrics.KindFloat64:
		return uint64(value.Float64())
	default:
		return 0
	}
}

// systemDatabaseAPIHandler reports the Postgres statistics views. A non-Postgres
// store answers with the structured unsupported payload and HTTP 200 so the
// page can degrade instead of erroring.
func systemDatabaseAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusOK, store.UnsupportedSystemDatabaseSnapshot("", "no trace store is configured"))
			return
		}
		snapshot, err := st.SystemDatabaseSnapshot(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}

// systemSlowQueriesAPIHandler serves the in-memory slow-statement ring. The
// ring is empty and `enabled` is false until debug.slow_query_threshold is set
// to a positive duration.
func systemSlowQueriesAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, st.SlowQuerySnapshot(systemSlowQueryLimit))
	}
}

// registerPprofHandlers mounts net/http/pprof on the Monitor mux.
//
// It is the Monitor mux rather than the proxy mux because the Monitor is where
// an operator already is while investigating - the slow reads whose cost a
// profile explains are the Monitor's own - and because the proxy mux serves the
// latency-critical data path, where an extra route is an extra way to be wrong.
// The switch is off by default; see debug.pprof_enabled.
func registerPprofHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}
