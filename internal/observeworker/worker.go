package observeworker

import (
	"context"
	"errors"
	"sync/atomic"

	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/detector"
	"github.com/kingfs/Trajecta/internal/projectid"
	"github.com/kingfs/Trajecta/internal/resources"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/xirobserve"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// RuntimeSettings is the live queue policy shared by the worker and the Monitor
// API. The worker reads it once per tick, so a PATCH takes effect without a
// restart; the holder is an atomic pointer because the API and the loop are
// different goroutines.
type RuntimeSettings struct {
	value atomic.Pointer[store.AnalysisQueueSettings]
}

// NewRuntimeSettings wraps one policy snapshot.
func NewRuntimeSettings(settings store.AnalysisQueueSettings) *RuntimeSettings {
	holder := &RuntimeSettings{}
	holder.Set(settings)
	return holder
}

// Get returns the current policy, never nil.
func (r *RuntimeSettings) Get() store.AnalysisQueueSettings {
	if r == nil {
		return store.DefaultAnalysisQueueSettings()
	}
	if current := r.value.Load(); current != nil {
		return store.NormalizeAnalysisQueueSettings(*current)
	}
	return store.DefaultAnalysisQueueSettings()
}

// Set replaces the policy. The caller validates it first.
func (r *RuntimeSettings) Set(settings store.AnalysisQueueSettings) {
	if r == nil {
		return
	}
	normalized := store.NormalizeAnalysisQueueSettings(settings)
	r.value.Store(&normalized)
}

type Worker struct {
	store    *store.Store
	registry *observe.Registry
	// settings is the lane policy. A caller that does not supply one gets the
	// Options.Interval/BatchSize it always had, expressed as a policy, so the
	// existing construction sites and tests keep their behaviour.
	settings *RuntimeSettings
	// detectors is the published detection-plugin snapshot. When autoScan is
	// set, the parser runs the enabled observation detectors over the freshly
	// stored observation and persists their findings, so the findings surfaces
	// cover normal traffic instead of waiting for a manual reanalysis.
	detectors *detector.Runtime
	autoScan  bool
	// parserEngine selects the observation parser: "xir" (default) or
	// "observe". The XIR engine falls back to observe when it reports
	// ErrUnsupported (client entry exchanges).
	parserEngine string
}

type Options struct {
	// Interval and BatchSize are the legacy pacing knobs for the fresh lane.
	// They only apply when Settings is nil.
	Interval  time.Duration
	BatchSize int
	// Settings, when set, overrides Interval and BatchSize and is shared with the
	// Monitor so the queue can be tuned while it runs.
	Settings  *RuntimeSettings
	Registry  *observe.Registry
	Detectors *detector.Runtime
	// AutoScan runs the observation detectors after a successful parse. It is
	// normally cfg.Analysis.AutoScanFindings.
	AutoScan bool
	// ParserEngine selects the observation parser: "xir" (default) or
	// "observe" (cfg.Analysis.ParserEngine).
	ParserEngine string
}

func New(st *store.Store, opts Options) *Worker {
	settings := opts.Settings
	if settings == nil {
		policy := store.DefaultAnalysisQueueSettings()
		if opts.Interval > 0 {
			policy.ParseIntervalMs = int(opts.Interval / time.Millisecond)
		}
		if opts.BatchSize > 0 {
			policy.ParseBatchSize = opts.BatchSize
		}
		settings = NewRuntimeSettings(policy)
	}
	registry := opts.Registry
	if registry == nil {
		registry = observe.NewDefaultRegistry()
	}
	return &Worker{
		store:        st,
		registry:     registry,
		settings:     settings,
		detectors:    opts.Detectors,
		autoScan:     opts.AutoScan,
		parserEngine: normalizeParserEngine(opts.ParserEngine),
	}
}

// Settings exposes the holder so the Monitor can read and replace the policy.
func (w *Worker) Settings() *RuntimeSettings {
	if w == nil {
		return nil
	}
	return w.settings
}

// Loop pacing. The tick is short enough to honour a 200 ms interval and cheap
// enough to run continuously; the planner and the reclaim are background chores,
// not per-tick work.
const (
	workerTick      = 250 * time.Millisecond
	plannerInterval = 30 * time.Second
	reclaimInterval = 60 * time.Second
)

// staleJobAfter is how long a running parse job may sit before it is treated as
// abandoned. No cassette parse takes minutes, so a row past this belongs to a
// process that died without finishing it.
const staleJobAfter = 5 * time.Minute

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	w.reclaimStale(ctx)
	// One pass immediately, so a restart drains before the first tick.
	w.runOnce(ctx)
	lastFresh := time.Now()
	lastBackfill := time.Now()
	lastPlan := time.Now()
	lastReclaim := time.Now()
	ticker := time.NewTicker(workerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			settings := w.settings.Get()
			now := time.Now()
			if now.Sub(lastFresh) >= time.Duration(settings.ParseIntervalMs)*time.Millisecond {
				w.runLane(ctx, settings.ParseBatchSize, w.store.ClaimParseJobs, settings)
				lastFresh = now
			}
			if settings.BackfillEnabled && now.Sub(lastBackfill) >= time.Duration(settings.BackfillIntervalMs)*time.Millisecond {
				w.runLane(ctx, settings.BackfillBatchSize, w.store.ClaimBackfillParseJobs, settings)
				lastBackfill = now
			}
			if now.Sub(lastPlan) >= plannerInterval {
				w.planBackfill(ctx, settings)
				lastPlan = now
			}
			if now.Sub(lastReclaim) >= reclaimInterval {
				w.reclaimStale(ctx)
				lastReclaim = now
			}
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) {
	w.runOnce(ctx)
}

// reclaimStale returns jobs an interrupted process left in running to the
// queue, so a redeploy does not permanently strand the traces it was parsing.
func (w *Worker) reclaimStale(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	count, err := w.store.ReclaimStaleParseJobs(time.Now().UTC().Add(-staleJobAfter))
	if err != nil {
		slog.Warn("Reclaim interrupted parse jobs failed", "error", err)
		return
	}
	if count > 0 {
		slog.Info("Reclaimed interrupted parse jobs", "count", count)
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	settings := w.settings.Get()
	w.runLane(ctx, settings.ParseBatchSize, w.store.ClaimParseJobs, settings)
	if settings.BackfillEnabled {
		w.runLane(ctx, settings.BackfillBatchSize, w.store.ClaimBackfillParseJobs, settings)
	}
	w.planBackfill(ctx, settings)
}

// runLane claims and processes one lane's batch. The lanes are separate claims
// so a historical queue of any size can never delay a freshly recorded trace.
func (w *Worker) runLane(ctx context.Context, batchSize int, claim func(int) ([]store.ParseJobRecord, error), settings store.AnalysisQueueSettings) {
	if batchSize <= 0 {
		return
	}
	jobs, err := claim(batchSize)
	if err != nil {
		slog.Warn("Claim parse jobs failed", "error", err)
		return
	}
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := w.processJob(ctx, job, settings.ParseMaxAttempts); err != nil {
			slog.Warn("Parse job failed", "trace_id", job.TraceID, "job_id", job.ID, "error", err)
		}
	}
}

// planBackfill keeps the historical lane fed.
//
// A one-shot backfill has to be re-requested for every chunk and dies with the
// request that started it. This planner tops the lane up whenever it drops below
// the watermark and walks backwards through recorded_at, so historical
// attribution is a continuous background job that stops when it reaches the
// oldest trace. The cursor is what makes it terminate: a trace that declares no
// working directory never gets a project row, so "everything missing" would queue
// it forever. `backfill_enabled=false` pauses the sweep without touching the
// fresh lane, and the panel's reset clears the cursor to walk history again.
func (w *Worker) planBackfill(ctx context.Context, settings store.AnalysisQueueSettings) {
	if !settings.BackfillEnabled || settings.BackfillChunkSize <= 0 {
		return
	}
	state := w.store.LoadBackfillState(ctx)
	if !state.CompletedAt.IsZero() {
		return
	}
	// Queued and running both count as "already in the lane". Counting only the
	// queued ones would let the planner stack another chunk on top of a batch the
	// worker is still draining.
	queuedCount, err := w.store.CountParseJobs("queued", store.ParsePriorityBackfill)
	if err != nil {
		slog.Warn("Count backfill queue failed", "error", err)
		return
	}
	runningCount, err := w.store.CountParseJobs("running", store.ParsePriorityBackfill)
	if err != nil {
		slog.Warn("Count backfill queue failed", "error", err)
		return
	}
	pending := queuedCount + runningCount
	// The watermark is "top the lane up below this depth". One means "only when
	// the lane is empty"; there is no zero, because a disabled gate would queue a
	// whole chunk on every planner pass.
	if pending >= settings.BackfillLowWatermark {
		return
	}
	through := state.ProjectThrough
	if through.IsZero() {
		through = time.Now().UTC()
	}
	plan, err := w.store.EnqueueProjectBackfillThrough(through, state.ProjectThroughTrace, settings.BackfillChunkSize)
	if err != nil {
		slog.Warn("Plan project backfill failed", "error", err)
		return
	}
	if plan.Candidates == 0 {
		// Nothing older: the sweep is complete until someone resets it.
		state.ProjectThrough = through
		state.CompletedAt = time.Now().UTC()
		if err := w.store.SaveBackfillState(ctx, state); err != nil {
			slog.Warn("Save backfill state failed", "error", err)
			return
		}
		slog.Info("Project backfill complete", "through", through)
		return
	}
	if plan.Queued == 0 {
		// There was work but none of it could be queued. Do not move the cursor:
		// the next pass has to retry the same window rather than skip it.
		slog.Warn("Project backfill queued nothing", "candidates", plan.Candidates)
		return
	}
	state.ProjectThrough = plan.Oldest
	state.ProjectThroughTrace = plan.OldestTraceID
	state.CompletedAt = time.Time{}
	if err := w.store.SaveBackfillState(ctx, state); err != nil {
		slog.Warn("Save backfill state failed", "error", err)
		return
	}
	slog.Info("Queued project backfill", "traces", plan.Queued, "candidates", plan.Candidates, "pending", pending, "through", plan.Oldest)
}

// processJob parses one already-claimed job. The claim moved the row to running
// and incremented its attempt count, so there is no separate mark-running step.
//
// A failure is requeued while the job still has attempts left: a corrupt
// cassette fails deterministically, but a database hiccup does not, and one
// transient error must not bury a trace for good.
func (w *Worker) processJob(ctx context.Context, job store.ParseJobRecord, maxAttempts int) error {
	fail := func(err error) error {
		if _, markErr := w.store.RequeueOrFailParseJob(job.ID, maxAttempts, err.Error()); markErr != nil {
			slog.Warn("Record parse job failure failed", "job_id", job.ID, "error", markErr)
		}
		return err
	}
	obs, err := ReparseTraceWithEngine(ctx, w.store, w.registry, w.parserEngine, job.TraceID)
	if err != nil {
		return fail(err)
	}
	if err := w.store.SaveObservation(obs); err != nil {
		return fail(err)
	}
	// The derived indexes are part of parsing, not a detector: the resource
	// index answers which paths and URLs the exchange carried, the project index
	// answers which project the client said it was working in, and the trace
	// detail and list pages read both. Saving them here is what makes them
	// automatic - an audit that only ran behind `analyze scan` would be empty
	// for every normally recorded trace.
	if err := PersistTraceDerivations(w.store, job.TraceID, obs); err != nil {
		return fail(err)
	}
	w.scanFindings(ctx, job.TraceID, obs)
	if err := w.store.MarkParseJobDone(job.ID); err != nil {
		return err
	}
	return nil
}

// scanFindings runs the observation detectors over a freshly parsed exchange.
// A failure here is logged rather than failing the parse job: the observation
// and resource index are already stored, and a reanalysis can recompute the
// findings without re-reading the cassette from a failed job.
func (w *Worker) scanFindings(ctx context.Context, traceID string, obs observe.TraceObservation) {
	if !w.autoScan || w.detectors == nil {
		return
	}
	snap := w.detectors.Snapshot()
	if snap == nil {
		return
	}
	findings, err := snap.Runner().Analyze(ctx, obs)
	if err != nil {
		slog.Warn("Auto findings scan failed", "trace_id", traceID, "error", err)
		return
	}
	if len(findings) == 0 {
		return
	}
	if err := w.store.SaveFindings(traceID, findings); err != nil {
		slog.Warn("Save auto findings failed", "trace_id", traceID, "error", err)
	}
}

// PersistTraceDerivations writes the indexes derived from one parsed exchange:
// the resources it named and the project it declared. Every writer that turns a
// cassette into an observation calls this - the parse worker and the
// reanalysis service - so a reparse cannot leave one index ahead of the other.
func PersistTraceDerivations(st *store.Store, traceID string, obs observe.TraceObservation) error {
	if err := st.SaveTraceResources(traceID, StoreResourceAccesses(obs)); err != nil {
		return err
	}
	return PersistTraceProject(st, traceID, obs)
}

// PersistTraceProject records the project one trace declared and refreshes its
// session's rollup.
//
// A trace that declares nothing still replaces its row with nothing: the parse
// may be a reparse of an older cassette, and a declaration the extractor no
// longer finds must not survive as a stale project.
func PersistTraceProject(st *store.Store, traceID string, obs observe.TraceObservation) error {
	sessionID, err := st.SessionIDForTrace(traceID)
	if err != nil {
		return err
	}
	outcome := projectid.Extract(obs)
	var record *store.ProjectRecord
	switch {
	case outcome.Declaration != nil:
		record = &store.ProjectRecord{
			Root:          outcome.Declaration.Root,
			RootKey:       outcome.Declaration.Key,
			Source:        string(outcome.Declaration.Source),
			MultipleRoots: outcome.Declaration.MultipleRoots,
		}
	case len(outcome.Rejections) > 0:
		// Keep the reason, not just the absence of a project: a harness that
		// changed its prompt format must be distinguishable from traffic that
		// declares nothing at all.
		rejection := outcome.Rejections[0]
		record = &store.ProjectRecord{
			Rejected: true,
			Source:   string(rejection.Source),
			Reason:   string(rejection.Reason),
			Evidence: rejection.Evidence,
		}
	}
	if err := st.SaveTraceProject(traceID, sessionID, record); err != nil {
		return err
	}
	return st.RebuildSessionProject(sessionID)
}

// StoreResourceAccesses adapts the extracted values to the write shape without
// making the store depend on the extractor.
func StoreResourceAccesses(obs observe.TraceObservation) []store.ResourceAccess {
	extracted := resources.Extract(obs)
	accesses := make([]store.ResourceAccess, 0, len(extracted))
	for _, item := range extracted {
		accesses = append(accesses, store.ResourceAccess{
			Kind:     item.Kind,
			Value:    item.Value,
			Count:    item.Count,
			Position: item.Position,
		})
	}
	return accesses
}

func ReparseTrace(ctx context.Context, st *store.Store, registry *observe.Registry, traceID string) (observe.TraceObservation, error) {
	// Empty selects the package default (xir); callers that need the rollback
	// engine pass it explicitly through ReparseTraceWithEngine.
	return ReparseTraceWithEngine(ctx, st, registry, "", traceID)
}

// ReparseTraceWithEngine parses one cassette with the selected observation
// engine. The XIR engine returns ErrUnsupported for the exchanges it does not
// model yet (streamed responses, client entry exchanges); those fall back to
// the observe registry instead of failing the parse job.
func ReparseTraceWithEngine(ctx context.Context, st *store.Store, registry *observe.Registry, engine, traceID string) (observe.TraceObservation, error) {
	if st == nil {
		return observe.TraceObservation{}, errors.New("trace store is nil")
	}
	if registry == nil {
		registry = observe.NewDefaultRegistry()
	}
	entry, err := st.GetByID(traceID)
	if err != nil {
		return observe.TraceObservation{}, err
	}
	content, err := os.ReadFile(entry.LogPath)
	if err != nil {
		return observe.TraceObservation{}, err
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		return observe.TraceObservation{}, err
	}
	_, reqBody, _, resBody := recordfile.ExtractSections(content, parsed)
	exchange := parsed.Header.Meta
	if indexed, err := st.GetTraceExchangeMetadata(entry.ID); err == nil {
		mergeExchangeMetadata(&exchange, indexed)
	}
	applyExchangeFallbacks(&exchange, entry.LogPath)
	input := observe.ParseInput{
		TraceID:          entry.ID,
		CassettePath:     entry.LogPath,
		Header:           parsed.Header,
		Events:           parsed.Events,
		RequestBody:      reqBody,
		ResponseBody:     resBody,
		IsStream:         parsed.Header.Layout.IsStream,
		ExchangeKind:     exchange.ExchangeKind,
		ExchangeRole:     exchange.ExchangeRole,
		ParentExchangeID: exchange.ParentExchangeID,
		SequenceIndex:    exchange.SequenceIndex,
		RequestAuditID:   exchange.RequestAuditID,
		ResponseID:       exchange.ResponseID,
	}
	if normalizeParserEngine(engine) == "xir" {
		obs, xirErr := xirobserve.Parse(ctx, input)
		if xirErr == nil {
			return obs, nil
		}
		if !errors.Is(xirErr, xirobserve.ErrUnsupported) {
			return obs, xirErr
		}
		// Unsupported exchange: keep the observe parser as the fallback so a
		// stream or entry exchange is still observed.
	}
	obs, err := registry.Parse(ctx, input)
	if errors.Is(err, observe.ErrNoParser) {
		// The exchange is well-formed traffic for an operation this build does not
		// parse, for example /v1/embeddings. Recording it as a failed observation
		// would fail the parse job and every later reanalysis of the trace, for a
		// payload nothing is wrong with; record it as unsupported instead.
		return observe.UnsupportedObservation(input), nil
	}
	return obs, err
}

func normalizeParserEngine(engine string) string {
	if strings.EqualFold(strings.TrimSpace(engine), "observe") {
		return "observe"
	}
	return "xir"
}

func mergeExchangeMetadata(dst *recordfile.MetaData, src recordfile.MetaData) {
	if dst.ExchangeKind == "" {
		dst.ExchangeKind = src.ExchangeKind
	}
	if dst.ExchangeRole == "" {
		dst.ExchangeRole = src.ExchangeRole
	}
	if dst.ParentExchangeID == "" {
		dst.ParentExchangeID = src.ParentExchangeID
	}
	if dst.SequenceIndex == 0 {
		dst.SequenceIndex = src.SequenceIndex
	}
	if dst.RequestAuditID == "" {
		dst.RequestAuditID = src.RequestAuditID
	}
	if dst.ResponseID == "" {
		dst.ResponseID = src.ResponseID
	}
}

func applyExchangeFallbacks(meta *recordfile.MetaData, cassettePath string) {
	if meta.ExchangeKind == "" && isEntryExchangeHint(*meta, cassettePath) {
		meta.ExchangeKind = "entry"
	}
	if meta.ExchangeKind == "" && isModelExchangeHint(*meta) {
		meta.ExchangeKind = "model"
	}
	if meta.ExchangeRole == "" {
		switch meta.ExchangeKind {
		case "entry":
			meta.ExchangeRole = "client_request"
		case "model":
			meta.ExchangeRole = "primary_model_call"
		}
	}
}

func isEntryExchangeHint(meta recordfile.MetaData, cassettePath string) bool {
	if meta.ExchangeRole == "client_request" {
		return true
	}
	lowerPath := strings.ToLower(filepath.ToSlash(cassettePath))
	if strings.Contains(lowerPath, "/entry/") || strings.Contains(lowerPath, "_entry") || strings.Contains(lowerPath, "-entry") {
		return true
	}
	return false
}

func isModelExchangeHint(meta recordfile.MetaData) bool {
	return meta.Model != "" || meta.Provider != "" || meta.SelectedUpstreamID != "" || meta.SelectedUpstreamBaseURL != ""
}
