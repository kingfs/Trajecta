package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kingfs/Trajecta/internal/observeworker"
	"github.com/kingfs/Trajecta/internal/store"
)

// analysisQueueDepth is what the queue panel shows next to the controls.
//
// Only counts that are cheap are taken: the two queued lanes, the running
// claims and the failed rows. A "total remaining" figure would have to scan the
// log index, and the projects page already answers "how much is attributed".
type analysisQueueDepth struct {
	FreshQueued    int `json:"fresh_queued"`
	BackfillQueued int `json:"backfill_queued"`
	Running        int `json:"running"`
	Failed         int `json:"failed"`
	Attributed     int `json:"attributed"`
}

// analysisQueueView is the settings row plus the live depth and whether this
// process has a worker attached to apply a change.
type analysisQueueView struct {
	store.AnalysisQueueSettings
	Queue           analysisQueueDepth   `json:"queue"`
	Backfill        store.BackfillCursor `json:"backfill"`
	RuntimeAttached bool                 `json:"runtime_attached"`
}

type analysisQueuePatch struct {
	ParseBatchSize       *int  `json:"parse_batch_size,omitempty"`
	ParseIntervalMs      *int  `json:"parse_interval_ms,omitempty"`
	ParseMaxAttempts     *int  `json:"parse_max_attempts,omitempty"`
	BackfillEnabled      *bool `json:"backfill_enabled,omitempty"`
	BackfillBatchSize    *int  `json:"backfill_batch_size,omitempty"`
	BackfillIntervalMs   *int  `json:"backfill_interval_ms,omitempty"`
	BackfillChunkSize    *int  `json:"backfill_chunk_size,omitempty"`
	BackfillLowWatermark *int  `json:"backfill_low_watermark,omitempty"`
}

// analysisQueueAPIHandler reads and writes the two-lane parse policy.
//
// The policy lives in `app_settings` rather than YAML because the right rate
// depends on the deployment's disk and database headroom at the moment: an
// operator who starts a large historical backfill needs to slow it down without
// a redeploy, and the next operator needs the same knob to speed it back up.
// A PATCH applies to the running worker immediately when one is attached; the
// stored row is what a restart picks up.
func analysisQueueAPIHandler(st *store.Store, settings *observeworker.RuntimeSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/reset-backfill") {
			if err := st.ResetBackfillState(r.Context()); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, analysisQueueView{
				AnalysisQueueSettings: st.LoadAnalysisQueueSettings(r.Context()),
				Queue:                 analysisQueueDepthFor(r.Context(), st),
				Backfill:              store.BackfillCursor{},
				RuntimeAttached:       settings != nil,
			})
			return
		}
		switch r.Method {
		case http.MethodGet:
			current := st.LoadAnalysisQueueSettings(r.Context())
			if settings != nil {
				current = settings.Get()
			}
			writeJSON(w, http.StatusOK, analysisQueueView{
				AnalysisQueueSettings: current,
				Queue:                 analysisQueueDepthFor(r.Context(), st),
				Backfill:              st.LoadBackfillState(r.Context()),
				RuntimeAttached:       settings != nil,
			})
		case http.MethodPatch:
			current := st.LoadAnalysisQueueSettings(r.Context())
			if settings != nil {
				current = settings.Get()
			}
			var patch analysisQueuePatch
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid analysis queue payload"})
				return
			}
			current = mergeAnalysisQueuePatch(current, patch)
			if message := store.ValidateAnalysisQueueSettings(current); message != "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
				return
			}
			if err := st.SaveAnalysisQueueSettings(r.Context(), current); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			saved := st.LoadAnalysisQueueSettings(r.Context())
			// Publish before answering: a control that claims to have taken
			// effect must already be pacing the loop when the response lands.
			if settings != nil {
				settings.Set(saved)
			}
			writeJSON(w, http.StatusOK, analysisQueueView{
				AnalysisQueueSettings: saved,
				Queue:                 analysisQueueDepthFor(r.Context(), st),
				Backfill:              st.LoadBackfillState(r.Context()),
				RuntimeAttached:       settings != nil,
			})
		default:
			http.NotFound(w, r)
		}
	}
}

func mergeAnalysisQueuePatch(current store.AnalysisQueueSettings, patch analysisQueuePatch) store.AnalysisQueueSettings {
	if patch.ParseBatchSize != nil {
		current.ParseBatchSize = *patch.ParseBatchSize
	}
	if patch.ParseIntervalMs != nil {
		current.ParseIntervalMs = *patch.ParseIntervalMs
	}
	if patch.ParseMaxAttempts != nil {
		current.ParseMaxAttempts = *patch.ParseMaxAttempts
	}
	if patch.BackfillEnabled != nil {
		current.BackfillEnabled = *patch.BackfillEnabled
	}
	if patch.BackfillBatchSize != nil {
		current.BackfillBatchSize = *patch.BackfillBatchSize
	}
	if patch.BackfillIntervalMs != nil {
		current.BackfillIntervalMs = *patch.BackfillIntervalMs
	}
	if patch.BackfillChunkSize != nil {
		current.BackfillChunkSize = *patch.BackfillChunkSize
	}
	if patch.BackfillLowWatermark != nil {
		current.BackfillLowWatermark = *patch.BackfillLowWatermark
	}
	return current
}

// analysisQueueDepthFor collects the counters, degrading to zero rather than
// failing the whole panel when one query is unhappy.
func analysisQueueDepthFor(ctx context.Context, st *store.Store) analysisQueueDepth {
	var depth analysisQueueDepth
	if count, err := st.CountParseJobs("queued", store.ParsePriorityFresh); err == nil {
		depth.FreshQueued = count
	}
	if count, err := st.CountParseJobs("queued", store.ParsePriorityBackfill); err == nil {
		depth.BackfillQueued = count
	}
	if count, err := st.CountParseJobs("running", ""); err == nil {
		depth.Running = count
	}
	if count, err := st.CountParseJobs("failed", ""); err == nil {
		depth.Failed = count
	}
	if count, err := st.CountDeclaredTraceProjects(); err == nil {
		depth.Attributed = count
	}
	return depth
}
