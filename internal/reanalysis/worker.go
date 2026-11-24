package reanalysis

import (
	"context"
	"log/slog"
	"time"

	"github.com/kingfs/Trajecta/internal/detector"
	"github.com/kingfs/Trajecta/internal/store"
)

type Worker struct {
	store     *store.Store
	service   *Service
	interval  time.Duration
	batchSize int
	// settings, when set, overrides interval and batchSize with the live queue
	// policy shared with the Monitor. The reanalysis queue used to be the one
	// queue nobody could tune while the parse queue next to it was tunable.
	settings func() (time.Duration, int)
}

type WorkerOptions struct {
	Interval  time.Duration
	BatchSize int
	Service   *Service
	// DetectorRuntime supplies the plugin-driven observation runner when no
	// explicit Service is given.
	DetectorRuntime *detector.Runtime
	// ParserEngine is forwarded to the service built from these options.
	ParserEngine string
	// Settings, when set, supplies the live interval and batch size. It is read
	// before every pass, so a change applies without a restart.
	Settings func() (time.Duration, int)
}

func NewWorker(st *store.Store, opts WorkerOptions) *Worker {
	interval := opts.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 5
	}
	service := opts.Service
	if service == nil {
		service = New(st, Options{DetectorRuntime: opts.DetectorRuntime, ParserEngine: opts.ParserEngine})
	}
	return &Worker{
		store:     st,
		service:   service,
		interval:  interval,
		batchSize: batchSize,
		settings:  opts.Settings,
	}
}

// pacing returns the live interval and batch size.
func (w *Worker) pacing() (time.Duration, int) {
	interval, batchSize := w.interval, w.batchSize
	if w.settings != nil {
		if liveInterval, liveBatch := w.settings(); liveInterval > 0 && liveBatch > 0 {
			return liveInterval, liveBatch
		}
	}
	return interval, batchSize
}

// staleJobAfter matches the parse worker: a job running past it belongs to a
// process that died, and a reanalysis is safe to repeat.
const staleJobAfter = 5 * time.Minute

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.store == nil || w.service == nil {
		return
	}
	if count, err := w.store.ReclaimStaleAnalysisJobs(time.Now().UTC().Add(-staleJobAfter)); err != nil {
		slog.Warn("Reclaim interrupted analysis jobs failed", "error", err)
	} else if count > 0 {
		slog.Info("Reclaimed interrupted analysis jobs", "count", count)
	}
	w.runOnce(ctx)
	// The tick is short and the pass is due-time driven, so a policy change is
	// honoured on the next tick instead of at the next full interval.
	lastRun := time.Now()
	const tick = 250 * time.Millisecond
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			interval, _ := w.pacing()
			if now.Sub(lastRun) < interval {
				continue
			}
			w.runOnce(ctx)
			lastRun = now
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) {
	w.runOnce(ctx)
}

func (w *Worker) runOnce(ctx context.Context) {
	_, batchSize := w.pacing()
	jobs, err := w.store.ClaimAnalysisJobsForWorker(batchSize)
	if err != nil {
		slog.Warn("Claim analysis jobs failed", "error", err)
		return
	}
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := w.service.ExecuteJob(ctx, job); err != nil {
			slog.Warn("Analysis job failed", "job_id", job.ID, "job_type", job.JobType, "target_id", job.TargetID, "error", err)
		}
	}
}
