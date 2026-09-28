package legacymigrate

import (
	"runtime"
	"sync"
	"time"
)

// Progress is one progress observation emitted by a long-running phase.
type Progress struct {
	Phase     string `json:"phase"`
	Database  string `json:"database,omitempty"`
	Table     string `json:"table,omitempty"`
	Message   string `json:"message,omitempty"`
	Scanned   int64  `json:"scanned,omitempty"`
	Checked   int64  `json:"checked,omitempty"`
	Rewritten int64  `json:"rewritten,omitempty"`
	Current   int64  `json:"already_current,omitempty"`
	Other     int64  `json:"other_format,omitempty"`
	Copied    int64  `json:"copied,omitempty"`
	Duplicate int64  `json:"duplicate,omitempty"`
	Failed    int64  `json:"failed,omitempty"`
	Processed int64  `json:"processed,omitempty"`
	Total     int64  `json:"total,omitempty"`
}

// ProgressFunc receives progress observations. Implementations must be safe for
// concurrent use, or callers must wrap them.
type ProgressFunc func(Progress)

type progressReporter struct {
	fn       ProgressFunc
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

func newProgressReporter(fn ProgressFunc, interval time.Duration) *progressReporter {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &progressReporter{fn: fn, interval: interval}
}

// report emits a progress observation at most once per interval.
func (p *progressReporter) report(done int64, build func() Progress) {
	if p == nil || p.fn == nil {
		return
	}
	p.mu.Lock()
	now := time.Now()
	if done != 1 && now.Sub(p.last) < p.interval {
		p.mu.Unlock()
		return
	}
	p.last = now
	p.mu.Unlock()
	p.fn(build())
}

func defaultWorkers() int {
	return clampInt(runtime.NumCPU(), 4, 32)
}

// DefaultWorkers is the default worker count for cassette passes.
func DefaultWorkers() int { return defaultWorkers() }

// DefaultDatabaseWorkers is the default table-level concurrency for the
// SQLite to Postgres merge.
func DefaultDatabaseWorkers() int { return clampInt(runtime.NumCPU(), 2, 8) }

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
