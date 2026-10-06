// Analysis and reanalysis surface of the monitor API: the request and response
// types, the HTTP handlers that start and inspect analysis runs, and the helpers
// that turn stored jobs into the views the monitor renders. Moving them out of
// server.go keeps that file to the routes it registers and the endpoints that do
// not belong to this surface.

package monitor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/kingfs/Trajecta/internal/reanalysis"
	"github.com/kingfs/Trajecta/internal/store"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type analysisListResponse struct {
	SessionID string            `json:"session_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	Items     []analysisRunView `json:"items"`
	Total     int               `json:"total"`
}

type analysisRunView struct {
	ID              int64     `json:"id"`
	TraceID         string    `json:"trace_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	Kind            string    `json:"kind"`
	Analyzer        string    `json:"analyzer"`
	AnalyzerVersion string    `json:"analyzer_version"`
	Model           string    `json:"model,omitempty"`
	InputRef        string    `json:"input_ref"`
	Output          any       `json:"output"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

type analysisJobListResponse struct {
	Items []analysisJobView `json:"items"`
	Total int               `json:"total"`
}

type analysisJobResponse struct {
	Job analysisJobView `json:"job"`
}

type reanalysisResponse struct {
	Job    analysisJobView `json:"job"`
	Result any             `json:"result,omitempty"`
}

type reanalysisRequest struct {
	Mode            string `json:"mode"`
	Scan            bool   `json:"scan"`
	RewriteCassette bool   `json:"rewrite_cassette"`
	Reparse         bool   `json:"reparse"`
}

type batchReanalysisRequest struct {
	Mode         string `json:"mode"`
	Query        string `json:"q"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Endpoint     string `json:"endpoint"`
	Upstream     string `json:"upstream"`
	Status       string `json:"status"`
	Observation  string `json:"observation"`
	MissingUsage bool   `json:"missing_usage"`
	Limit        int    `json:"limit"`
	RepairUsage  bool   `json:"repair_usage"`
	Reparse      bool   `json:"reparse"`
	Scan         bool   `json:"scan"`
}

type analysisJobView struct {
	ID         int64     `json:"id"`
	JobType    string    `json:"job_type"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Status     string    `json:"status"`
	Steps      []string  `json:"steps"`
	Request    any       `json:"request"`
	Result     any       `json:"result"`
	LastError  string    `json:"last_error,omitempty"`
	Attempts   int       `json:"attempts"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

func handleSessionAnalysis(w http.ResponseWriter, r *http.Request, st *store.Store, sessionID string) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	limit := parseInt(r.URL.Query().Get("limit"), 20)
	runs, err := st.ListAnalysisRuns(sessionID, "", kind, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query analysis runs: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, analysisListResponse{
		SessionID: sessionID,
		Items:     analysisRunViews(runs),
		Total:     len(runs),
	})
}

func handleSessionReanalyze(w http.ResponseWriter, r *http.Request, st *store.Store, sessionID string) {
	req, ok := decodeReanalysisRequest(w, r)
	if !ok {
		return
	}
	opts := reanalysis.SessionOptions{
		Reparse: req.Reparse,
		Scan:    req.Scan,
	}
	mode := requestMode(req.Mode, "async")
	svc := reanalysis.New(st, reanalysis.Options{})
	if mode == "async" {
		job, err := svc.EnqueueSessionReanalyze(sessionID, opts)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, reanalysisResponse{Job: analysisJobViewFromStore(job)})
		return
	}
	if mode != "sync" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be sync or async"})
		return
	}
	result, err := svc.ReanalyzeSession(r.Context(), sessionID, opts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, reanalysisResponse{Job: analysisJobViewFromStore(result.Job), Result: result})
}

func analysisListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		limit := parseInt(r.URL.Query().Get("limit"), 50)
		runs, err := st.ListAnalysisRuns("", "", kind, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, analysisListResponse{
			Items: analysisRunViews(runs),
			Total: len(runs),
		})
	}
}

func analysisJobListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		targetType := strings.TrimSpace(r.URL.Query().Get("target_type"))
		targetID := strings.TrimSpace(r.URL.Query().Get("target_id"))
		limit := parseInt(r.URL.Query().Get("limit"), 50)
		jobs, err := st.ListAnalysisJobs(status, targetType, targetID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		items := make([]analysisJobView, 0, len(jobs))
		for _, job := range jobs {
			items = append(items, analysisJobViewFromStore(job))
		}
		writeJSON(w, http.StatusOK, analysisJobListResponse{Items: items, Total: len(items)})
	}
}

func analysisBatchReanalyzeAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req batchReanalysisRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
			return
		}
		opts := reanalysis.BatchOptions{
			Filter: store.ListFilter{
				Query:             strings.TrimSpace(req.Query),
				Provider:          strings.TrimSpace(req.Provider),
				Model:             strings.TrimSpace(req.Model),
				Endpoint:          strings.TrimSpace(req.Endpoint),
				SelectedUpstream:  strings.TrimSpace(req.Upstream),
				Status:            strings.TrimSpace(req.Status),
				ObservationStatus: strings.TrimSpace(req.Observation),
				MissingUsage:      req.MissingUsage,
			},
			Limit:       req.Limit,
			RepairUsage: req.RepairUsage,
			Reparse:     req.Reparse,
			Scan:        req.Scan,
		}
		mode := requestMode(req.Mode, "async")
		svc := reanalysis.New(st, reanalysis.Options{})
		switch mode {
		case "async":
			job, err := svc.EnqueueBatchReanalyze(opts)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusAccepted, reanalysisResponse{Job: analysisJobViewFromStore(job)})
		case "sync":
			result, err := svc.ReanalyzeBatch(r.Context(), opts)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, reanalysisResponse{Job: analysisJobViewFromStore(result.Job), Result: result})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be sync or async"})
		}
	}
}

func analysisJobDetailAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(pathClean(r.URL.Path), "/api/analysis/jobs/")
		path = strings.Trim(path, "/")
		if path == "" {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(path, "/")
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
			return
		}
		if len(parts) == 2 {
			if parts[1] == "cancel" && r.Method == http.MethodPost {
				if err := st.MarkAnalysisJobCanceled(id); err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				job, err := st.GetAnalysisJob(id)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, analysisJobResponse{Job: analysisJobViewFromStore(job)})
				return
			}
			http.NotFound(w, r)
			return
		}
		if len(parts) != 1 || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		job, err := st.GetAnalysisJob(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, analysisJobResponse{Job: analysisJobViewFromStore(job)})
	}
}

func handleTraceReanalyze(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	req, ok := decodeReanalysisRequest(w, r)
	if !ok {
		return
	}
	runTraceReanalysis(w, r, st, requestMode(req.Mode, "sync"), func(svc *reanalysis.Service) (store.AnalysisJobRecord, error) {
		return svc.EnqueueTraceReanalyze(entry.ID)
	}, func(svc *reanalysis.Service) (reanalysis.Result, error) {
		return svc.ReanalyzeTrace(r.Context(), entry.ID)
	})
}

func runTraceReanalysis(
	w http.ResponseWriter,
	r *http.Request,
	st *store.Store,
	mode string,
	enqueue func(*reanalysis.Service) (store.AnalysisJobRecord, error),
	runSync func(*reanalysis.Service) (reanalysis.Result, error),
) {
	svc := reanalysis.New(st, reanalysis.Options{})
	switch mode {
	case "async":
		job, err := enqueue(svc)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, reanalysisResponse{Job: analysisJobViewFromStore(job)})
	case "sync":
		result, err := runSync(svc)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, reanalysisResponse{Job: analysisJobViewFromStore(result.Job), Result: result})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be sync or async"})
	}
}

func decodeReanalysisRequest(w http.ResponseWriter, r *http.Request) (reanalysisRequest, bool) {
	var req reanalysisRequest
	if r.Body == nil || r.ContentLength == 0 {
		return req, true
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return reanalysisRequest{}, false
	}
	return req, true
}

func analysisRunViews(runs []store.AnalysisRunRecord) []analysisRunView {
	out := make([]analysisRunView, 0, len(runs))
	for _, run := range runs {
		var output any = map[string]any{}
		if strings.TrimSpace(run.OutputJSON) != "" {
			_ = json.Unmarshal([]byte(run.OutputJSON), &output)
		}
		out = append(out, analysisRunView{
			ID:              run.ID,
			TraceID:         run.TraceID,
			SessionID:       run.SessionID,
			Kind:            run.Kind,
			Analyzer:        run.Analyzer,
			AnalyzerVersion: run.AnalyzerVersion,
			Model:           run.Model,
			InputRef:        run.InputRef,
			Output:          output,
			Status:          run.Status,
			CreatedAt:       run.CreatedAt,
		})
	}
	return out
}

func analysisJobViewFromStore(job store.AnalysisJobRecord) analysisJobView {
	var steps []string
	if strings.TrimSpace(job.StepsJSON) != "" {
		_ = json.Unmarshal([]byte(job.StepsJSON), &steps)
	}
	var request any = map[string]any{}
	if strings.TrimSpace(job.RequestJSON) != "" {
		_ = json.Unmarshal([]byte(job.RequestJSON), &request)
	}
	var result any = map[string]any{}
	if strings.TrimSpace(job.ResultJSON) != "" {
		_ = json.Unmarshal([]byte(job.ResultJSON), &result)
	}
	return analysisJobView{
		ID:         job.ID,
		JobType:    job.JobType,
		TargetType: job.TargetType,
		TargetID:   job.TargetID,
		Status:     job.Status,
		Steps:      steps,
		Request:    request,
		Result:     result,
		LastError:  job.LastError,
		Attempts:   job.Attempts,
		CreatedAt:  job.CreatedAt,
		UpdatedAt:  job.UpdatedAt,
		StartedAt:  job.StartedAt,
		FinishedAt: job.FinishedAt,
	}
}
