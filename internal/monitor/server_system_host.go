package monitor

import (
	"net/http"
	"sync"
	"time"
)

// systemHostMinInterval is the shortest interval between two samples that may
// produce a rate. Two requests arriving almost together describe nearly no
// elapsed time, so dividing the counter difference by it would turn a handful
// of ticks into a wild percentage; reusing the previous answer is both cheaper
// and more honest than reporting that spike.
const systemHostMinInterval = 500 * time.Millisecond

// systemHostMaxGap is how stale a baseline may be before it stops describing
// the current load. A dashboard tab left open overnight would otherwise compare
// this minute's counters against yesterday's, so a sample older than this is
// dropped: the response reports zeros for the delta-derived fields and the new
// reading becomes the baseline.
const systemHostMaxGap = 5 * time.Minute

// systemHostDiskLimit caps the disk list so a machine with many mounts cannot
// make the page render hundreds of rows. It matches what the panel shows.
const systemHostDiskLimit = 12

// systemHostResponse is the payload of GET /api/system/host. It is deliberately
// fully populated and never fails: the system page renders every panel, so a
// metric that cannot be collected is a zero plus one warning line rather than a
// missing key or an error status.
type systemHostResponse struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Unsupported bool              `json:"unsupported"`
	Reason      string            `json:"reason"`
	Host        systemHostInfo    `json:"host"`
	CPU         systemHostCPU     `json:"cpu"`
	Memory      systemHostMemory  `json:"memory"`
	Disk        []systemHostDisk  `json:"disk"`
	Network     systemHostNetwork `json:"network"`
	Process     systemHostProcess `json:"process"`
	Warnings    []string          `json:"warnings"`
	History     []systemHostPoint `json:"history"`
}

// systemHostPoint is one reading in the trend the page draws. It is a much
// smaller shape than the sample it comes from: the chart needs the timestamp,
// the percentages and the throughput it plots, and copying the counters would
// put a per-core array and every interface in each point of a sixty-point
// series. The network rates are the aggregate, so the line keeps its shape when
// an interface appears or disappears while the tab is open.
type systemHostPoint struct {
	At            time.Time `json:"at"`
	CPUPercent    float64   `json:"cpu_percent"`
	MemoryPercent float64   `json:"memory_percent"`
	Load1         float64   `json:"load1"`
	RxBytesPerSec float64   `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64   `json:"tx_bytes_per_sec"`
}

// systemHostHistoryLimit is how many readings the trend keeps. The server
// samples every five seconds while a tab is watching the system page, so this
// is the last five minutes - a window wide enough to see a spike and short
// enough that the oldest point still describes this machine's current load.
const systemHostHistoryLimit = 60

// systemHostInfo identifies the machine and how long it has been up. The
// uptime is the kernel's, not this process's: it pairs with the load average
// next to it on the page.
type systemHostInfo struct {
	Hostname      string  `json:"hostname"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	Kernel        string  `json:"kernel"`
}

// systemHostCPU reports utilisation. The percentages are deltas between two
// samples, so the first call after start leaves them at zero; the load average
// and the core count are instantaneous and always present.
type systemHostCPU struct {
	Cores          int       `json:"cores"`
	UsagePercent   float64   `json:"usage_percent"`
	UserPercent    float64   `json:"user_percent"`
	SystemPercent  float64   `json:"system_percent"`
	IowaitPercent  float64   `json:"iowait_percent"`
	IdlePercent    float64   `json:"idle_percent"`
	Load1          float64   `json:"load1"`
	Load5          float64   `json:"load5"`
	Load15         float64   `json:"load15"`
	PerCorePercent []float64 `json:"per_core_percent"`
}

// systemHostMemory reports RAM and swap. Used memory is derived from what the
// kernel can hand out (MemAvailable), not from MemFree, because page cache is
// reclaimable and would otherwise look like pressure.
type systemHostMemory struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
	CachedBytes    uint64  `json:"cached_bytes"`
	SwapTotalBytes uint64  `json:"swap_total_bytes"`
	SwapUsedBytes  uint64  `json:"swap_used_bytes"`
	SwapUsedPct    float64 `json:"swap_used_percent"`
}

// systemHostDisk is one mounted local filesystem. Used excludes the blocks the
// kernel reserves for root, which is what an operator sees with df.
type systemHostDisk struct {
	Mount          string  `json:"mount"`
	Filesystem     string  `json:"filesystem"`
	Device         string  `json:"device"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

// systemHostNetwork lists the interfaces plus the aggregate throughput. The
// aggregate deliberately omits loopback so local chatter cannot dominate it.
type systemHostNetwork struct {
	Interfaces    []systemHostInterface `json:"interfaces"`
	RxBytesPerSec float64               `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64               `json:"tx_bytes_per_sec"`
}

// systemHostInterface is one interface's cumulative counters and the rate
// derived from them. The counters are totals since boot, so the page can render
// a stable number even while the rates are still being primed.
type systemHostInterface struct {
	Name          string  `json:"name"`
	Loopback      bool    `json:"loopback"`
	RxBytes       uint64  `json:"rx_bytes"`
	TxBytes       uint64  `json:"tx_bytes"`
	RxBytesPerSec float64 `json:"rx_bytes_per_sec"`
	TxBytesPerSec float64 `json:"tx_bytes_per_sec"`
}

// systemHostProcess describes this server process, which is the only process
// the monitor may speak about with confidence. The JSON name vs_z_bytes keeps
// the field aligned with the panel that renders it.
type systemHostProcess struct {
	PID        int       `json:"pid"`
	RSSBytes   uint64    `json:"rss_bytes"`
	VSZBytes   uint64    `json:"vs_z_bytes"`
	CPUPercent float64   `json:"cpu_percent"`
	Threads    int       `json:"threads"`
	OpenFDs    int       `json:"open_fds"`
	StartedAt  time.Time `json:"started_at"`
}

// systemHostSample is one raw counter set. Rates are the difference between two
// of these, so the sampler only has to remember the counters and when they were
// read; keeping raw ticks rather than percentages means a dropped sample can
// never be mistaken for a real interval.
type systemHostSample struct {
	at           time.Time
	cpuTicks     systemHostCPUTicks
	perCoreTicks []systemHostCPUTicks
	rxBytes      uint64
	txBytes      uint64
	ifaceRx      map[string]uint64
	ifaceTx      map[string]uint64
	processTicks uint64
}

// systemHostCPUTicks mirrors the first eight /proc/stat columns. They are kept
// as separate counters because the page reports user, system, iowait and idle
// separately; only their sum is a total.
type systemHostCPUTicks struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
	steal   uint64
}

func (t systemHostCPUTicks) total() uint64 {
	return t.user + t.nice + t.system + t.idle + t.iowait + t.irq + t.softirq + t.steal
}

// tickDelta subtracts two counters without ever going negative. A counter reset
// (in practice, a reboot) would otherwise wrap around and report an absurd
// rate; contributing nothing is the only defensible answer.
func tickDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

// systemHostSampler keeps the process-wide baseline and the last answer. The
// Monitor is a single process, so one cache serves every request and the mutex
// is the only concurrency the sampling rules need.
type systemHostSampler struct {
	mu        sync.Mutex
	prev      *systemHostSample
	prevAt    time.Time
	cached    systemHostResponse
	hasCached bool

	// history is the ring the trend is read from, oldest first. It is filled by
	// whoever samples - the page's own request or the realtime hub's timer -
	// so the chart keeps filling while the tab is open and stops with the last
	// reader.
	history []systemHostPoint
}

// systemHostSamples is the shared sampler instance behind the handler.
var systemHostSamples systemHostSampler

// systemHostAPIHandler reports host-level resource metrics. It is admin-only
// because hostnames, mount points and file descriptors describe the machine,
// not the application.
func systemHostAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildSystemHost(time.Now()))
	}
}

// buildSystemHost takes one sample through the shared sampler. It is split out
// from the handler so the sampling rules can be tested without an HTTP round
// trip and a real clock.
func buildSystemHost(now time.Time) systemHostResponse {
	return systemHostSamples.read(now)
}

func (s *systemHostSampler) read(now time.Time) systemHostResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Two requests inside the minimum interval would divide by an interval too
	// short to be meaningful, so the previous rates are reused and only the
	// timestamp is refreshed. The trend is part of the answer here too: a reader
	// refreshing quickly must not be told there is no history.
	if s.hasCached && now.Sub(s.prevAt) < systemHostMinInterval {
		reused := s.cached
		reused.GeneratedAt = now.UTC()
		reused.History = s.historySliceLocked()
		return reused
	}

	prev := s.prev
	if prev != nil && now.Sub(s.prevAt) > systemHostMaxGap {
		prev = nil
	}
	response, sample := readSystemHostMetrics(now, prev)

	// An unsupported platform has no counters to remember, so only real
	// readings become the next baseline.
	if !response.Unsupported {
		s.prev = &sample
		s.prevAt = now
		s.appendHistoryLocked(response)
	}
	response.History = s.historySliceLocked()
	s.cached = response
	s.hasCached = true
	return response
}

// appendHistoryLocked records one reading, dropping the oldest when the ring is
// full.
func (s *systemHostSampler) appendHistoryLocked(response systemHostResponse) {
	s.history = append(s.history, systemHostPoint{
		At:            response.GeneratedAt,
		CPUPercent:    response.CPU.UsagePercent,
		MemoryPercent: response.Memory.UsedPercent,
		Load1:         response.CPU.Load1,
		RxBytesPerSec: response.Network.RxBytesPerSec,
		TxBytesPerSec: response.Network.TxBytesPerSec,
	})
	if len(s.history) > systemHostHistoryLimit {
		s.history = append([]systemHostPoint(nil), s.history[len(s.history)-systemHostHistoryLimit:]...)
	}
}

// historySliceLocked returns a copy, so a reader cannot append to the sampler's
// own backing array and a response cannot be mutated after it is handed out.
func (s *systemHostSampler) historySliceLocked() []systemHostPoint {
	if len(s.history) == 0 {
		return []systemHostPoint{}
	}
	out := make([]systemHostPoint, len(s.history))
	copy(out, s.history)
	return out
}
