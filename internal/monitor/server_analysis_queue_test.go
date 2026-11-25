package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/observeworker"
	"github.com/kingfs/Trajecta/internal/store"
)

// The queue panel is the only place an operator can pace the two parse lanes.
// What it writes has to (a) validate, (b) persist for the next restart and
// (c) reach the running worker immediately.

func analysisQueueTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := newTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestAnalysisQueueAPIReadsDefaultsAndAppliesPatch(t *testing.T) {
	st := analysisQueueTestStore(t)
	holder := observeworker.NewRuntimeSettings(store.DefaultAnalysisQueueSettings())
	handler := analysisQueueAPIHandler(st, holder)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/settings/analysis-queue", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var view struct {
		ParseBatchSize  int  `json:"parse_batch_size"`
		ParseIntervalMs int  `json:"parse_interval_ms"`
		BackfillEnabled bool `json:"backfill_enabled"`
		RuntimeAttached bool `json:"runtime_attached"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.ParseBatchSize != 10 || view.ParseIntervalMs != 5000 || !view.BackfillEnabled || !view.RuntimeAttached {
		t.Fatalf("view = %+v, want the defaults with a worker attached", view)
	}

	body := `{"parse_batch_size":25,"parse_interval_ms":1000,"backfill_enabled":false,"backfill_chunk_size":10,"backfill_scope":"all","analysis_batch_size":9,"analysis_interval_ms":2000}`
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/api/settings/analysis-queue", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	// Runtime first: a control that claims to have taken effect must already be
	// pacing the loop.
	active := holder.Get()
	if active.ParseBatchSize != 25 || active.ParseIntervalMs != 1000 || active.BackfillEnabled {
		t.Fatalf("runtime settings = %+v", active)
	}
	// And persisted for the next restart.
	stored := st.LoadAnalysisQueueSettings(t.Context())
	if stored.ParseBatchSize != 25 || stored.BackfillEnabled {
		t.Fatalf("stored settings = %+v", stored)
	}
	if stored.BackfillChunkSize != 10 {
		t.Fatalf("backfill chunk = %d, want the patched value", stored.BackfillChunkSize)
	}
	if stored.BackfillScope != store.BackfillScopeAll || stored.AnalysisBatchSize != 9 || stored.AnalysisIntervalMs != 2000 {
		t.Fatalf("stored = %+v, want scope and analysis pacing patched", stored)
	}
}

func TestAnalysisQueueAPIRejectsAnIntervalBelowTheFloor(t *testing.T) {
	st := analysisQueueTestStore(t)
	handler := analysisQueueAPIHandler(st, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/api/settings/analysis-queue", strings.NewReader(`{"parse_interval_ms":1}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "parse_interval_ms") {
		t.Fatalf("body = %s, want the field named", recorder.Body.String())
	}
}

func TestAnalysisQueueAPIReportsAndResetsTheBackfillCursor(t *testing.T) {
	st := analysisQueueTestStore(t)
	handler := analysisQueueAPIHandler(st, nil)
	through := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	if err := st.SaveBackfillState(t.Context(), store.BackfillCursor{ProjectThrough: through}); err != nil {
		t.Fatalf("SaveBackfillState() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/settings/analysis-queue", nil))
	var view struct {
		Backfill struct {
			ProjectThrough time.Time `json:"project_through"`
		} `json:"backfill"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Backfill.ProjectThrough.Equal(through) {
		t.Fatalf("backfill = %+v, want the stored cursor", view.Backfill)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/settings/analysis-queue/reset-backfill", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if state := st.LoadBackfillState(t.Context()); !state.ProjectThrough.IsZero() {
		t.Fatalf("state after reset = %+v", state)
	}
}
