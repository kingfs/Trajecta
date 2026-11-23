package observeworker

import (
	"context"
	"errors"

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

type Worker struct {
	store     *store.Store
	registry  *observe.Registry
	interval  time.Duration
	batchSize int
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
	Interval  time.Duration
	BatchSize int
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
	interval := opts.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}
	registry := opts.Registry
	if registry == nil {
		registry = observe.NewDefaultRegistry()
	}
	return &Worker{
		store:        st,
		registry:     registry,
		interval:     interval,
		batchSize:    batchSize,
		detectors:    opts.Detectors,
		autoScan:     opts.AutoScan,
		parserEngine: normalizeParserEngine(opts.ParserEngine),
	}
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	w.runOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) {
	w.runOnce(ctx)
}

func (w *Worker) runOnce(ctx context.Context) {
	jobs, err := w.store.ClaimParseJobs(w.batchSize)
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
		if err := w.processJob(ctx, job); err != nil {
			slog.Warn("Parse job failed", "trace_id", job.TraceID, "job_id", job.ID, "error", err)
		}
	}
}

// processJob parses one already-claimed job. The claim moved the row to running
// and incremented its attempt count, so there is no separate mark-running step.
func (w *Worker) processJob(ctx context.Context, job store.ParseJobRecord) error {
	obs, err := ReparseTraceWithEngine(ctx, w.store, w.registry, w.parserEngine, job.TraceID)
	if err != nil {
		_ = w.store.MarkParseJobFailed(job.ID, err.Error())
		return err
	}
	if err := w.store.SaveObservation(obs); err != nil {
		_ = w.store.MarkParseJobFailed(job.ID, err.Error())
		return err
	}
	// The derived indexes are part of parsing, not a detector: the resource
	// index answers which paths and URLs the exchange carried, the project index
	// answers which project the client said it was working in, and the trace
	// detail and list pages read both. Saving them here is what makes them
	// automatic - an audit that only ran behind `analyze scan` would be empty
	// for every normally recorded trace.
	if err := PersistTraceDerivations(w.store, job.TraceID, obs); err != nil {
		_ = w.store.MarkParseJobFailed(job.ID, err.Error())
		return err
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
